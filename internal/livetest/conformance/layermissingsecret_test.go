// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	_ "embed"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/livetest"
)

//go:embed testdata/layer/prepare-release-missing-secret.yml
var layerMissingSecretFixture string

// otherReleaseSecret matches the diagnostic line of any secret the fixture
// does pass. The diagnostic lists each missing secret as "- NAME".
var otherReleaseSecret = regexp.MustCompile(`-[[:space:]]+(RELEASE_SSH_SIGNING_KEY|RELEASE_COSIGN_PRIVATE_KEY|RELEASE_COSIGN_PASSPHRASE|RELEASE_GPG_PRIVATE_KEY|RELEASE_GPG_FINGERPRINT|RELEASE_GPG_PASSPHRASE|RELEASE_TOKEN)([^A-Z0-9_]|$)`)

// LAYER-2: prepare-release.yml stops on a missing secret before it does
// anything else, and says which.
//
// The claim is about order. The secret check is the first step of the job
// that renders the changelog, and it runs before the
// layer's files are fetched: a release that cannot finish must not start. So
// the run is expected to FAIL, and what is asserted is how: the called
// workflow's coordinate job succeeded, its render job failed on a first
// attempt, the log names the one absent secret and none of the present ones,
// no later step left a trace in it, no secret value or credential is in it,
// and the repository and the owner's packages are exactly as they were.
func TestForgejoLayer_PrepareReleaseStopsOnAMissingSecret(t *testing.T) {
	livetest.RegisterLiveScenario(t)
	for _, visibility := range livetest.ConsumerVisibilities(t) {
		t.Run(visibility.Name, func(t *testing.T) { runLayerMissingSecret(t, visibility.Private) })
	}
}

func runLayerMissingSecret(t *testing.T, private bool) {
	t.Helper()

	const (
		workflow     = "layer-missing-secret"
		absentSecret = "RELEASE_REGISTRY_TOKEN"
		prepareJob   = "Prepare: changelog"
	)

	scenario := newLayerScenario(t, "layer-missing-secret", private, "sha1")
	target, repo := scenario.target, scenario.repo

	packages := livetest.OwnerPackages(t, target)

	main := livetest.CommitFiles(t, target, repo, livetest.FileCommit{
		Branch: layerMain, Message: "livetest: " + workflow,
		Files: map[string]string{".forgejo/workflows/" + workflow + ".yml": scenario.render(t, layerMissingSecretFixture)},
	})

	run := livetest.WaitForLayerRun(t, target, repo, workflow, "push")
	jobs := scenario.requireStatus(t, run, jobFailure)

	// The called workflow's two jobs ran on the runner; the calling job has
	// none and ends blocked or failed with the workflow it called.
	statuses := make(map[string]string, len(jobs))
	for _, job := range jobs {
		statuses[job.Name] = job.Status
	}

	onRunner, wrappers := requireJobsOnRunner(t, jobs, run.ID, scenario.label)
	if !slices.Contains(onRunner, prepareJob) || !slices.Contains(onRunner, "Check: release settings") ||
		!slices.Contains(wrappers, "prepare-release-missing-secret") {
		t.Fatalf("the run's jobs are %v on the runner and %v without one, want the render and coordinate jobs under one caller", onRunner, wrappers)
	}

	if status := statuses["Prepare: release commit and tag"]; status != "skipped" && status != "blocked" {
		t.Errorf("the signing job ended %q after a missing secret, want skipped or blocked", status)
	}

	if statuses["Check: release settings"] != jobSuccess || statuses[prepareJob] != jobFailure ||
		statuses["prepare-release-missing-secret"] != "blocked" && statuses["prepare-release-missing-secret"] != jobFailure {
		t.Errorf("job statuses are %v, want the coordinate job successful and the prepare job failed", statuses)
	}

	var log string

	for _, job := range jobs {
		if job.Name == prepareJob {
			log = livetest.JobLog(t, target, repo, job)
		}
	}

	if !strings.Contains(log, "ERROR: missing required secrets:") || !strings.Contains(log, absentSecret) {
		t.Errorf("the prepare job's log does not say that %s is missing", absentSecret)
	}

	if named := otherReleaseSecret.FindString(log); named != "" || strings.Contains(log, "All required secrets are configured.") {
		t.Errorf("the prepare job's log reports a secret the fixture passes as missing, or none as missing: %q", named)
	}

	if strings.Contains(log, "acceptance-present-not-a-credential") {
		t.Error("the prepare job's log holds the value of a secret")
	}

	// The steps that follow the check: fetching the layer's files, and the
	// consumer's. A name in the log means the step started.
	for _, later := range []string{"Checkout layer helpers at the caller's pin", "Checkout consumer repository at release request tag"} {
		if strings.Contains(log, later) {
			t.Errorf("the prepare job went on to %q after the secret check failed", later)
		}
	}

	scenario.requireBranches(t, map[string]string{layerMain: main})
	scenario.requireNone(t, "/tags", "/releases")
	scenario.requireSamePackages(t, packages)

	if artifacts := livetest.RunArtifacts(t, target, repo, run.ID); len(artifacts) != 0 {
		t.Errorf("the failed run left artifacts: %+v", artifacts)
	}
}
