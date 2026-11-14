// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"

	"github.com/diggsweden/forge-ci/internal/adapters/cosign"
	appconfig "github.com/diggsweden/forge-ci/internal/app/config"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/release"
	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

// The prepared commit was verified by requireReleaseGit. Re-plan its immutable
// declaration through the engine owner, never the host checkout or ledger.
// The plan is returned for what else is read back from it.
func requireLocalReleaseImageOutputs(t *testing.T, target livetest.Target, repo string, runID int64, prepared string, keys releaseKeys, settings livetest.TrialSettings) pipeline.ReleaseImagesPlan {
	t.Helper()

	mirror := livetest.MirrorRepo(t, target, repo)

	mode, err := mirror.Git(t, "ls-tree", prepared, "--", settings.ReleaseImagesConfig)
	if err != nil || !strings.HasPrefix(mode, "100644 blob ") {
		t.Fatalf("release image declaration must be a regular blob in the verified prepared source: %v", err)
	}

	declaration, err := mirror.Git(t, "show", prepared+":"+settings.ReleaseImagesConfig)
	if err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(t.TempDir(), "artifacts.yml")
	if err := os.WriteFile(file, []byte(declaration), 0o600); err != nil {
		t.Fatal(err)
	}

	host, err := livetest.RegistryHost(target)
	if err != nil {
		t.Fatal(err)
	}

	repository := strings.ToLower(host + "/" + livetest.RepoSlug(target, repo))

	platforms, err := livetest.NativeReleasePlatforms(settings.Variables, livetest.RunnerArch(t, target))
	if err != nil {
		t.Fatal(err)
	}

	plan, err := appconfig.ResolveReleaseImagesPlan(appconfig.EmitReleaseImagesPlanInput{
		Config: appconfig.EmitConfigPlanInput{Path: file},
		Images: pipeline.ReleaseImagesInput{ImageRepository: repository, Repository: livetest.RepoSlug(target, repo), ReleaseTag: "v" + settings.Version, ReleasePlatforms: platforms["RELEASE_PLATFORMS"]},
	}, output.Annotator{})
	if err != nil {
		t.Fatal("release image expected plan: ", err)
	}

	// Select the engine's standard collected output from this exact run. A
	// custom artifact namespace or resolved/base plan needs its own authority
	// seam; missing receipts must not be interpreted as an image-free release.
	var ledger []byte

	artifacts := livetest.RunArtifacts(t, target, repo, runID)

	for _, artifact := range artifacts {
		if artifact.Name != "release-images" {
			continue
		}

		if ledger != nil || artifact.Expired {
			t.Fatal("release image receipt is duplicate or expired")
		}

		tree, err := livetest.ZIPTree(livetest.ArtifactZIP(t, target, repo, artifact.ID), maxReleaseFile)
		if err != nil {
			t.Fatal(err)
		}

		ledger = tree["release-images.json"]
		if len(ledger) == 0 {
			t.Fatal("collected release image artifact has no ledger")
		}
	}

	if len(ledger) == 0 {
		t.Fatal("missing planned release-images run artifact")
	}

	verifier := &cosign.Adapter{
		Bin: livetest.HostTool(t, "cosign"), AuthFile: livetest.RegistryAuthFile(t, target, t.TempDir()),
		Env: livetest.ToolEnvironment(t, target, nil), Transparency: release.TransparencyNone,
	}

	ctx, cancel := context.WithTimeout(livetest.Context(t), 2*time.Minute)
	defer cancel()

	resolve := func(ctx context.Context, ref string) (string, error) {
		tag, err := name.NewTag(ref)
		if err != nil || tag.Context().Name() != repository {
			return "", fmt.Errorf("release image read is outside the expected repository: %w", errs.ErrValidation)
		}

		digest, found := livetest.ImageDigestContext(ctx, t, target, repo, tag.TagStr())
		if !found {
			return "", fmt.Errorf("planned final image is missing: %w", errs.ErrValidation)
		}

		return digest, nil
	}

	if err := livetest.VerifyReleaseImageOutputs(ctx, livetest.ReleaseImageOutputs{
		Plan: plan, Ledger: ledger, Repository: repository, Tag: "v" + settings.Version, Commit: prepared,
		Source: strings.TrimRight(target.BaseURL(), "/") + "/" + livetest.RepoSlug(target, repo), Workflow: ".forgejo/workflows/" + settings.Workflow, PublicKey: keys.cosignPublicPath,
	}, resolve, verifier, os.Stdout); err != nil {
		t.Fatal("local release image readback: ", err)
	}

	requirePromotionJournal(t, target, repo, runID, artifacts, ledger)

	return plan
}

// promotionJournalArtifact is the name `Publish: images` gives the artifact
// that keeps its journal, before the run attempt; promotionJournalFile is
// the one file in it.
const (
	promotionJournalArtifact = "release-promote-"
	promotionJournalFile     = "image-promotions.jsonl"
)

// requirePromotionJournal holds a release run that promoted images to the
// journal it kept: one artifact of the run, which holds the journal alone,
// and a journal that names the images of the verified receipt
// (docs/failed-release-recovery.md, Container promotion partially completed).
func requirePromotionJournal(t *testing.T, target livetest.Target, repo string, runID int64, artifacts []livetest.RunArtifact, ledger []byte) {
	t.Helper()

	var kept []livetest.RunArtifact

	for _, artifact := range artifacts {
		if strings.HasPrefix(artifact.Name, promotionJournalArtifact) {
			kept = append(kept, artifact)
		}
	}

	// A re-run promote attempt would keep a second journal; no row re-runs
	// promote (Forgejo has no re-run API; recovery uses a new run).
	if len(kept) != 1 || kept[0].Expired {
		t.Fatalf("run %d promoted images and kept %d unexpired promotion journals (%+v), want one artifact %s<attempt>", runID, len(kept), kept, promotionJournalArtifact)
	}

	tree, err := livetest.ZIPTree(livetest.ArtifactZIP(t, target, repo, kept[0].ID), maxReleaseFile)
	if err != nil {
		t.Fatal(err)
	}

	if len(tree) != 1 || len(tree[promotionJournalFile]) == 0 {
		t.Fatalf("artifact %s holds %d files, want %s alone", kept[0].Name, len(tree), promotionJournalFile)
	}

	for _, finding := range livetest.PromotionJournalFindings(tree[promotionJournalFile], ledger) {
		t.Error(finding)
	}
}

// maxHandOverArtifact bounds the run artifacts searched for an image build's
// context: below what ArtifactZIP reads at all.
const maxHandOverArtifact = 24 << 20

// requireImagesCarryReleasedFiles holds every planned image that copies a
// file of the release's hand-off to that file. Which files, and where to, is
// read from the planned Containerfile at the verified prepared commit; the
// release serves the file, an artifact of the run holds it at the path the
// Containerfile copies, and the published linux/amd64 image holds the same
// bytes. An image whose Containerfile builds for itself copies none, and is
// not compared.
func requireImagesCarryReleasedFiles(t *testing.T, target livetest.Target, repo string, runID int64, prepared, tag string, plan pipeline.ReleaseImagesPlan) {
	t.Helper()

	mirror := livetest.MirrorRepo(t, target, repo)

	copies, problems := plannedReleaseFileCopies(plan, "amd64", func(file string) (string, error) {
		return mirror.Git(t, "show", prepared+":"+file)
	})
	for _, problem := range problems {
		t.Error(problem)
	}

	if len(copies) == 0 {
		t.Logf("no planned linux/amd64 image of %s copies a file of the release; none is compared", tag)

		return
	}

	released, handedOver := map[string]string{}, map[string][]string{}

	for _, files := range copies {
		for _, file := range files {
			released[file.Asset], handedOver[file.Source] = "", nil
		}
	}

	for _, release := range livetest.Releases(t, target, repo) {
		if release.TagName != tag {
			continue
		}

		for _, asset := range release.Assets {
			if _, copied := released[asset.Name]; copied {
				released[asset.Name] = sha256Hex(livetest.Download(t, target, asset.URL, maxReleaseFile))
			}
		}
	}

	// Which artifact is the image build's context is the consumer's to name,
	// so each is searched. One too large to read, or not a plain tree, holds
	// no hand-off as far as this reader knows: a copy only it held is a
	// finding below, with these lines beside it.
	for _, artifact := range livetest.RunArtifacts(t, target, repo, runID) {
		if artifact.Expired || artifact.SizeInBytes > maxHandOverArtifact {
			t.Logf("run artifact %s (%d bytes, expired %t) is not searched for the image build's files", artifact.Name, artifact.SizeInBytes, artifact.Expired)

			continue
		}

		tree, err := livetest.ZIPTree(livetest.ArtifactZIP(t, target, repo, artifact.ID), maxReleaseFile)
		if err != nil {
			t.Logf("run artifact %s is not searched for the image build's files: %v", artifact.Name, err)

			continue
		}

		for source := range handedOver {
			if body, held := tree[source]; held {
				handedOver[source] = append(handedOver[source], sha256Hex(body))
			}
		}
	}

	compared := 0

	for _, image := range plan.Images {
		files := copies[image.Name]
		if len(files) == 0 {
			continue
		}

		digest, found := livetest.ImageDigest(t, target, repo, image.FinalTagName)
		if !found {
			t.Errorf("planned image %s is not served under %s", image.Name, image.FinalTagName)

			continue
		}

		paths := make([]string, 0, len(files))
		for _, file := range files {
			paths = append(paths, file.Path)
		}

		for _, finding := range imageReleaseFileFindings(image.Name, files, released, handedOver, livetest.ImageFileSHA256(t, target, repo, digest, paths)) {
			t.Error(finding)
		}

		compared += len(files)
	}

	t.Logf("release %s: %d image files compared with the released files they were built from", tag, compared)
}
