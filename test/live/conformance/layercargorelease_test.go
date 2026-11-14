// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"bytes"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

// cargoReleaseImageEnv names the Rust job image the release runs its
// version job and its build in, pinned by digest: the image a Cargo
// consumer's CI_JOB_IMAGE names, which a prepare publishes as
// JOB_IMAGE_RUST. It needs bash, git, curl, sha256sum, tar, gzip and a
// Cargo of edition 2024 that builds for the runner's own platform.
const cargoReleaseImageEnv = "FORGE_CI_LIVE_CARGO_RELEASE_IMAGE"

// A Cargo binary is released artifact-first: built by the consumer's own job,
// handed over by digest, signed and published by the layer.
//
// The release is LAYER-4's, in a repository of its own and held to every
// assertion of TestForgejoLayer_MinimalBinaryRelease, with the differences a
// Cargo consumer has. The canary is a crate without a dependency, with a
// committed lockfile, declared in .forge-ci/artifacts.yml as one Cargo
// project. The plan therefore bumps it: the run has a "Prepare: version rust"
// job in place of the bump's empty row, in the Rust image the run names, and
// that job hands its patch on. The consumer's job builds with the layer's
// build-cargo in the same image, and its check of the prepared commit no
// longer asks for a changelog-only one.
//
// The host then holds the release commit to the bump. It changes exactly
// CHANGELOG.md, Cargo.toml and Cargo.lock; the changelog gains the version;
// the manifest and the lockfile both name the crate at the released version;
// and the published archive's binary carries that version, which the crate
// compiles in from its manifest, so the file a reader downloads was built
// from the bumped release commit and not from the request.
//
// One clean case a visibility. The Build SBOM of build-cargo is not part of
// it: the fixture declares an SBOM of its own, as the Go release does, and the
// build installs no cargo-cyclonedx. No image is built either.
func TestForgejoLayer_CargoBinaryRelease(t *testing.T) {
	livetest.RegisterLiveScenario(t)

	image := os.Getenv(cargoReleaseImageEnv)
	if image == "" {
		livetest.NotCoveredf(t, "set %s to a digest-pinned Rust job image, the one a Cargo consumer's CI_JOB_IMAGE names; the version job and the build have no Cargo without one", cargoReleaseImageEnv)
	}

	if !container.ValidDigestReference(image) {
		t.Fatalf("%s must be digest-pinned", cargoReleaseImageEnv)
	}

	for _, visibility := range livetest.ConsumerVisibilities(t) {
		t.Run(visibility.Name, func(t *testing.T) {
			format := pairedObjectFormat(visibility)
			t.Run(format, func(t *testing.T) { runLayerCargoRelease(t, visibility.Private, format, image) })
		})
	}
}

func runLayerCargoRelease(t *testing.T, private bool, objectFormat, image string) {
	t.Helper()

	const name = "layer-cargo-release"

	released := runLayerRelease(t, name, private, objectFormat, "", func(files map[string]string) {
		if err := shapeCargoCanary(files, image); err != nil {
			t.Fatal(err)
		}
	})

	// A release that fell short has no bump to read.
	if t.Failed() {
		return
	}

	target, repo := livetest.Accept(t, provider.ForgeForgejo), livetest.ScratchRepo(name)
	version := strings.TrimPrefix(releaseFinalTag, "v")

	mirror := livetest.MirrorRepo(t, target, repo)
	git := func(args ...string) string {
		out, err := mirror.Git(t, args...)
		if err != nil {
			t.Fatalf("Cargo release commit: %v", err)
		}

		return out
	}

	changed := strings.Fields(git("diff-tree", "--no-commit-id", "--name-only", "-r", released+"^", released))
	slices.Sort(changed)

	if want := []string{releaseChangelog, "Cargo.lock", "Cargo.toml"}; !slices.Equal(changed, want) {
		t.Errorf("the release commit changes %v, want exactly %v", changed, want)
	}

	for _, finding := range cargoVersionFindings(git("show", released+":Cargo.toml"), git("show", released+":Cargo.lock"), cargoCanaryCrate, version) {
		t.Error(finding)
	}

	release := livetest.ReadRelease(t, target, repo, releaseFinalTag)

	asset := slices.IndexFunc(release.Assets, func(asset livetest.ReleaseAsset) bool { return asset.Name == releaseArchive })
	if asset == -1 {
		t.Fatalf("the release offers no %s", releaseArchive)
	}

	binary, err := archivedFile(livetest.Download(t, target, release.Assets[asset].URL, maxReleaseFile),
		strings.TrimSuffix(releaseArchive, ".tar.gz")+"/"+cargoCanaryCrate, maxReleaseFile)
	if err != nil {
		t.Fatalf("the release archive: %v", err)
	}

	if line := cargoCanaryCrate + " " + version + "\n"; !bytes.Contains(binary, []byte(line)) {
		t.Errorf("the released binary does not hold %q, so it was not built from the manifest of the release commit", line)
	}

	t.Logf("Cargo crate %s released at %s from the signed release commit %s", cargoCanaryCrate, version, released)
}
