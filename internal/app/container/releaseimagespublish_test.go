// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	rootcli "github.com/diggsweden/forge-ci/internal/cli"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
	domainsbom "github.com/diggsweden/forge-ci/internal/domain/sbom"
)

type releaseImagesPublishFixture struct {
	in     appcontainer.PublishReleaseImagesInput
	writes *atomic.Int32
	gated  *[]string
	digest map[string]string // <name>-<arch> → staged digest
}

// releaseImagesPublishFixture stages what the build jobs leave for the
// publish job: app for amd64 and arm64, worker for amd64, in a layouts
// directory apart from the job's working directory.
func newReleaseImagesPublishFixture(t *testing.T) releaseImagesPublishFixture {
	t.Helper()
	t.Chdir(t.TempDir())

	var writes atomic.Int32

	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writes.Add(1)
		}

		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)

	host := strings.TrimPrefix(server.URL, "http://")
	layouts := filepath.Join(t.TempDir(), "layouts")
	auth := filepath.Join(t.TempDir(), "auth.json")
	require.NoError(t, os.WriteFile(auth, []byte(`{"auths":{}}`), 0o600))

	plan := pipeline.ReleaseImagesPlan{Images: []pipeline.ReleaseImage{
		{Name: "app", ImageRepository: host + "/org/app", CandidateTag: "staging-v1.2.3", FinalTagName: "v1.2.3", Archs: "amd64\narm64", Scan: true},
		{Name: "worker", ImageRepository: host + "/org/worker", CandidateTag: "staging-v1.2.3", FinalTagName: "v1.2.3", Archs: "amd64", Scan: true},
	}}

	digests := map[string]string{}

	for _, image := range plan.Images {
		for arch := range strings.FieldsSeq(image.Archs) {
			dir := filepath.Join(layouts, image.Name+"-arch-"+arch)
			tag := image.ImageRepository + ":staging-v1.2.3-" + arch
			_, staged := stagedLayout(t, t.Context(), filepath.Join(dir, "layout"), tag, arch)
			digest, err := staged.Digest()
			require.NoError(t, err)

			body, err := json.Marshal(appcontainer.SignerImageArchMetadata{Arch: arch, Tag: tag, Digest: digest.String(), Ref: image.ImageRepository + "@" + digest.String()})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, image.Name+"-"+arch+".json"), body, 0o600))

			digests[image.Name+"-"+arch] = digest.String()
		}
	}

	gated := []string{}

	return releaseImagesPublishFixture{
		in: appcontainer.PublishReleaseImagesInput{
			Plan: plan, LayoutsDir: layouts, OutputDir: filepath.Join(t.TempDir(), "images"), AuthFile: auth,
			ReleaseTag: "v1.2.3", ReleaseSHA: strings.Repeat("b", 40), ServerURL: server.URL, Repository: "org/app", RetryAttempts: 1,
			Trivy: &fakeImageEvidenceTrivy{},
			Gate: func(_ context.Context, _, platform string) error {
				gated = append(gated, platform)

				return nil
			},
		},
		writes: &writes,
		gated:  &gated,
		digest: digests,
	}
}

func (f releaseImagesPublishFixture) publish(t *testing.T) error {
	t.Helper()

	return appcontainer.PublishReleaseImages(t.Context(), ociregistry.WithAuthFile(f.in.AuthFile), nil, io.Discard, f.in)
}

// Every planned image is scanned, gated and pushed, and the one ledger the
// job writes records each of them by the digest of the index it pushed.
func TestPublishReleaseImagesRecordsEveryImageInOneLedger(t *testing.T) {
	fixture := newReleaseImagesPublishFixture(t)

	require.NoError(t, fixture.publish(t))
	require.Equal(t, []string{"linux/amd64", "linux/arm64", "linux/amd64"}, *fixture.gated)

	for _, name := range []string{"trivy-results-app-amd64.json", "trivy-results-app-arm64.json", "trivy-results-worker-amd64.json", "index-app.json", "index-worker.json"} {
		require.FileExists(t, filepath.Join(fixture.in.OutputDir, name))
	}

	body, err := os.ReadFile(filepath.Join(fixture.in.OutputDir, appcontainer.ReleaseImagesLedgerName))
	require.NoError(t, err)
	entries, err := imageledger.Parse(body)
	require.NoError(t, err)
	require.Len(t, entries, 2)

	for i, image := range fixture.in.Plan.Images {
		entry := entries[i]
		pushed, err := ociregistry.NewAnonymous().ResolveDigest(t.Context(), image.ImageRepository+":staging-v1.2.3")
		require.NoError(t, err)
		require.Equal(t, image.Name, entry.Role)
		require.Equal(t, imageledger.ImageKindRelease, entry.ImageKind)
		require.Equal(t, pushed, entry.Digest)
		require.Equal(t, image.ImageRepository+"@"+pushed, entry.Ref)
		require.Equal(t, image.ImageRepository+":v1.2.3", entry.FinalTag)
		require.Equal(t, image.ImageRepository+":staging-v1.2.3", entry.CandidateTag)
		require.Empty(t, entry.SBOMSHA256, "ordinary native publication leaves SBOM generation to signing")
		require.NoFileExists(t, filepath.Join(fixture.in.OutputDir, "image-sbom-"+image.Name+".cyclonedx.json"))
	}

	// The job's working directory keeps nothing of the run.
	left, err := os.ReadDir(".")
	require.NoError(t, err)
	require.Empty(t, left)
}

// The gate decides before the first upload: a blocked layout publishes
// nothing and leaves no ledger.
func TestPublishReleaseImagesGatesBeforeAnyUpload(t *testing.T) {
	fixture := newReleaseImagesPublishFixture(t)
	gated := []string{}
	// Only the last image fails its gate; the ones before it pass theirs and
	// must not be pushed either.
	fixture.in.Gate = func(_ context.Context, layout, _ string) error {
		gated = append(gated, filepath.Base(filepath.Dir(layout)))
		if strings.HasPrefix(filepath.Base(filepath.Dir(layout)), "worker-") {
			return errs.ErrValidation
		}

		return nil
	}

	require.ErrorIs(t, fixture.publish(t), errs.ErrValidation)
	require.Equal(t, []string{"app-arch-amd64", "app-arch-arm64", "worker-arch-amd64"}, gated)
	require.Zero(t, fixture.writes.Load())
	require.NoFileExists(t, filepath.Join(fixture.in.OutputDir, appcontainer.ReleaseImagesLedgerName))
}

// The layouts come from jobs that ran consumer code. Anything but the planned
// layouts is refused before a scan or an upload: a ledger planted to be
// appended to, an extra file, a link, a missing layout.
func TestPublishReleaseImagesAdmitsOnlyThePlannedLayouts(t *testing.T) {
	for name, plant := range map[string]func(t *testing.T, layouts string){
		"pre-seeded ledger": func(t *testing.T, layouts string) {
			t.Helper()
			require.NoError(t, os.MkdirAll(filepath.Join(layouts, "app-ledger"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(layouts, "app-ledger", "release-images.json"), []byte(`{"version":1,"images":[]}`), 0o600))
		},
		"file inside a layout directory": func(t *testing.T, layouts string) {
			t.Helper()
			require.NoError(t, os.WriteFile(filepath.Join(layouts, "app-arch-amd64", "release-images.json"), nil, 0o600))
		},
		"linked layout directory": func(t *testing.T, layouts string) {
			t.Helper()
			moved := filepath.Join(t.TempDir(), "elsewhere")
			require.NoError(t, os.Rename(filepath.Join(layouts, "worker-arch-amd64"), moved))
			require.NoError(t, os.Symlink(moved, filepath.Join(layouts, "worker-arch-amd64")))
		},
		"linked metadata file": func(t *testing.T, layouts string) {
			t.Helper()

			file := filepath.Join(layouts, "app-arch-arm64", "app-arm64.json")
			moved := filepath.Join(t.TempDir(), "app-arm64.json")
			require.NoError(t, os.Rename(file, moved))
			require.NoError(t, os.Symlink(moved, file))
		},
		"missing layout": func(t *testing.T, layouts string) {
			t.Helper()
			require.NoError(t, os.RemoveAll(filepath.Join(layouts, "app-arch-arm64")))
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newReleaseImagesPublishFixture(t)
			plant(t, fixture.in.LayoutsDir)

			require.ErrorIs(t, fixture.publish(t), errs.ErrValidation)
			require.Empty(t, *fixture.gated)
			require.Zero(t, fixture.writes.Load())
			require.NoDirExists(t, fixture.in.OutputDir)
		})
	}
}

func TestPublishReleaseImagesPinsOriginalInventoriesAndIndexBOM(t *testing.T) {
	fixture := newReleaseImagesPublishFixture(t)
	// Nano-shaped final declarations; native bases remain refused until their
	// separate ledger and canonical premade-sboms handoff actually exist.
	fixture.in.Plan.Images[1].Flavor = "core"
	fixture.in.PrepublicationSBOMs = true
	fixture.in.SBOMURLPrefix = "https://forge.example/org/app/releases/download/v1.2.3"
	syft := &fakeImageEvidenceSyft{jsonBody: "{\n  \"bomFormat\":\"CycloneDX\",\"specVersion\":\"1.6\",\"version\":1\n}\n"}
	fixture.in.Syft = syft
	originals := map[string][]byte{}

	require.NoError(t, filepath.WalkDir(fixture.in.LayoutsDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !entry.IsDir() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}

			originals[path] = body
		}

		return nil
	}))
	fixture.in.Gate = func(_ context.Context, _, _ string) error {
		require.Zero(t, fixture.writes.Load(), "all inventories and gates precede the first upload")

		return nil
	}
	require.NoError(t, fixture.publish(t))
	require.Len(t, syft.targets, 3)

	for path, want := range originals {
		got, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, want, got, path)
	}
	// Exercise the actual native collector, not a test-only basename mapping.
	require.NoError(t, os.Mkdir("dist", 0o700))
	output, err := collectNativeImageEvidence(t, fixture.in.OutputDir)
	require.NoError(t, err, "%s", output)
	body, err := os.ReadFile("dist/" + appcontainer.ReleaseImagesLedgerName)
	require.NoError(t, err)
	entries, err := imageledger.Parse(body)
	require.NoError(t, err)
	require.Equal(t, "dist/image-sbom-app.cyclonedx.json", entries[0].SBOM)
	require.Equal(t, "dist/image-sbom-core.cyclonedx.json", entries[1].SBOM)

	for _, entry := range entries {
		body, err := os.ReadFile(entry.SBOM)
		require.NoError(t, err)
		require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(body)), entry.SBOMSHA256)
	}

	inventory, err := os.ReadFile(filepath.Join(fixture.in.OutputDir, "sbom-core-amd64.json"))
	require.NoError(t, err)
	index, err := os.ReadFile(filepath.Join(fixture.in.OutputDir, "index-worker.json"))
	require.NoError(t, err)
	wantBOM, err := domainsbom.ComposeIndexBOM(entries[1].Ref, index, []domainsbom.IndexBOMPart{{
		Platform: "linux/amd64", ImageRef: fixture.in.Plan.Images[1].ImageRepository + "@" + fixture.digest["worker-amd64"],
		URL: fixture.in.SBOMURLPrefix + "/sbom-core-amd64.json", SHA256: fmt.Sprintf("%x", sha256.Sum256(inventory)), Document: inventory,
	}})
	require.NoError(t, err)
	gotBOM, err := os.ReadFile(filepath.Join(fixture.in.OutputDir, "image-sbom-core.cyclonedx.json"))
	require.NoError(t, err)
	require.Equal(t, wantBOM, gotBOM)
	// The signer reads the literal canonical ledger paths, verifies pins and
	// attests those exact bytes. A rescan would fail this test.
	for _, entry := range entries {
		signer, signerSyft := &recordingImageSigner{}, &fakeLedgerSyft{}
		want, err := os.ReadFile(entry.SBOM)
		require.NoError(t, err)
		require.NoError(t, appcontainer.SignLedgerImages(t.Context(), signer, signerSyft,
			fakeLedgerResolver{entry.CandidateTag: entry.Digest}, io.Discard, io.Discard, appcontainer.SignLedgerImagesInput{
				Entries: []imageledger.Entry{entry}, ReleaseTag: fixture.in.ReleaseTag,
				PredicatePath: writeBasePredicate(t), Method: domainrelease.SignMethodKMS, KeyRef: "inert-key-reference",
				ExpectedImageRepository: strings.Split(entry.Ref, "@")[0],
			}))
		require.Empty(t, signerSyft.targets)
		require.Len(t, signer.attests, 2)
		require.Equal(t, entry.SBOM, signer.attests[0].input.PredicatePath)
		require.Equal(t, want, signer.attests[0].predicate)
		// A collector/staging mutation at the canonical path must not silently
		// regenerate or attest a different inventory.
		require.NoError(t, os.WriteFile(entry.SBOM, append(append([]byte{}, want...), '\n'), 0o600))

		rejected := &recordingImageSigner{}
		err = appcontainer.SignLedgerImages(t.Context(), rejected, signerSyft,
			fakeLedgerResolver{entry.CandidateTag: entry.Digest}, io.Discard, io.Discard, appcontainer.SignLedgerImagesInput{
				Entries: []imageledger.Entry{entry}, ReleaseTag: fixture.in.ReleaseTag,
				PredicatePath: writeBasePredicate(t), Method: domainrelease.SignMethodKMS, KeyRef: "inert-key-reference",
				ExpectedImageRepository: strings.Split(entry.Ref, "@")[0],
			})
		require.ErrorContains(t, err, "does not match sbom_sha256")
		require.Empty(t, rejected.signs)
		require.Empty(t, rejected.attests)
		require.Empty(t, signerSyft.targets)
		require.NoError(t, os.WriteFile(entry.SBOM, want, 0o600))
	}
}

func collectNativeImageEvidence(t *testing.T, images string) ([]byte, error) {
	t.Helper()

	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)

	script := filepath.Join(filepath.Dir(source), "../../../.gitlab/bin/release.sh")
	command := exec.CommandContext(t.Context(), "bash", "-c", `set -euo pipefail; source "$1"; DIST_DIR=dist rc_collect_release_images "$2"`, "collector", script, images)

	return command.CombinedOutput()
}

// The only inert part is the file signer. Production publisher, collector,
// shell evidence enumeration, manifest admission/finalization and exact
// publication selection all participate in this journey.
func TestNativePublicImageEvidenceManifestJourney(t *testing.T) {
	for _, defect := range []string{"", "missing index", "changed index", "missing platform", "changed platform", "extra publisher SBOM", "missing pin", "unregistered platform"} {
		t.Run(defect, func(t *testing.T) {
			fixture := newReleaseImagesPublishFixture(t)
			fixture.in.Plan.Images[0].Archs = "amd64"
			require.NoError(t, os.RemoveAll(filepath.Join(fixture.in.LayoutsDir, "app-arch-arm64")))
			fixture.in.PrepublicationSBOMs = true
			fixture.in.SBOMURLPrefix = "https://gitlab.example/org/source/-/releases/v1.2.3/downloads"
			fixture.in.Syft = &fakeImageEvidenceSyft{}
			require.NoError(t, fixture.publish(t))
			require.NoError(t, os.Mkdir("dist", 0o700))
			require.NoError(t, os.WriteFile("dist/app.tar.gz", []byte("archive"), 0o600))

			before, err := apprelease.DeclareReleaseFiles(apprelease.DeclareReleaseFilesInput{Assets: []string{"dist/app.tar.gz"}})
			require.NoError(t, err)
			// A consumer's pre-existing inventory must never become public.
			require.NoError(t, os.WriteFile("dist/sbom-consumer-amd64.json", []byte("{}"), 0o600))

			originalManifest, err := os.ReadFile("dist/release-files.json")
			require.NoError(t, err)
			output, err := collectNativeImageEvidence(t, fixture.in.OutputDir)
			require.NoError(t, err, "%s", output)

			originals := map[string][]byte{}
			for _, name := range []string{"image-sbom-app.cyclonedx.json", "image-sbom-worker.cyclonedx.json", "sbom-app-amd64.json", "sbom-worker-amd64.json"} {
				originals["dist/"+name], err = os.ReadFile("dist/" + name)
				require.NoError(t, err)
			}

			switch defect {
			case "missing index":
				require.NoError(t, os.Remove("dist/image-sbom-app.cyclonedx.json"))
			case "changed index":
				require.NoError(t, os.WriteFile("dist/image-sbom-app.cyclonedx.json", []byte("{}"), 0o600))
			case "missing platform":
				require.NoError(t, os.Remove("dist/sbom-app-amd64.json"))
			case "changed platform":
				require.NoError(t, os.WriteFile("dist/sbom-app-amd64.json", []byte("{}"), 0o600))
			case "extra publisher SBOM":
				require.NoError(t, os.WriteFile(filepath.Join(fixture.in.OutputDir, "sbom-unplanned-amd64.json"), []byte("{}"), 0o600))
				require.NoError(t, os.WriteFile("dist/sbom-unplanned-amd64.json", []byte("{}"), 0o600))
			case "missing pin":
				body, err := os.ReadFile("dist/release-images.json")
				require.NoError(t, err)
				entries, err := imageledger.Parse(body)
				require.NoError(t, err)

				entries[0].SBOMSHA256 = ""
				body, err = imageledger.Marshal(entries)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile("dist/release-images.json", body, 0o600))
			case "unregistered platform":
				require.NoError(t, os.Remove(filepath.Join(fixture.in.OutputDir, "sbom-app-amd64.json")))
			}

			args := nativeImageManifestArguments(t, fixture.in.OutputDir)
			command := rootcli.New(rootcli.BuildInfo{Version: "dev"})

			err = command.Run(t.Context(), append([]string{"forge-ci", "--provider", "gitlab"}, args...))
			if defect != "" {
				require.Error(t, err)

				got, readErr := os.ReadFile("dist/release-files.json")
				require.NoError(t, readErr)
				require.Equal(t, originalManifest, got)
				require.NoFileExists(t, "dist/image-sbom-app.cyclonedx.json.bundle")

				return
			}

			require.NoError(t, err)
			body, err := os.ReadFile("dist/release-files.json")
			require.NoError(t, err)

			var after apprelease.FileManifest
			require.NoError(t, json.Unmarshal(body, &after))
			require.Equal(t, before.Checksums, after.Checksums)
			require.Equal(t, before.SBOMs, after.SBOMs)
			require.Len(t, after.Evidence, 4)
			require.Len(t, after.Assets, len(before.Assets)+4)

			signer := &nativeEvidenceBundleSigner{}
			require.NoError(t, apprelease.SignArtifacts(t.Context(), signer, io.Discard, apprelease.SignInput{
				SkipChecksumsFile: true, SkipReleaseArtifactsDir: true, ManifestSections: []string{"evidence"},
			}))
			require.Len(t, signer.files, 4)
			// The independent provenance stage contributes its usual two files;
			// it is not part of the eight image-evidence additions.
			require.NoError(t, os.WriteFile("dist/slsa-provenance.intoto.json", []byte("{}"), 0o600))
			require.NoError(t, os.WriteFile("dist/slsa-provenance.intoto.json.bundle", []byte("inert provenance bundle"), 0o600))

			final, err := apprelease.WriteReleaseFileManifest(apprelease.WriteReleaseFileManifestInput{})
			require.NoError(t, err)
			require.Len(t, final.Provenance, 1)
			require.Len(t, final.Assets, len(before.Assets)+8+2)

			public, err := apprelease.PublishableReleaseFiles(apprelease.FilesInput{})
			require.NoError(t, err)
			require.NotContains(t, public, "dist/sbom-consumer-amd64.json")
			require.NotContains(t, public, "dist/index-app.json")
			require.NotContains(t, public, "dist/trivy-results-app-amd64.json")

			for path, want := range originals {
				got, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, want, got)
				require.Contains(t, public, path)
				require.Contains(t, public, path+".bundle")
			}
		})
	}
}

type nativeEvidenceBundleSigner struct{ files []string }

func (*nativeEvidenceBundleSigner) Extensions() []string { return []string{".bundle"} }
func (s *nativeEvidenceBundleSigner) SignFile(_ context.Context, file string) error {
	s.files = append(s.files, file)

	return os.WriteFile(file+".bundle", []byte("inert bundle"), 0o600)
}

func nativeImageManifestArguments(t *testing.T, images string) []string {
	t.Helper()

	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)

	script := filepath.Join(filepath.Dir(source), "../../../.gitlab/bin/release.sh")
	command := exec.CommandContext(t.Context(), "bash", "-c", `set -euo pipefail
source "$1"
rc_engine() { printf '%s\0' "$@"; }
DIST_DIR=dist RELEASE_FILES_MANIFEST=dist/release-files.json rc_register_release_image_evidence "$2"`, "manifest", script, images)
	output, err := command.Output()
	require.NoError(t, err)

	args := strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00")
	require.Equal(t, []string{"release", "files", "manifest", "--unsigned", "--manifest", "dist/release-files.json", "--dist-dir", "dist", "--release-images-ledger", "dist/release-images.json"}, args[:10])

	for i := 10; i < len(args); i += 2 {
		require.Equal(t, "--evidence-file", args[i])
	}

	return args
}

func TestNativeImageCollectorAdmitsCompleteFlatDistMappingBeforeCopy(t *testing.T) {
	for _, defect := range []string{"premade root", "linked evidence", "existing destination"} {
		t.Run(defect, func(t *testing.T) {
			t.Chdir(t.TempDir())
			require.NoError(t, os.Mkdir("images", 0o700))
			require.NoError(t, os.Mkdir("dist", 0o700))
			require.NoError(t, os.WriteFile("images/a.json", []byte("{}\n"), 0o600))

			switch defect {
			case "premade root":
				require.NoError(t, os.Mkdir("images/premade-sboms", 0o700))
			case "linked evidence":
				require.NoError(t, os.Symlink("a.json", "images/z.json"))
			case "existing destination":
				require.NoError(t, os.WriteFile("images/z.json", []byte("{}\n"), 0o600))
				require.NoError(t, os.WriteFile("dist/z.json", []byte("reserved\n"), 0o600))
			}

			_, err := collectNativeImageEvidence(t, "images")
			require.Error(t, err)
			require.NoFileExists(t, "dist/a.json", "a later rejected entry must stop the complete fan-in")
		})
	}
}

func TestPublishReleaseImagesEvidenceFailuresPrecedeAllUploads(t *testing.T) {
	for _, defect := range []string{"base without evidence", "base with evidence", "invalid public URL", "SBOM collision", "inventory missing", "wrong destination", "wrong architecture"} {
		t.Run(defect, func(t *testing.T) {
			fixture := newReleaseImagesPublishFixture(t)
			fixture.in.PrepublicationSBOMs = true
			fixture.in.SBOMURLPrefix = "https://forge.example/org/app/releases/download/v1.2.3"
			fixture.in.Syft = &fakeImageEvidenceSyft{}

			switch defect {
			case "base without evidence":
				fixture.in.PrepublicationSBOMs, fixture.in.SBOMURLPrefix = false, ""

				fallthrough
			case "base with evidence":
				fixture.in.Plan.Images[1].ImageKind, fixture.in.Plan.Images[1].Flavor = "base", "core"
			case "invalid public URL":
				fixture.in.SBOMURLPrefix = "http://forge.example/download"
			case "SBOM collision":
				fixture.in.Plan.Images[0].Flavor, fixture.in.Plan.Images[1].Flavor = "same", "same"
			case "inventory missing":
				fixture.in.Syft = &fakeImageEvidenceSyft{noOutput: true}
			case "wrong destination":
				fixture.in.Plan.Images[1].ImageRepository = "registry.example/other/image"
			case "wrong architecture":
				fixture.in.Plan.Images[1].Archs = "riscv64"
			}

			err := fixture.publish(t)
			require.Error(t, err)

			if strings.HasPrefix(defect, "base ") {
				require.ErrorIs(t, err, errs.ErrInvalidConfig)
				require.ErrorContains(t, err, "canonical premade-sboms handoff")
				require.NoDirExists(t, fixture.in.OutputDir)
				require.Empty(t, *fixture.gated)
			}

			require.Zero(t, fixture.writes.Load())
			require.NoFileExists(t, filepath.Join(fixture.in.OutputDir, appcontainer.ReleaseImagesLedgerName))
		})
	}
}
