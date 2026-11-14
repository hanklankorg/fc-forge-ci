// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"codefloe.com/itiquette/forge-lab-contract/labcontract"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// A release through the layer needs what a consumer's organization brings:
// a signer image, a bot key that may push, a Cosign key. A scenario makes the
// keys itself, for one run, and they go with its scratch repository. The
// signer image is the one thing it is handed, because building it is minutes
// of work that belongs to a commit and not to a test.

// signerImageEnv names the digest-pinned signer image the release scenarios
// run their signer jobs in. `just test-live-image signer` builds it from this
// checkout, publishes it to the lab registry and prints the value.
const signerImageEnv = "FORGE_CI_LIVE_SIGNER_IMAGE"

// digestPinnedImage is the layer's image pin, the shape every Forgejo workflow
// validates (test/forgejoguard, canonicalImagePin): a repository coordinate, an
// optional tag and the digest.
var digestPinnedImage = regexp.MustCompile(`^[a-z0-9]+([.-][a-z0-9]+)*(:[0-9]+)?/([a-z0-9]+(([._]|__|-+)[a-z0-9]+)*/)*` +
	`[a-z0-9]+(([._]|__|-+)[a-z0-9]+)*(:` + container.OCITagComponent + `)?@sha256:[0-9a-f]{64}$`)

// SignerImage returns the signer image this run was given, or false after
// logging what to do about it.
//
// Logf and false, as Requires answers an unmet need: the image is a fact of
// the environment, and a scenario that cannot have it skips where a reader
// sees why, with the recipe that supplies it. A value that is set and not a
// digest-pinned reference on a lab host is a mistake and fails.
func SignerImage(tb TB) (string, bool) {
	tb.Helper()

	image := os.Getenv(signerImageEnv)
	if image == "" {
		tb.Logf("SKIP: %s is not set; run `just test-live-image signer <forge-lab checkout>` and pass the reference it prints", signerImageEnv)

		return "", false
	}

	host, _, _ := strings.Cut(image, "/")
	if !digestPinnedImage.MatchString(image) || disposableImageHost(host) != nil {
		tb.Fatalf("livetest: %s must be a digest-pinned image on a lab registry, as `just test-live-image signer` prints it", signerImageEnv)
	}

	return image, true
}

// disposableImageHost holds host to the contract module's disposable-host list.
func disposableImageHost(host string) error {
	if _, err := parseOrigin("https://" + host); err != nil {
		return err
	}

	return labcontract.Disposable(host)
}

// SSHRoute is the SSH clone URL a JOB pushes a scratch repository by and the
// fingerprint of the host key behind it, or false after logging why there is
// none.
//
// The fingerprint is the contract's. The address is the contract module's
// runner-side one: ssh.base_url is the route from the operator's side, a
// port the lab publishes on the host, and a job container on the lab's own
// network reaches the same server, with the same host key, by another name.
func SSHRoute(tb TB, target Target, repo string) (string, string, bool) {
	tb.Helper()
	requireAccepted(tb, target)

	if target.endpoint.SSH == nil {
		tb.Logf("SKIP %s: the contract publishes no ssh route for endpoint %q", target.Forge, target.endpoint.Name)

		return "", "", false
	}

	fingerprint := target.endpoint.SSH.HostKeyFingerprint

	authority := labcontract.RunnerSSHAuthority(target.Host)
	if authority == "" {
		tb.Logf("SKIP %s: the contract module knows no runner-side SSH route for host %q", target.Forge, target.Host)

		return "", "", false
	}

	return "ssh://git@" + authority + "/" + target.Owner + "/" + repo + ".git", fingerprint, true
}

// sshKeyFile is the name SSHKey gives the private key in its directory.
const sshKeyFile = "lab-ssh"

// SSHKey generates a throwaway Ed25519 key pair in dir and returns the private
// key's text and the public key line. No passphrase: the key exists for one
// run and its only authority is the scratch repository it is a deploy key of.
func SSHKey(tb TB, dir string) (string, string) {
	tb.Helper()

	// Not among LauncherHostTools: it is OpenSSH's, which
	// git needs on this host anyway to verify the signatures the bot makes.
	sshKeygen := HostTool(tb, "ssh-keygen")

	ctx, cancel := context.WithTimeout(Context(tb), timeoutAPI)
	defer cancel()

	path := filepath.Join(dir, sshKeyFile)

	cmd := exec.CommandContext(ctx, sshKeygen, "-q", "-t", "ed25519", "-N", "", "-C", "livetest", "-f", path) //nolint:gosec // G204: kit-authored arguments.
	if out, err := cmd.CombinedOutput(); err != nil {
		tb.Fatalf("livetest: ssh-keygen: %v\n%s", err, out)
	}

	private, err := os.ReadFile(path) //nolint:gosec // The key this function just generated.
	if err != nil {
		tb.Fatalf("livetest: read the generated SSH key: %v", err)
	}

	public, err := os.ReadFile(path + ".pub") //nolint:gosec // The key this function just generated.
	if err != nil {
		tb.Fatalf("livetest: read the generated SSH public key: %v", err)
	}

	return string(private), strings.TrimSpace(string(public))
}

// requestSigner is the name a signed release request is made under. It is the
// tagger of the tag and the principal of the allowed-signers entry.
const requestSigner = "livetest@localhost"

// RequestSigner signs a release request as a person with an SSH key would,
// for a consumer that asks the layer to verify who requested a release.
type RequestSigner struct {
	keyPath string
	// AllowedSigners is the line the consumer keeps in its allowed-signers
	// file to trust this signer.
	AllowedSigners string
}

// NewRequestSigner makes a signer with a throwaway key in dir, which must
// hold no other key of the kit's.
func NewRequestSigner(tb TB, dir string) RequestSigner {
	tb.Helper()

	_, public := SSHKey(tb, dir)

	return RequestSigner{keyPath: filepath.Join(dir, sshKeyFile), AllowedSigners: requestSigner + " " + public + "\n"}
}

// PushTag creates the annotated tag at the head of a branch of the scratch
// repository, signed, and pushes it with git, as a person does. The forge's
// API cannot do it: a tag it creates is the forge's own and unsigned.
func (signer RequestSigner) PushTag(tb TB, target Target, repo, tag, branch, message string) {
	tb.Helper()

	mirror := MirrorRepo(tb, target, repo)

	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()

	err := signer.tag(ctx, mirror.git, mirror.dir, tag, "refs/heads/"+branch, message)
	if err == nil {
		err = mirror.git.runPush(ctx, mirror.dir, scratchRemote(target, repo), "refs/tags/"+tag)
	}

	if err != nil {
		tb.Fatalf("livetest: push the signed tag %s to %s/%s: %v", tag, target.Owner, repo, err)
	}
}

func (signer RequestSigner) tag(ctx context.Context, git layerGit, dir, tag, commit, message string) error {
	_, err := git.run(ctx, dir, "-c", "gpg.format=ssh", "-c", "user.signingkey="+signer.keyPath, "tag", "-s", "-m", message, tag, commit)

	return err
}

// GPGKey generates a throwaway Ed25519 signing key in dir and returns its
// armored private key, under passphrase, and its fingerprint: what the signer
// job needs to sign a release's distribution packages.
func GPGKey(tb TB, dir, passphrase string) (string, string) {
	tb.Helper()

	// Not among LauncherHostTools: only a release that
	// signs packages needs it.
	gpgBinary := HostTool(tb, "gpg")
	gpgconf := HostTool(tb, "gpgconf")

	home := filepath.Join(dir, "gnupg")
	if err := os.Mkdir(home, 0o700); err != nil {
		tb.Fatalf("livetest: create a GnuPG home: %v", err)
	}

	ctx, cancel := context.WithTimeout(Context(tb), timeoutAPI)
	defer cancel()

	gpg := func(args ...string) string {
		args = append([]string{"--homedir", home, "--batch", "--pinentry-mode", "loopback", "--passphrase-fd", "0"}, args...)

		cmd := exec.CommandContext(ctx, gpgBinary, args...) //nolint:gosec // G204: kit-authored arguments.
		cmd.Stdin = strings.NewReader(passphrase + "\n")

		for key, value := range runtimeEnvironment(tb) {
			cmd.Env = append(cmd.Env, key+"="+value)
		}

		var stdout, stderr strings.Builder

		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			tb.Fatalf("livetest: gpg %s: %v\n%s", args[len(args)-1], err, stderr.String())
		}

		return stdout.String()
	}

	// The agent gpg starts outlives the command; it goes with the key.
	tb.Cleanup(func() {
		ctx, cancel := cleanupContext(timeoutAPI)
		defer cancel()

		if err := exec.CommandContext(ctx, gpgconf, "--homedir", home, "--kill", "gpg-agent").Run(); err != nil { //nolint:gosec // G204: kit-authored arguments; bounded cleanup outlives the test's context.
			tb.Errorf("livetest: stop the fixture GPG agent: %v", err)
		}
	})

	const identity = "livetest <livetest@invalid.example>"

	gpg("--quick-generate-key", identity, "ed25519", "sign", "1d")

	fingerprint := ""

	for line := range strings.SplitSeq(gpg("--with-colons", "--list-secret-keys", identity), "\n") {
		if fields := strings.Split(line, ":"); fields[0] == "fpr" && len(fields) > 9 {
			fingerprint = fields[9]

			break
		}
	}

	if len(fingerprint) != 40 {
		tb.Fatalf("livetest: gpg listed no fingerprint for the generated key")
	}

	return gpg("--armor", "--export-secret-keys", fingerprint), fingerprint
}

// AddDeployKey registers a public key on the scratch repository with write
// access. It is the narrowest grant a release bot can have: one repository,
// gone with it, and nothing on the account.
func AddDeployKey(tb TB, target Target, repo, title, publicKey string) {
	tb.Helper()
	requireForgejoActions(tb, target)

	ctx, cancel := context.WithTimeout(Context(tb), timeoutAPI)
	defer cancel()

	body := map[string]any{"title": title, "key": publicKey, "read_only": false}
	if err := discard(ctx, target, http.MethodPost, actionsURL(target, repo, "/keys"), body, http.StatusCreated); err != nil {
		tb.Fatalf("livetest: add deploy key to %s/%s: %v", target.Owner, repo, err)
	}
}

// Release is a release as the forge lists it.
type Release struct {
	TagName    string         `json:"tag_name"`
	Body       string         `json:"body"`
	Draft      bool           `json:"draft"`
	Prerelease bool           `json:"prerelease"`
	Assets     []ReleaseAsset `json:"assets"`
}

// ReleaseAsset is one file attached to a release.
type ReleaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	// Size is what the file adds to the owner's release storage.
	Size int64 `json:"size"`
}

// ReadRelease reads the release of a tag with its assets.
func ReadRelease(tb TB, target Target, repo, tag string) Release {
	tb.Helper()

	var release Release

	readActions(tb, target, repo, "/releases/tags/"+url.PathEscape(tag), &release)

	return release
}

// Releases lists the repository's releases, drafts included: the forge does
// not answer for a draft by its tag.
func Releases(tb TB, target Target, repo string) []Release {
	tb.Helper()

	var releases []Release

	readList(tb, target, repo, "/releases", &releases, func() int { return len(releases) })

	return releases
}

// Download fetches a URL the forge handed out, such as a release asset's, and
// returns the body. The transport reaches lab hosts only, so a URL that
// points elsewhere fails here and is not followed.
func Download(tb TB, target Target, address string, limit int64) []byte {
	tb.Helper()

	return DownloadContext(Context(tb), tb, target, address, limit)
}

// DownloadContext downloads evidence within the caller's operation deadline.
func DownloadContext(ctx context.Context, tb TB, target Target, address string, limit int64) []byte {
	tb.Helper()
	requireAccepted(tb, target)

	ctx, cancel := context.WithTimeout(ctx, timeoutAPI)
	defer cancel()

	body, err := fetch(ctx, target, address, limit)
	if err != nil {
		tb.Fatalf("livetest: download: %v", err)
	}

	return body
}

// DownloadIfPresent is Download for a file a run may not have produced: it
// reports false when the forge answers 404, and fails on any other error.
func DownloadIfPresent(tb TB, target Target, address string, limit int64) ([]byte, bool) {
	tb.Helper()
	requireAccepted(tb, target)

	ctx, cancel := context.WithTimeout(Context(tb), timeoutAPI)
	defer cancel()

	body, err := fetch(ctx, target, address, limit)
	if errors.Is(err, errAbsent) {
		return nil, false
	}

	if err != nil {
		tb.Fatalf("livetest: download: %v", err)
	}

	return body, true
}

// VerifyBlob checks a Cosign bundle over a blob against a public key with
// the real cosign binary, and returns cosign's complaint when it refuses.
//
// The transparency log is waived, which is what verifying a signature made
// with the layer's default asks of every verifier. A signature that verified
// without the waiver would be one that had been written to the public log.
func VerifyBlob(tb TB, target Target, publicKey string, blob, bundle []byte) error {
	tb.Helper()

	dir, err := os.MkdirTemp("", "livetest-verify-")
	if err != nil {
		tb.Fatalf("livetest: create a verification directory: %v", err)
	}

	defer func() { _ = os.RemoveAll(dir) }()

	blobPath, bundlePath := filepath.Join(dir, "blob"), filepath.Join(dir, "blob.bundle")
	if err := os.WriteFile(blobPath, blob, 0o600); err != nil {
		tb.Fatalf("livetest: stage the blob: %v", err)
	}

	if err := os.WriteFile(bundlePath, bundle, 0o600); err != nil {
		tb.Fatalf("livetest: stage the bundle: %v", err)
	}

	ctx, cancel := context.WithTimeout(Context(tb), timeoutAPI)
	defer cancel()

	cmd := exec.CommandContext(ctx, HostTool(tb, "cosign"), "verify-blob", //nolint:gosec // G204: kit-authored arguments and paths.
		"--key", publicKey, "--bundle", bundlePath, "--new-bundle-format", "--insecure-ignore-tlog=true", blobPath)
	cmd.Env = ToolEnvironment(tb, target, nil)

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("cosign verify-blob: %w: %s", err, redactTargetSecrets(target, strings.TrimSpace(string(out))))
	}

	return nil
}

// RepoMirror is a local, bare copy of a scratch repository's branches and
// tags, for the questions the forge's API answers badly: who signed a commit,
// and how two commits are related.
type RepoMirror struct {
	git layerGit
	dir string
}

// MirrorRepo clones every branch and tag of the scratch repository into a
// directory removed with the test.
func MirrorRepo(tb TB, target Target, repo string) RepoMirror {
	tb.Helper()
	requireAccepted(tb, target)

	dir, err := os.MkdirTemp("", "livetest-mirror-")
	if err != nil {
		tb.Fatalf("livetest: create a mirror directory: %v", err)
	}

	tb.Cleanup(func() { _ = os.RemoveAll(dir) })

	gitBase := strings.TrimSuffix(target.endpoint.GitURL(), "/")
	mirror := RepoMirror{dir: dir, git: layerGit{
		target: target,
		env:    ToolEnvironment(tb, target, layerGitEnvironment(gitBase, target.CredentialUsername, target.Token)),
	}}

	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()

	// A clone, so the mirror takes the repository's object format: a consumer's
	// history may be SHA-256. A bare clone fetches the branches and the tags.
	if _, err := mirror.git.run(ctx, dir, "clone", gitQuiet, "--bare", scratchRemote(target, repo), "."); err != nil {
		tb.Fatalf("livetest: mirror %s/%s: %v", target.Owner, repo, err)
	}

	return mirror
}

// RequireMergeOf holds merge to a merge commit of base and head, in that
// order, whose tree is head's: what a native merge of a change that builds on
// its base leaves. The forge takes no base commit when it merges, so a base
// that moved shows here.
func (mirror RepoMirror) RequireMergeOf(tb TB, merge, base, head string) {
	tb.Helper()

	ctx, cancel := context.WithTimeout(Context(tb), timeoutAPI)
	defer cancel()

	if err := mirror.mergeOf(ctx, merge, base, head); err != nil {
		tb.Fatalf("livetest: the merge %s %v", merge, err)
	}
}

// Git runs one git command in the mirror and returns its trimmed output.
func (mirror RepoMirror) Git(tb TB, args ...string) (string, error) {
	tb.Helper()

	ctx, cancel := context.WithTimeout(Context(tb), timeoutAPI)
	defer cancel()

	return mirror.git.run(ctx, mirror.dir, args...)
}

func (mirror RepoMirror) mergeOf(ctx context.Context, merge, base, head string) error {
	return gitMergeOf(ctx, func(ctx context.Context, args ...string) (string, error) {
		return mirror.git.run(ctx, mirror.dir, args...)
	}, merge, base, head)
}

// gitMergeOf is RequireMergeOf's check on any clone that holds the three
// commits, read with git, whose runner returns trimmed output: the lab
// mirror's, or the hosted trial's working copy.
func gitMergeOf(ctx context.Context, git func(context.Context, ...string) (string, error), merge, base, head string) error {
	parents, err := git(ctx, "rev-list", "--parents", "-n", "1", merge)
	if err != nil {
		return err
	}

	if parents != merge+" "+base+" "+head {
		return fmt.Errorf("is %q, want the parents %s and %s: %w", parents, base, head, errs.ErrValidation)
	}

	mergeTree, err := git(ctx, "rev-parse", merge+"^{tree}")
	if err != nil {
		return err
	}

	headTree, err := git(ctx, "rev-parse", head+"^{tree}")
	if err != nil {
		return err
	}

	if mergeTree != headTree {
		return fmt.Errorf("has the tree %s, want the change's %s: %w", mergeTree, headTree, errs.ErrValidation)
	}

	return nil
}
