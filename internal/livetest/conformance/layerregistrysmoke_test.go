// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"bytes"
	_ "embed"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/livetest"
)

//go:embed testdata/layer/registry-signer-smoke.yml
var layerRegistrySmokeFixture string

// The consumer whose image is released: one file in an image built FROM
// scratch, so the build needs no base image and the registry holds a few
// hundred bytes.
const (
	smokeContainerfile = "FROM scratch\nCOPY smoke.txt /smoke.txt\n"
	smokeText          = "forge-ci registry and signer smoke\n"
	smokeChangelog     = `# Changelog

All notable changes to this acceptance fixture will be documented in this file.

## [Unreleased]

### Added

- Add the registry and signer smoke canary.
`
)

// LAYER-5: an image built in a job is pushed, recorded, signed and verifiable.
//
// What the minimal release leaves out, in its smallest form: one image, one
// platform. The consumer's build job sets Buildah up with the layer's action,
// builds a FROM scratch image, pushes it to the forge's registry and records
// it in the image ledger; sign-and-publish-release.yml signs the image named
// by that ledger and promotes it. The same image goes a second time to a base
// repository, where sign-container-images.yml signs it and attests its SBOM
// and lineage, verify-base-container-images.yml verifies all three, and a
// last job fails unless the base signed in this run is among the verified.
//
// The host then checks, with no product in the path: the release commit and
// tag are signed by the bot; the registry serves the release tag; the ledger
// the signer was handed names that digest; cosign verifies the image and the
// base image by digest against the release's public key, with the
// transparency log waived as the layer's default requires; and the published
// release's bundles verify against the same key.
//
// The request is signed here and nowhere else in the tier: the tag is made
// with an SSH key and pushed with git, the consumer's allowed-signers file
// names that key, and the fixture asks prepare-release.yml to verify it.
//
// A job can build an image only where the runner lets it, and no contract
// says whether it does, so the operator does: with RC_LIVE_IMAGE_BUILDS=0 the
// scenario skips. Without it a failed build is a failure. The fixture's first
// job is the layer's setup-buildah alone, so that such a failure can say the
// runner refused the build.
func TestForgejoLayer_RegistrySignerSmoke(t *testing.T) {
	for _, visibility := range livetest.ConsumerVisibilities(t) {
		t.Run(visibility.Name, func(t *testing.T) { runLayerRegistrySignerSmoke(t, visibility.Private) })
	}
}

func runLayerRegistrySignerSmoke(t *testing.T, private bool) {
	t.Helper()
	const (
		workflow = "layer-registry-smoke"
		name     = "layer-registry-smoke"
	)

	if !livetest.ImageBuilds(t) {
		t.Skip("no job on this runner may build an image")
	}

	signer, ok := livetest.SignerImage(t)
	if !ok {
		t.Skip("no signer image, so the signer jobs cannot run")
	}

	gitURL, fingerprint, ok := livetest.SSHRoute(t, livetest.Accept(t, provider.ForgeForgejo), livetest.ScratchRepo(name))
	if !ok {
		t.Skip("no SSH route, so the release commit cannot be pushed")
	}

	scenario := newLayerScenario(t, name, private, "sha1")
	target, repo := scenario.target, scenario.repo

	registry, err := livetest.RegistryHost(target)
	if err != nil {
		t.Fatal(err)
	}

	// Both images are the owner's packages, and only the first is named as
	// the repository is.
	livetest.SweepPackagesAfter(t, target, repo)

	image := registry + "/" + strings.ToLower(livetest.RepoSlug(target, repo))
	baseImage := image + "-base"

	keys := newReleaseKeys(t)
	requester := livetest.NewRequestSigner(t, t.TempDir())
	livetest.AddDeployKey(t, target, repo, "livetest release bot", keys.sshPublic)
	livetest.SetRepoVariable(t, target, repo, "COSIGN_PUBLIC_KEY", keys.cosignPublic)
	// The fixture names the serving repository in no call, so this is how
	// each called workflow finds the layer's files.
	livetest.SetRepoVariable(t, target, repo, "FORGE_CI_REPOSITORY", scenario.layer.Repository)

	for secret, value := range map[string]string{
		"RELEASE_SSH_SIGNING_KEY":    keys.sshPrivate,
		"RELEASE_COSIGN_PRIVATE_KEY": keys.cosignPrivate,
		"RELEASE_COSIGN_PASSPHRASE":  keys.passphrase,
		"RELEASE_TOKEN":              target.Token,
		"RELEASE_REGISTRY_TOKEN":     target.Token,
	} {
		livetest.SetRepoSecret(t, target, repo, secret, value)
	}

	request := livetest.CommitFiles(t, target, repo, livetest.FileCommit{
		Branch: layerMain, Message: "feat: add the registry and signer smoke canary",
		Files: map[string]string{
			"Containerfile":                        smokeContainerfile,
			"smoke.txt":                            smokeText,
			"base-flavors.list":                    "smoke\n",
			".forgejo/release-allowed-signers":     requester.AllowedSigners,
			".chglog/config-keepachangelog.yml":    chglogKeepConfig,
			".chglog/CHANGELOG-keepachangelog.tpl": chglogKeepTemplate,
			".chglog/config-minimal.yml":           chglogMinimalConfig,
			".chglog/CHANGELOG-minimal.tpl":        chglogMinimalTemplate,
			"CHANGELOG.md":                         smokeChangelog,
			".forgejo/workflows/" + workflow + ".yml": scenario.render(t, layerRegistrySmokeFixture,
				"RENDER_SIGNER_IMAGE", signer,
				"RENDER_COSIGN_PUBLIC_KEY_SHA256", keys.cosignPublicSHA256,
				"RENDER_RELEASE_GIT_URL", gitURL,
				"RENDER_RELEASE_GIT_HOST_KEY_FINGERPRINT", fingerprint,
				"RENDER_BASE_REPOSITORY", baseImage,
			),
		},
	})

	requester.PushTag(t, target, repo, releaseRequestTag, layerMain, "Request release v0.0.1")

	run := livetest.WaitForLayerRun(t, target, repo, workflow, "push")
	if run.Status != jobSuccess {
		t.Logf("where %q is the job that failed, the runner refused the build and the release is not at fault: run the scenario where a job may "+
			"build an image, or say that this runner allows none with RC_LIVE_IMAGE_BUILDS=0", smokeStorageJob)
	}

	scenario.requireStatus(t, run, jobSuccess)

	requireReleaseGit(t, target, repo, keys, request, releaseRequestTag, releaseFinalTag)
	requireSignedRelease(t, target, repo, keys, releaseFinalTag)

	digest, served := livetest.ImageDigest(t, target, repo, releaseFinalTag)
	if !served {
		t.Fatalf("the registry serves no %s:%s after a successful release", image, releaseFinalTag)
	}

	// The ledger is the one thing the signer takes images from, so it must
	// name the image the registry serves.
	var ledger []byte

	for _, artifact := range livetest.RunArtifacts(t, target, repo, run.ID) {
		if artifact.Name != "signed-dist" {
			continue
		}

		tree, err := livetest.ZIPTree(livetest.ArtifactZIP(t, target, repo, artifact.ID), maxReleaseFile)
		if err != nil {
			t.Fatalf("artifact %s is not a plain tree: %v", artifact.Name, err)
		}

		ledger = tree["release-images.json"]
	}

	if !bytes.Contains(ledger, []byte(digest)) || !bytes.Contains(ledger, []byte(image)) {
		t.Errorf("the image ledger the signer handed on does not name %s@%s:\n%s", image, digest, ledger)
	}

	authDir := livetest.RegistryAuthConfigDir(t, target, keys.dir)
	verifyCosignSignature(t, target, image+"@"+digest, keys.cosignPublicPath, authDir)

	// The base's tag is derived inside the run; it is the one tag there that
	// is neither a digest nor a signature.
	bases := 0

	for tag, baseDigest := range livetest.RegistrySnapshot(t, target, repo+"-base") {
		if strings.HasSuffix(tag, "-smoke") {
			bases++

			verifyCosignSignature(t, target, baseImage+"@"+baseDigest, keys.cosignPublicPath, authDir)
		}
	}

	if bases != 1 {
		t.Errorf("the base repository holds %d base tags, want the one this run pushed", bases)
	}
}

// smokeStorageJob is the fixture's job that is the layer's setup-buildah
// alone.
const smokeStorageJob = "Check that a job can build an image"
