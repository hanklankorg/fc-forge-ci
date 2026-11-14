// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

// developmentWorkflow is the workflow file a consumer's development run is
// dispatched from.
const developmentWorkflow = livetest.ServiceDevelopmentWorkflow

// requireServiceDevelopment reads what a dispatched service development run
// left: the frozen snapshot plan bound to the selected source head and
// branch, the receipts of every planned amd64 image as the snapshot
// workflow's collect job exports them, the registry's digest and tag of each
// with no stable alias, each image's retained evidence with an SBOM exactly
// where the declaration's policy plans one
// (livetest.SnapshotEvidenceFindings), and, for the lifecycle event alone,
// the readiness evidence binding its published digest to that source. It
// returns the receipts, in plan order, and the
// count of the producers the plan built, none for a container-first
// service.
func requireServiceDevelopment(t *testing.T, target livetest.Target, repo string, run livetest.WorkflowRun, consumer livetest.Consumer, config livetest.ServiceDevelopmentConfig, head, image string) ([]snapshotimage.Receipt, int) {
	t.Helper()

	if run.Status != jobSuccess || (run.Event != "" && run.Event != "workflow_dispatch") || run.TriggerEvent != "workflow_dispatch" || run.WorkflowID != developmentWorkflow {
		t.Fatalf("service development run %d: %s; a failure of the consumer's own Containerfile or build is a CI integration failure, not permission to alter source", run.ID, run.Status)
	}

	artifacts := map[string]map[string][]byte{}
	// The publish job's retained evidence of each image, by image name.
	evidenced := map[string]map[string][]byte{}

	for _, artifact := range livetest.RunArtifacts(t, target, repo, run.ID) {
		imageName, imageEvidence := strings.CutPrefix(artifact.Name, snapshotArtifact+"-evidence-")
		if !imageEvidence && !slices.Contains([]string{"service-evidence-" + head, snapshotArtifact, snapshotArtifact + "-plan"}, artifact.Name) {
			continue
		}

		if artifact.Expired || artifact.SizeInBytes <= 0 || artifact.SizeInBytes > 256<<20 {
			t.Fatalf("invalid service artifact: %+v", artifact)
		}

		address := fmt.Sprintf("%s/api/v1/repos/%s/actions/artifacts/%d/zip", target.BaseURL(), livetest.RepoSlug(target, repo), artifact.ID)

		tree, err := livetest.ZIPTree(livetest.Download(t, target, address, 256<<20), 256<<20)
		if err != nil {
			t.Fatal(err)
		}

		if imageEvidence {
			evidenced[imageName] = tree

			continue
		}

		artifacts[artifact.Name] = tree
	}

	receipts, expected := requireSnapshotImages(livetest.Context(t), t, target, repo, consumer, config.Branch, head, image,
		artifacts[snapshotArtifact+"-plan"]["snapshot-plan.json"], artifacts[snapshotArtifact]["container-images.json"])

	// The declaration's SBOM policy, as the expected plan carries it, decides
	// which image retains an SBOM and which must not.
	if findings := livetest.SnapshotEvidenceFindings(expected, receipts, evidenced); len(findings) != 0 {
		t.Fatalf("the snapshot's retained evidence does not keep the declaration's SBOM policy:\n%s", strings.Join(findings, "\n"))
	}

	// What was held, for the run's log: a forbidden SBOM is judged absent
	// above, not merely unlisted here.
	for _, planned := range expected.Images {
		t.Logf("snapshot evidence of image %s: the declaration plans its SBOM %t, and the run retained %v", planned.Name, planned.SBOM, slices.Sorted(maps.Keys(evidenced[planned.Name])))
	}

	evidence, probed := artifacts["service-evidence-"+head]
	if probed == config.Images {
		t.Fatalf("readiness evidence present %t, want %t for snapshot-only event %t", probed, !config.Images, config.Images)
	}

	if probed && (string(evidence["image-ref.txt"]) != receipts[0].Ref+"\n" || string(evidence["source-sha.txt"]) != head+"\n" ||
		string(evidence["readiness-url.txt"]) != config.ReadinessURL+"\n" || len(evidence["readiness-body.txt"]) == 0) {
		t.Fatalf("readiness evidence does not bind selected source and published image: %+v", receipts[0])
	}

	return receipts, len(expected.Artifacts)
}

// requireSnapshotFiles reads what a dispatched snapshot of file outputs
// left, a successful dispatch of workflow: the frozen plan bound to the
// selected source head and branch, which is the plan of declaration, the
// body the workflow reads, and
// plans file outputs and no image (livetest.RequireSnapshotFilesPlan), and
// one unexpired run artifact per planned output, named
// snapshot-images-files-<artifact>-<snapshot version>, whose tree is the
// bundle the oracle judges (livetest.SnapshotFilesFindings). A files artifact
// of the namespace under any other name is judged as an unplanned bundle. It
// returns the names of the artifacts it read, in plan order.
func requireSnapshotFiles(t *testing.T, target livetest.Target, repo string, run livetest.WorkflowRun, workflow, declaration string, config livetest.ServiceDevelopmentConfig, head string) []string {
	t.Helper()

	if run.Status != jobSuccess || (run.Event != "" && run.Event != "workflow_dispatch") || run.TriggerEvent != "workflow_dispatch" || run.WorkflowID != workflow {
		t.Fatalf("snapshot run %d of %s: status %s, event %s; want a successful workflow_dispatch", run.ID, run.WorkflowID, run.Status, run.TriggerEvent)
	}

	const filesPrefix = snapshotArtifact + "-files-"

	trees := map[string]map[string][]byte{}

	for _, artifact := range livetest.RunArtifacts(t, target, repo, run.ID) {
		if artifact.Name != snapshotArtifact+"-plan" && !strings.HasPrefix(artifact.Name, filesPrefix) {
			continue
		}

		if artifact.Expired || artifact.SizeInBytes <= 0 || artifact.SizeInBytes > 256<<20 {
			t.Fatalf("invalid snapshot artifact: %+v", artifact)
		}

		address := fmt.Sprintf("%s/api/v1/repos/%s/actions/artifacts/%d/zip", target.BaseURL(), livetest.RepoSlug(target, repo), artifact.ID)

		tree, err := livetest.ZIPTree(livetest.Download(t, target, address, 256<<20), 256<<20)
		if err != nil {
			t.Fatalf("run artifact %s: %v", artifact.Name, err)
		}

		trees[artifact.Name] = tree
	}

	frozen, planned := trees[snapshotArtifact+"-plan"]["snapshot-plan.json"]
	if !planned {
		t.Fatalf("run %d kept no snapshot-plan.json in %s-plan; its artifacts of the namespace are %v", run.ID, snapshotArtifact, slices.Sorted(maps.Keys(trees)))
	}

	delete(trees, snapshotArtifact+"-plan")

	expected := livetest.RequireSnapshotFilesPlan(t, livetest.SnapshotRun{
		Declaration: declaration, Branch: config.Branch, Head: head, Plan: frozen,
	})

	// Each planned output's bundle is the artifact of its planned name; what
	// is left under any other name stays under it, an unplanned bundle.
	bundles := map[string]map[string][]byte{}
	kept := make([]string, 0, len(expected.Files))

	for _, file := range expected.Files {
		name := filesPrefix + expected.FilesArtifactName(file.Name)
		if tree, found := trees[name]; found {
			bundles[file.Name] = tree

			kept = append(kept, name)

			delete(trees, name)
		}
	}

	maps.Copy(bundles, trees)

	if findings := livetest.SnapshotFilesFindings(expected, bundles); len(findings) != 0 {
		t.Fatalf("run %d did not keep the file outputs its plan names, as %s<artifact>-%s:\n%s", run.ID, filesPrefix, expected.ArtifactVersion(), strings.Join(findings, "\n"))
	}

	return kept
}

// snapshotArtifact is the snapshot-images workflow's artifact namespace, its
// artifact-name input's default: the collect job's receipts, and with -plan,
// the frozen plan.
const snapshotArtifact = "snapshot-images"

// requireServiceSnapshot is requireSnapshotImages of a service snapshot, the
// lifecycle's development phase: its plan has one image, whose receipt it
// returns with the expected plan.
func requireServiceSnapshot(ctx context.Context, t *testing.T, target livetest.Target, repo string, consumer livetest.Consumer, branch, head, image string, plan, receipts []byte) (snapshotimage.Receipt, snapshotimage.Plan) {
	t.Helper()

	published, expected := requireSnapshotImages(ctx, t, target, repo, consumer, branch, head, image, plan, receipts)
	if len(published) != 1 {
		t.Fatalf("a service snapshot published %d images, want one", len(published))
	}

	return published[0], expected
}

// requireSnapshotImages reads what a snapshot of a consumer left, from its
// frozen plan and its receipts: the plan the trial expects for the selected
// source head and branch, which the planner makes of the staged declaration,
// every build native amd64, and a receipt of each planned image, bound to
// them as the product's own collection requires (snapshotimage.Collect),
// each image the registry serves under its receipt's tag, with no alias but
// the tags of the planned images. It returns the receipts, in plan order,
// and the expected plan.
func requireSnapshotImages(ctx context.Context, t *testing.T, target livetest.Target, repo string, consumer livetest.Consumer, branch, head, image string, plan, receipts []byte) ([]snapshotimage.Receipt, snapshotimage.Plan) {
	t.Helper()

	registry := livetest.LabSnapshotRegistry(t, target, repo)
	if registry.Image() != image {
		t.Fatalf("the run's image repository is %s, the registry's %s", image, registry.Image())
	}

	run := livetest.SnapshotRun{Declaration: livetest.ServiceSnapshotDeclaration(ctx, t, consumer), Branch: branch, Head: head, Plan: plan, Receipts: receipts}

	published, expected := livetest.RequireSnapshotImages(ctx, t, run, registry)

	// The trial's overlay plans every build native amd64.
	if slices.ContainsFunc(expected.Builds, func(build snapshotimage.Build) bool { return build.Arch != "amd64" }) {
		t.Fatalf("the snapshot plan builds beyond native amd64: %+v", expected.Builds)
	}

	return published, expected
}
