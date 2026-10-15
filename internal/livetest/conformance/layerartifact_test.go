// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"bytes"
	_ "embed"
	"slices"
	"strconv"
	"testing"

	"github.com/diggsweden/forge-ci/internal/livetest"
)

//go:embed testdata/layer/artifact-roundtrip.yml
var layerArtifactFixture string

// LAYER-1: two jobs of a consumer workflow set the layer's binary up from the
// pinned commit, and an artifact the first uploads reaches the second intact.
//
// The jobs assert for themselves, and a failed assertion fails the run: the
// run is this repository on this forge; the binary is the one whose digest
// the fixture names, in both jobs, and the first job's digest reaches the
// second as a job output; the upload reports its artifact name as a step
// output; a failing CLI fails its step; and the downloaded tree, hidden and
// empty files included, is byte-identical to the uploaded one.
//
// The host then asks the forge, with no product in the path: the fixture's
// push started the only run; that run is exactly the two jobs, each
// successful on the contract's runner label; and its one artifact, named
// for the run and not expired, holds exactly the tree the job uploaded.
func TestForgejoLayer_ArtifactRoundTrip(t *testing.T) {
	for _, visibility := range livetest.ConsumerVisibilities(t) {
		t.Run(visibility.Name, func(t *testing.T) { runLayerArtifactRoundTrip(t, visibility.Private) })
	}
}

func runLayerArtifactRoundTrip(t *testing.T, private bool) {
	t.Helper()
	const workflow = "layer-artifact-roundtrip"

	scenario := newLayerScenario(t, "layer-artifact", private, "sha1")
	target, repo, label := scenario.target, scenario.repo, scenario.label

	run := livetest.RunLayerWorkflow(t, target, repo, workflow, scenario.render(t, layerArtifactFixture))
	jobs := scenario.requireStatus(t, run, jobSuccess)

	// Pushing the layer's branch must have started nothing, or the run judged
	// above might not be the fixture's.
	if runs := livetest.WorkflowRuns(t, target, repo); len(runs) != 1 || runs[0].ID != run.ID {
		t.Fatalf("the repository has %d runs, want only the fixture's: %+v", len(runs), runs)
	}

	if run.Event != "push" || run.WorkflowID != workflow+".yml" {
		t.Errorf("the run is %q of %q, want the push of %s.yml", run.Event, run.WorkflowID, workflow)
	}

	assertLayerJobs(t, jobs, run.ID, label,
		"Upload deterministic artifact fixture",
		"Download and independently compare artifact fixture",
	)

	artifacts := livetest.RunArtifacts(t, target, repo, run.ID)
	if len(artifacts) != 1 {
		t.Fatalf("the run left %d artifacts, want exactly one: %+v", len(artifacts), artifacts)
	}

	// The run attempt is 1: nothing reruns a job of a repository this new.
	artifact := artifacts[0]
	if want := "layer-artifact-roundtrip-" + strconv.FormatInt(run.ID, 10) + "-1"; artifact.Name != want || artifact.Expired {
		t.Errorf("the artifact is %q (expired: %t), want the live %q", artifact.Name, artifact.Expired, want)
	}

	tree, err := livetest.ZIPTree(livetest.ArtifactZIP(t, target, repo, artifact.ID), 16<<20)
	if err != nil {
		t.Fatalf("the artifact the forge serves is not a plain tree: %v", err)
	}

	assertLayerArtifactTree(t, tree)
}

// layerArtifactTree is the tree the fixture's upload job builds, restated here
// so the comparison does not depend on the fixture's own copy of it.
func layerArtifactTree() map[string][]byte {
	return map[string][]byte{
		".hidden-manifest": []byte("hidden\n"),
		"empty-marker":     {},
		"nanolinter-9.9.9-linux-x86_64.tar.gz.sbom.json":            []byte("{\"bomFormat\":\"CycloneDX\"}\n"),
		"nanolinter-pie-musl_aarch64-unknown-linux-musl/nanolinter": make([]byte, 2_500_000),
		"nanolinter-pie_x86_64-unknown-linux-gnu/nanolinter":        make([]byte, 3_000_000),
		"nanolinter_9.9.9_amd64.deb":                                make([]byte, 40_000),
		"nanolinter_9.9.9_checksums.txt":                            []byte("sha256  artifact\n"),
		"nanolinter_9.9.9_checksums.txt.bundle":                     []byte("cosign-bundle\n"),
	}
}

// assertLayerArtifactTree names each file that is missing, different or
// unexpected. Lengths and not contents are printed: three of the files are
// megabytes of zeroes.
func assertLayerArtifactTree(t *testing.T, tree map[string][]byte) {
	t.Helper()

	want := layerArtifactTree()

	for name, content := range want {
		got, present := tree[name]

		switch {
		case !present:
			t.Errorf("the artifact lacks %s", name)
		case !bytes.Equal(got, content):
			t.Errorf("the artifact's %s differs from what was uploaded (%d bytes, want %d)", name, len(got), len(content))
		}
	}

	for name := range tree {
		if _, expected := want[name]; !expected {
			t.Errorf("the artifact holds %s, which was never uploaded", name)
		}
	}
}

// assertLayerJobs holds a run to exactly the named jobs, in the forge's order,
// each a distinct job of this run that succeeded on the runner label.
func assertLayerJobs(t *testing.T, jobs []livetest.RunJob, runID int64, label string, names ...string) {
	t.Helper()

	if len(jobs) != len(names) {
		t.Fatalf("the run has %d jobs, want %d: %+v", len(jobs), len(names), jobs)
	}

	seen := make(map[int64]bool, len(jobs))

	for index, job := range jobs {
		if job.ID <= 0 || seen[job.ID] || job.RunID != runID {
			t.Errorf("job %d is not a distinct job of run %d: %+v", index, runID, job)
		}

		seen[job.ID] = true

		if job.Name != names[index] || job.Status != jobSuccess || !slices.Equal(job.RunsOn, []string{label}) {
			t.Errorf("job %d is %q, %s on %v; want %q, success on [%s]", index, job.Name, job.Status, job.RunsOn, names[index], label)
		}
	}
}
