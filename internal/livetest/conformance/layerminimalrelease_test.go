// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/livetest"
)

//go:embed testdata/layer/minimal-binary-release.yml
var layerMinimalReleaseFixture string

// The consumer that is released: one command, and a changelog with an entry
// to release.
const (
	releaseCanaryMain = `package main

import "fmt"

var version = "development"

func main() {
	fmt.Printf("layer-release-canary %s\n", version)
}
`

	releaseCanaryChangelog = `# Changelog

All notable changes to this acceptance fixture will be documented in this file.

## [Unreleased]

### Added

- Add the minimal signed binary release canary.
`

	releaseRequestTag = "release-request/v0.0.1"
	releaseFinalTag   = "v0.0.1"
	releaseBotEmail   = "layer-acceptance@invalid.example"

	releaseArchive    = "layer-release-canary_0.0.1_linux_amd64.tar.gz"
	releaseSBOM       = releaseArchive + ".sbom.json"
	releaseChecksums  = "layer-release-canary_0.0.1_checksums.txt"
	releaseProvenance = "slsa-provenance.intoto.json"
	releaseManifest   = "release-files.json"
	releaseNotes      = "release-notes.md"
	bundleSuffix      = ".bundle"

	maxReleaseFile = 64 << 20
)

// releaseAssets are the files a reader of the release can download: what the
// build declared, the provenance statement, and a signature bundle for each
// file that is signed.
func releaseAssets() []string {
	return []string{
		releaseChecksums, releaseChecksums + bundleSuffix,
		releaseArchive, releaseSBOM, releaseSBOM + bundleSuffix,
		releaseProvenance, releaseProvenance + bundleSuffix,
	}
}

// LAYER-4: a release request becomes a signed, published release.
//
// The whole promise of the layer in its smallest form: a consumer pushes a
// request tag; prepare-release.yml writes the changelog, signs the release
// commit and the final tag with the bot's SSH key and pushes both; the
// consumer's own job builds with the layer's Go build and hands dist/ over
// by digest; sign-and-publish-release.yml signs in the signer image, with
// keys the build job never saw, and publishes the release.
//
// The host then checks what a user of that release would rely on, with no
// product in the path: main advanced by exactly one commit, a child of the
// request, signed by the bot key, and the final tag is signed and points at
// it; the signed hand-off is the build's files byte for byte plus signatures
// and provenance; the release is published under the final tag with exactly
// the seven public files, each identical to the signed hand-off; the
// checksums and the provenance subjects name the archive and the SBOM by
// their real digests; cosign verifies all three bundles against the public
// key, with the transparency log waived as the layer's default requires; the
// archive holds the binary; no job log holds a key or a passphrase; and the
// owner's packages are as they were.
func TestForgejoLayer_MinimalBinaryRelease(t *testing.T) {
	for _, visibility := range livetest.ConsumerVisibilities(t) {
		t.Run(visibility.Name, func(t *testing.T) {
			format := pairedObjectFormat(visibility)
			t.Run(format, func(t *testing.T) { runLayerMinimalRelease(t, visibility.Private, format) })
		})
	}
}

func runLayerMinimalRelease(t *testing.T, private bool, objectFormat string) {
	t.Helper()

	const (
		workflow = "layer-minimal-release"
		name     = "layer-minimal-release"
	)

	signer, ok := livetest.SignerImage(t)
	if !ok {
		t.Skip("no signer image, so the signer jobs cannot run")
	}

	gitURL, fingerprint, ok := livetest.SSHRoute(t, livetest.Accept(t, provider.ForgeForgejo), livetest.ScratchRepo(name))
	if !ok {
		t.Skip("no SSH route, so the release commit cannot be pushed")
	}

	scenario := newLayerScenario(t, name, private, objectFormat)
	target, repo := scenario.target, scenario.repo

	keys := newReleaseKeys(t)
	livetest.AddDeployKey(t, target, repo, "livetest release bot", keys.sshPublic)

	for secret, value := range map[string]string{
		"RELEASE_SSH_SIGNING_KEY":    keys.sshPrivate,
		"RELEASE_COSIGN_PRIVATE_KEY": keys.cosignPrivate,
		"RELEASE_COSIGN_PASSPHRASE":  keys.passphrase,
		"RELEASE_TOKEN":              target.Token,
	} {
		livetest.SetRepoSecret(t, target, repo, secret, value)
	}

	rendered := scenario.render(t, layerMinimalReleaseFixture,
		"RENDER_SIGNER_IMAGE", signer,
		"RENDER_COSIGN_PUBLIC_KEY_SHA256", keys.cosignPublicSHA256,
		// The key is a literal block in the fixture, indented eight spaces.
		"RENDER_COSIGN_PUBLIC_KEY", strings.ReplaceAll(strings.TrimSuffix(keys.cosignPublic, "\n"), "\n", "\n        "),
		"RENDER_RELEASE_GIT_URL", gitURL,
		"RENDER_RELEASE_GIT_HOST_KEY_FINGERPRINT", fingerprint,
	)

	request := livetest.CommitFiles(t, target, repo, livetest.FileCommit{
		Branch: layerMain, Message: "feat: add the minimal signed binary release canary",
		Files: map[string]string{
			"go.mod":                                  "module example.invalid/layer-release-canary\n\ngo 1.27.2\n",
			".chglog/config-keepachangelog.yml":       chglogKeepConfig,
			".chglog/CHANGELOG-keepachangelog.tpl":    chglogKeepTemplate,
			".chglog/config-minimal.yml":              chglogMinimalConfig,
			".chglog/CHANGELOG-minimal.tpl":           chglogMinimalTemplate,
			"CHANGELOG.md":                            releaseCanaryChangelog,
			"cmd/layer-release-canary/main.go":        releaseCanaryMain,
			".forgejo/workflows/" + workflow + ".yml": rendered,
		},
	})

	packages := livetest.OwnerPackages(t, target)

	// The request is an annotated tag on the head of main, and pushing it is
	// the trigger.
	livetest.AnnotateTag(t, target, repo, releaseRequestTag, layerMain, "Request release v0.0.1")

	run := livetest.WaitForLayerRun(t, target, repo, workflow, "push")
	jobs := scenario.requireStatus(t, run, jobSuccess)

	requireReleaseJobs(t, jobs, run.ID, scenario.label)
	main := requireReleaseGit(t, target, repo, keys, request, releaseRequestTag, releaseFinalTag)
	scenario.requireBranches(t, map[string]string{layerMain: main})

	signed := requireReleaseHandOff(t, scenario, run.ID)
	requireReleaseAssets(t, scenario, keys, signed)

	// A log is the one place a key would surface, so every job's is read.
	for _, job := range jobs {
		if len(job.RunsOn) == 0 {
			continue
		}

		log := livetest.JobLog(t, target, repo, job)

		for what, secret := range map[string]string{"SSH signing key": keys.sshPrivate, "Cosign private key": keys.cosignPrivate} {
			for _, line := range strings.Split(strings.TrimSpace(secret), "\n") {
				if !strings.HasPrefix(line, "-----") && len(line) >= 16 && strings.Contains(log, line) {
					t.Errorf("the log of job %q holds a line of the %s", job.Name, what)
				}
			}
		}

		if strings.Contains(log, keys.passphrase) {
			t.Errorf("the log of job %q holds the Cosign passphrase", job.Name)
		}
	}

	scenario.requireSamePackages(t, packages)

	if keysOnRepo := livetest.RepoCount(t, target, repo, "/keys"); keysOnRepo != 1 {
		t.Errorf("the repository has %d deploy keys, want only the bot's", keysOnRepo)
	}
}

// releaseKeys is the signing material of one release, made for it.
type releaseKeys struct {
	dir                string
	sshPrivate         string
	sshPublic          string
	cosignPrivate      string
	cosignPublic       string
	cosignPublicPath   string
	cosignPublicSHA256 string
	passphrase         string
}

func newReleaseKeys(t *testing.T) releaseKeys {
	t.Helper()

	keys := releaseKeys{dir: t.TempDir()}
	keys.sshPrivate, keys.sshPublic = livetest.SSHKey(t, keys.dir)

	privatePath, publicPath := trialCosignKey(t, keys.dir)
	keys.cosignPublicPath = publicPath

	passphrase, err := os.ReadFile(filepath.Join(filepath.Dir(privatePath), signingKeyPassphraseFile))
	if err != nil {
		t.Fatal(err)
	}

	keys.passphrase = string(passphrase)

	private, err := os.ReadFile(privatePath)
	if err != nil {
		t.Fatal(err)
	}

	public, err := os.ReadFile(publicPath)
	if err != nil {
		t.Fatal(err)
	}

	// The digest a consumer writes into its workflow is of the key as a file
	// ending in one newline, which is how the signer job writes it back.
	keys.cosignPrivate = string(private)
	keys.cosignPublic = strings.TrimSuffix(string(public), "\n") + "\n"
	digest := sha256.Sum256([]byte(keys.cosignPublic))
	keys.cosignPublicSHA256 = hex.EncodeToString(digest[:])

	return keys
}

// signingKeyDirEnv names a directory whose Cosign key pair the trials reuse,
// as an organization signs every release with one key: a second release then
// finds and verifies the bases the first one signed. Unset, each trial signs
// with a key of its own.
const (
	signingKeyDirEnv         = "FORGE_CI_LIVE_SIGNING_KEY_DIR"
	signingKeyPassphraseFile = "lab-cosign.passphrase"
)

// trialCosignKey returns the passphrase-protected key pair a trial signs
// with: the reused one in FORGE_CI_LIVE_SIGNING_KEY_DIR, made there on first use,
// or a fresh pair in dir.
func trialCosignKey(t *testing.T, dir string) (string, string) {
	t.Helper()

	if reused := os.Getenv(signingKeyDirEnv); reused != "" {
		if !filepath.IsAbs(reused) {
			t.Fatalf("%s must be an absolute directory, not %q", signingKeyDirEnv, reused)
		}

		dir = filepath.Clean(reused)
	}

	privatePath, publicPath := filepath.Join(dir, "lab-cosign.key"), filepath.Join(dir, "lab-cosign.pub")
	if _, err := os.Stat(privatePath); err == nil { //nolint:gosec // the operator's own absolute key directory.
		return privatePath, publicPath
	}

	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}

	passphrase := hex.EncodeToString(random)
	if err := os.WriteFile(filepath.Join(dir, signingKeyPassphraseFile), []byte(passphrase), 0o600); err != nil { //nolint:gosec // the operator's own absolute key directory.
		t.Fatal(err)
	}

	return livetest.CosignKeyWithPassword(t, dir, passphrase)
}

// requireReleaseJobs holds the run to the two called workflows' jobs and the
// consumer's build, each successful, under the two calling jobs.
func requireReleaseJobs(t *testing.T, jobs []livetest.RunJob, runID int64, label string) {
	t.Helper()

	want := []string{
		"Build minimal binary release",
		"Check: release settings",
		"Check: signing settings",
		"Prepare: changelog",
		"Prepare: release commit and tag",
		// Runs with every step skipped when no bump is asked for: Forgejo
		// skips the dependant of a skipped job on the server.
		"Prepare: version",
		"Publish: images",
		"Publish: release",
		"Publish: tidy up",
		"Sign: files and images",
	}

	onRunner, wrappers := requireJobsOnRunner(t, jobs, runID, label)
	if !slices.Equal(onRunner, want) || len(wrappers) != 2 {
		t.Errorf("the run's jobs are %v on the runner and %v without one, want %v under two callers", onRunner, wrappers, want)
	}

	for _, job := range jobs {
		if job.Status != jobSuccess {
			t.Errorf("job %q is %s in a successful run", job.Name, job.Status)
		}
	}
}

// requireReleaseGit asks a mirror of the repository what the release did to
// its history, and git, with the bot's public key as the only allowed signer,
// who signed it. It returns the release commit, the head of main.
func requireReleaseGit(t *testing.T, target livetest.Target, repo string, keys releaseKeys, request, requestTag, finalTag string) string {
	t.Helper()

	mirror := livetest.MirrorRepo(t, target, repo)
	git := func(args ...string) string {
		out, err := mirror.Git(t, args...)
		if err != nil {
			t.Fatalf("release history: %v", err)
		}

		return out
	}

	main := git("rev-parse", "refs/heads/"+layerMain)
	if parents := git("rev-list", "--parents", "-n", "1", main); parents != main+" "+request || git("rev-list", "--count", request+".."+main) != "1" {
		t.Errorf("main is at %q, want one release commit whose only parent is the request %s", parents, request)
	}

	if tagged := git("rev-parse", "refs/tags/"+finalTag+"^{commit}"); tagged != main {
		t.Errorf("%s points at %s, want the release commit %s", finalTag, tagged, main)
	}

	if requested := git("rev-parse", "refs/tags/"+requestTag+"^{commit}"); requested != request {
		t.Errorf("%s moved to %s, want it left at %s", requestTag, requested, request)
	}

	// The principal is only a name for the one key in the file: git asks
	// which principal the signing key belongs to and verifies as that one.
	allowed := filepath.Join(keys.dir, "allowed-signers")
	if err := os.WriteFile(allowed, []byte(releaseBotEmail+" "+keys.sshPublic+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	signature := []string{"-c", "gpg.format=ssh", "-c", "gpg.ssh.allowedSignersFile=" + allowed}
	if _, err := mirror.Git(t, append(signature, "verify-commit", main)...); err != nil {
		t.Errorf("the release commit is not signed by the bot's key: %v", err)
	}

	if _, err := mirror.Git(t, append(signature, "verify-tag", "refs/tags/"+finalTag)...); err != nil {
		t.Errorf("%s is not a tag signed by the bot's key: %v", finalTag, err)
	}

	return main
}

// requireReleaseHandOff reads the rendered changelog, the build's unsigned
// dist and the signer's signed one, and returns the signed files. Signing may
// add files and must not change one the build declared.
func requireReleaseHandOff(t *testing.T, scenario layerScenario, runID int64) map[string][]byte {
	t.Helper()

	trees := map[string]map[string][]byte{}

	artifacts := livetest.RunArtifacts(t, scenario.target, scenario.repo, runID)
	for _, artifact := range artifacts {
		tree, err := livetest.ZIPTree(livetest.ArtifactZIP(t, scenario.target, scenario.repo, artifact.ID), maxReleaseFile)
		if err != nil || artifact.Expired {
			t.Fatalf("artifact %s (expired: %t) is not a plain tree: %v", artifact.Name, artifact.Expired, err)
		}

		trees[artifact.Name] = tree
	}

	unsigned, signed := trees["build-dist"], trees["signed-dist"]
	if len(artifacts) != 3 || unsigned == nil || signed == nil || trees["release-changelog"] == nil {
		t.Fatalf("the run left artifacts %+v, want release-changelog, build-dist and signed-dist", artifacts)
	}

	if got, want := sortedNames(trees["release-changelog"]), sortedNames(namesOf("CHANGELOG.md", "commit-msg.txt")); !slices.Equal(got, want) {
		t.Errorf("the renderer handed over %v, want only %v", got, want)
	}

	if got, want := sortedNames(unsigned), sortedNames(namesOf(releaseArchive, releaseSBOM, releaseChecksums, releaseManifest, releaseNotes)); !slices.Equal(got, want) {
		t.Errorf("the build handed over %v, want %v", got, want)
	}

	if got, want := sortedNames(signed), sortedNames(namesOf(append(releaseAssets(), releaseManifest, releaseNotes)...)); !slices.Equal(got, want) {
		t.Errorf("the signer handed over %v, want %v", got, want)
	}

	notes := string(unsigned[releaseNotes])
	if !strings.HasPrefix(notes, "## [0.0.1]") ||
		!strings.Contains(notes, "minimal signed binary release canary") ||
		strings.Contains(notes, "## [Unreleased]") {
		t.Errorf("release notes do not contain only the prepared version section: %q", notes)
	}

	// The manifest is the one file the signer rewrites: it gains the
	// signatures and the provenance.
	for name, content := range unsigned {
		if name != releaseManifest && !bytes.Equal(signed[name], content) {
			t.Errorf("signing changed %s, which the build declared", name)
		}
	}

	return signed
}

// requireReleaseAssets holds the published release to the signed hand-off and
// checks each file for what it claims.
func requireReleaseAssets(t *testing.T, scenario layerScenario, keys releaseKeys, signed map[string][]byte) {
	t.Helper()

	target, repo := scenario.target, scenario.repo

	if releases, tags := livetest.RepoCount(t, target, repo, "/releases"), livetest.RepoCount(t, target, repo, "/tags"); releases != 1 || tags != 2 {
		t.Errorf("the repository has %d releases and %d tags, want one release and the request and final tags", releases, tags)
	}

	release := livetest.ReadRelease(t, target, repo, releaseFinalTag)
	if release.TagName != releaseFinalTag || release.Draft || release.Prerelease {
		t.Errorf("the release is %+v, want a published release of %s", release, releaseFinalTag)
	}

	if strings.TrimSpace(release.Body) != strings.TrimSpace(string(signed[releaseNotes])) {
		t.Error("the published release body differs from the changelog-section notes handed to the signer")
	}

	published := make(map[string][]byte, len(release.Assets))

	for _, asset := range release.Assets {
		if _, twice := published[asset.Name]; twice {
			t.Errorf("the release lists %s twice", asset.Name)
		}

		published[asset.Name] = livetest.Download(t, target, asset.URL, maxReleaseFile)
	}

	if got, want := sortedNames(published), sortedNames(namesOf(releaseAssets()...)); !slices.Equal(got, want) {
		t.Fatalf("the release offers %v, want exactly %v", got, want)
	}

	for name, content := range published {
		if len(content) == 0 || !bytes.Equal(content, signed[name]) {
			t.Errorf("the published %s differs from the file the signer handed over", name)
		}
	}

	digests := map[string]string{releaseArchive: sha256Hex(published[releaseArchive]), releaseSBOM: sha256Hex(published[releaseSBOM])}

	wantChecksums := digests[releaseArchive] + "  " + releaseArchive + "\n" + digests[releaseSBOM] + "  " + releaseSBOM + "\n"
	if got := string(published[releaseChecksums]); !sameLines(got, wantChecksums) {
		t.Errorf("the checksums file is %q, want the real digests of the archive and the SBOM:\n%s", got, wantChecksums)
	}

	if subjects, err := provenanceSubjects(published[releaseProvenance]); err != nil || len(subjects) != len(digests) ||
		subjects[releaseArchive] != digests[releaseArchive] || subjects[releaseSBOM] != digests[releaseSBOM] {
		t.Errorf("the provenance statement names subjects %v (%v), want the archive and the SBOM by their digests", subjects, err)
	}

	for _, name := range []string{releaseChecksums, releaseSBOM, releaseProvenance} {
		if err := livetest.VerifyBlob(t, target, keys.cosignPublicPath, published[name], published[name+bundleSuffix]); err != nil {
			t.Errorf("the bundle of %s does not verify against the release's public key: %v", name, err)
		}
	}
	requireReleaseBundleRejections(t, target, keys.cosignPublicPath, published[releaseChecksums], published[releaseChecksums+bundleSuffix])

	if err := archiveHoldsBinary(published[releaseArchive], "layer-release-canary_0.0.1_linux_amd64/layer-release-canary"); err != nil {
		t.Errorf("the release archive: %v", err)
	}
}

// The positive verification uses the actual downloaded release bytes. Keep the
// bundle well-formed when changing its signature so a parser refusal cannot pass.
func requireReleaseBundleRejections(t *testing.T, target livetest.Target, publicKey string, blob, bundle []byte) {
	t.Helper()
	var document map[string]any
	decoder := json.NewDecoder(bytes.NewReader(bundle))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		t.Fatal(err)
	}
	message, ok := document["messageSignature"].(map[string]any)
	if !ok {
		t.Fatal("published bundle has no message signature")
	}
	encoded, ok := message["signature"].(string)
	if !ok {
		t.Fatal("published bundle signature is not a string")
	}
	signature, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(signature) == 0 {
		t.Fatalf("published bundle has no decodable signature: %v", err)
	}
	signature[len(signature)-1] ^= 1
	message["signature"] = base64.StdEncoding.EncodeToString(signature)
	changedBundle, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	_, wrongKey := livetest.CosignKey(t, t.TempDir())
	for _, tc := range []struct {
		name, key    string
		blob, bundle []byte
	}{
		{"changed content", publicKey, append(bytes.Clone(blob), '\n'), bundle},
		{"changed signature", publicKey, blob, changedBundle},
		{"wrong key", wrongKey, blob, bundle},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := livetest.VerifyBlob(t, target, tc.key, tc.blob, tc.bundle)
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(err.Error(), "failed to verify signature") {
				t.Fatalf("release bundle was not rejected for %s: %v", tc.name, err)
			}
		})
	}
}

func namesOf(names ...string) map[string][]byte {
	set := make(map[string][]byte, len(names))
	for _, name := range names {
		set[name] = nil
	}

	return set
}

func sortedNames(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}

	slices.Sort(names)

	return names
}

func sha256Hex(content []byte) string {
	digest := sha256.Sum256(content)

	return hex.EncodeToString(digest[:])
}

// sameLines compares two newline-terminated texts as sets of lines: the order
// of a checksums file is the producer's to choose.
func sameLines(got, want string) bool {
	gotLines, wantLines := strings.Split(got, "\n"), strings.Split(want, "\n")
	slices.Sort(gotLines)
	slices.Sort(wantLines)

	return strings.HasSuffix(got, "\n") && slices.Equal(gotLines, wantLines)
}

// provenanceSubjects reads a SLSA v1 in-toto statement and returns its
// subjects' SHA-256 digests by name.
func provenanceSubjects(statement []byte) (map[string]string, error) {
	var parsed struct {
		Type          string         `json:"_type"`
		PredicateType string         `json:"predicateType"`
		Predicate     map[string]any `json:"predicate"`
		Subject       []struct {
			Name   string            `json:"name"`
			Digest map[string]string `json:"digest"`
		} `json:"subject"`
	}

	if err := json.Unmarshal(statement, &parsed); err != nil {
		return nil, err
	}

	if parsed.Type != "https://in-toto.io/Statement/v1" || parsed.PredicateType != "https://slsa.dev/provenance/v1" || parsed.Predicate == nil {
		return nil, fmt.Errorf("not a SLSA v1 provenance statement: %w", errs.ErrValidation)
	}

	subjects := make(map[string]string, len(parsed.Subject))
	for _, subject := range parsed.Subject {
		subjects[subject.Name] = subject.Digest["sha256"]
	}

	return subjects, nil
}

// archiveHoldsBinary checks that a gzipped tar holds exactly one regular
// file, at path, and that it is an ELF executable.
func archiveHoldsBinary(archive []byte, path string) error {
	unzipped, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return err
	}

	reader := tar.NewReader(unzipped)
	files := 0

	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return err
		}

		if header.Typeflag != tar.TypeReg {
			continue
		}

		files++

		magic := make([]byte, 4)
		if _, err := io.ReadFull(reader, magic); err != nil || header.Name != path || string(magic) != "\x7fELF" {
			return fmt.Errorf("holds %s, which is not the ELF binary at %s: %w", header.Name, path, errs.ErrValidation)
		}
	}

	if files != 1 {
		return fmt.Errorf("holds %d regular files, want the one binary: %w", files, errs.ErrValidation)
	}

	return nil
}
