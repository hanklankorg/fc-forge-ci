// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

// The jobs of sign-and-publish-release.yml, by the names the layer gives them.
const forgejoSignJob = "Sign: files and images"

var forgejoPublishJobs = []string{"Publish: images", "Publish: packages", "Publish: release", "Publish: tidy up"}

// A consumer's unsigned hand-off that already holds the provenance statement
// is refused by the signing job before anything is signed.
//
// The consumer's own release workflow runs unchanged but for one step the
// harness adds, in its private index, before the hand-off upload: it writes
// an inert dist/slsa-provenance.intoto.json. Signing would write that file
// and publication would offer what it finds, so a statement the producer
// placed would be published as the signer's.
//
// The run is expected to FAIL in "Sign: files and images" and only there:
// its log gives the engine's refusal of that file, it left no signed-dist
// artifact, no publication job was taken by a runner, and the repository has
// no release and the owner no new package. The release commit and the final
// tag exist: on Forgejo prepare-release.yml pushes both before the build.
func TestForgejoRelease_RejectsPreseededProvenance(t *testing.T) {
	livetest.RegisterLiveScenario(t)

	forScenarioConsumer(t, provider.ForgeForgejo, "the refused hand-off requires a reviewed native release consumer or a prepared directory", func(t *testing.T, root string) {
		t.Helper()

		for _, visibility := range livetest.ConsumerVisibilities(t) {
			t.Run(visibility.Name, func(t *testing.T) {
				runForgejoConsumerTrial(t, root, forgejoPreseededProvenanceRelease, visibility.Private)
			})
		}
	})
}

// A release interrupted between the unsigned hand-off and signing is
// recovered the way docs/forgejo.md gives an operator of this Forgejo: by a
// release request for the next patch version.
//
// The interruption point is the harness's: two jobs it adds to the consumer's
// release workflow, in its private index, after everything the signing
// workflow needs. The run is cancelled once the first has succeeded, and the
// signing workflow needs the second, which holds until then and fails if
// nothing cancels it. No job of the layer is changed.
//
// The cancelled run has prepared the release and released nothing: main is
// one signed commit ahead with the final tag on it, no signing or publication
// job was taken by a runner, and there is no release and no package of the
// version. In this recovery that is where the version stays: a final tag is
// never released a second time. Running the cancelled jobs again instead is
// TestForgejoRelease_RerunAfterInterruption's, on Forgejo 17 or later.
//
// The hold is then released and the next patch version requested on a new
// commit of main. That run must succeed with the release every trial is held
// to. The interrupted version keeps its tag and has no release and no
// package, and the new release offers nothing named for it.
func TestForgejoRelease_InterruptedRecovery(t *testing.T) {
	livetest.RegisterLiveScenario(t)

	forScenarioConsumer(t, provider.ForgeForgejo, "the interrupted recovery requires a reviewed native release consumer or a prepared directory", func(t *testing.T, root string) {
		t.Helper()

		for _, visibility := range livetest.ConsumerVisibilities(t) {
			t.Run(visibility.Name, func(t *testing.T) {
				runForgejoConsumerTrial(t, root, forgejoInterruptedRecoveryRelease, visibility.Private)
			})
		}
	})
}

// A release interrupted between the unsigned hand-off and signing is
// finished by running its cancelled jobs again, in the same run, through
// the API Forgejo 17 added for it.
//
// The release is interrupted as in TestForgejoRelease_InterruptedRecovery.
// The hold is then released and the first job the run cancelled after the
// checkpoint, the hold, is rerun with the jobs that need it, the caller of
// signing and publication among them. They run in the run that was
// cancelled, and no run is added: the hold at attempt 2, and signing at
// attempt 1, since Forgejo creates a called workflow's jobs when its caller
// is unblocked, and the interruption came before that. The jobs before
// the checkpoint are not rerun, so signing takes the build-dist hand-off
// attempt 1 uploaded: whether Forgejo serves a rerun job an earlier
// attempt's artifacts is what this row proves.
//
// The run must end in the release every trial is held to, of the version
// that was interrupted and on the commit its first attempt prepared: one
// release, the two tags of one request, and the packages the release
// publishes, and no other.
func TestForgejoRelease_RerunAfterInterruption(t *testing.T) {
	livetest.RegisterLiveScenario(t)

	if livetest.Selected(t, provider.ForgeForgejo) && !livetest.Requires(t, provider.ForgeForgejo, livetest.NeedsForgejo17Actions) {
		t.Skip("this Forgejo cannot rerun a job over its API; the logged SKIP names its version")
	}

	forScenarioConsumer(t, provider.ForgeForgejo, "the rerun after an interruption requires a reviewed native release consumer or a prepared directory", func(t *testing.T, root string) {
		t.Helper()

		for _, visibility := range livetest.ConsumerVisibilities(t) {
			t.Run(visibility.Name, func(t *testing.T) {
				runForgejoConsumerTrial(t, root, forgejoRerunAfterInterruptionRelease, visibility.Private)
			})
		}
	})
}

// requireForgejoReleaseCase refuses the settings a scenario's own end cannot
// be judged with: it requests one release of release.yml, the workflow its
// fixture is staged in, publishes only to the lab, and keeps nothing.
func requireForgejoReleaseCase(t *testing.T, releaseCase forgejoReleaseCase, settings livetest.TrialSettings) {
	t.Helper()

	if releaseCase == forgejoConsumerRelease {
		return
	}

	_, held := settings.Variables[livetest.ForgejoRecoveryHoldVariable]

	if settings.Event != livetest.TrialRelease || settings.Workflow != "release.yml" || settings.NextVersion != "" ||
		settings.InterruptAfter != "" || len(settings.Images) != 0 || held || livetest.DemoName() != "" {
		t.Fatalf("a release scenario requests one release of release.yml on the lab's own registry: no next-version, interrupt-after, image, %s or demo", livetest.ForgejoRecoveryHoldVariable)
	}
}

func requireForgejoPreseededProvenanceRefusal(t *testing.T, scenario layerScenario, requester livetest.RequestSigner, keys releaseKeys, settings livetest.TrialSettings, request string, secrets ...map[string]string) {
	t.Helper()

	const (
		refusal = livetest.PreseededProvenanceRefusal
		// The signing job says this once it has signed and uploads signed-dist.
		signed = "signed dist/ digest:"
	)

	target, repo := scenario.target, scenario.repo
	requestTag, finalTag := "release-request/v"+settings.Version, "v"+settings.Version
	packages := livetest.OwnerPackages(t, target)

	requester.PushTag(t, target, repo, requestTag, layerMain, "Request release "+finalTag)

	run := livetest.WaitForConsumerRun(t, target, repo, settings.Workflow, "push", "")
	jobs := scenario.requireStatus(t, run, jobFailure, secrets...)
	refused := false

	for _, job := range jobs {
		if slices.Contains(forgejoPublishJobs, job.Name) && job.Started() {
			t.Errorf("publication job %q ended %s on a runner after a preseeded provenance statement", job.Name, job.Status)
		}

		if job.Name != forgejoSignJob {
			continue
		}

		if job.Status != jobFailure || !job.Started() {
			t.Fatalf("job %q ended %s, want it to fail on the runner", job.Name, job.Status)
		}

		log := livetest.JobLog(t, target, repo, job)
		requireNoReleaseKey(t, job.Name, log, keys)

		if !strings.Contains(log, refusal) {
			t.Errorf("the log of job %q does not give the refusal %q", job.Name, refusal)
		}

		if strings.Contains(log, signed) {
			t.Errorf("the log of job %q says %q of a hand-off it was to refuse", job.Name, signed)
		}

		refused = true
	}

	if !refused {
		t.Errorf("the run has no job %q, so the hand-off was not refused where signing starts", forgejoSignJob)
	}

	artifacts := livetest.RunArtifacts(t, target, repo, run.ID)
	logArtifacts(t, run, artifacts)

	names := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		names = append(names, artifact.Name)
	}

	if !slices.Contains(names, "build-dist") || slices.Contains(names, "signed-dist") {
		t.Errorf("the refused run left artifacts %v, want the unsigned build-dist and no signed-dist", names)
	}

	// Prepared, as every Forgejo release is before its build, and no further.
	prepared := requireReleaseGit(t, target, repo, keys, request, requestTag, finalTag)
	scenario.requireBranches(t, map[string]string{layerMain: prepared})
	scenario.requireNone(t, "/releases")
	scenario.requireSamePackages(t, packages)

	if tags := livetest.RepoCount(t, target, repo, "/tags"); tags != 2 {
		t.Errorf("the repository has %d tags, want the request tag and the prepared final tag", tags)
	}

	requireRunIDs(t, target, repo, run.ID)
}

func recoverForgejoReleaseAfterHandoff(t *testing.T, scenario layerScenario, requester livetest.RequestSigner, keys releaseKeys, settings livetest.TrialSettings, external *livetest.ExternalRegistry, request string, secrets ...map[string]string) {
	t.Helper()

	target, repo := scenario.target, scenario.repo
	finalTag := "v" + settings.Version
	packages := awaitedPackages(t, target, repo, settings)

	interrupted, _, prepared := interruptForgejoReleaseAtHandoff(t, scenario, requester, keys, settings, request, packages, secrets...)

	// The recovery: the hold is released, and the next patch version is
	// requested on a new commit of main, as after any release.
	next, err := livetest.NextPatchVersion(settings.Version)
	if err != nil {
		t.Fatal(err)
	}

	livetest.SetRepoVariable(t, target, repo, livetest.ForgejoRecoveryHoldVariable, livetest.ForgejoRecoveryHoldReleased)

	candidate := nextReleaseCandidate(t, scenario, settings, request)
	interruptedVersion := settings.Version
	settings.Version = next

	recovered, released := requestRelease(t, scenario, requester, keys, settings, external, candidate, interrupted.ID, secrets...)
	if released == prepared {
		t.Errorf("the release of v%s is the commit %s the interrupted run prepared for %s", next, prepared, finalTag)
	}

	// The interrupted version is where its run left it: a tag on the commit
	// it prepared, and nothing published.
	for _, release := range livetest.Releases(t, target, repo) {
		if release.TagName == finalTag {
			t.Errorf("the interrupted version has a release %s after the recovery", finalTag)
		}
	}

	if tagged := livetest.TagCommitSHA(t, target, repo, finalTag); tagged != prepared {
		t.Errorf("the tag %s names %s after the recovery, want the commit %s its run prepared", finalTag, tagged, prepared)
	}

	for _, name := range packages {
		if listed := livetest.PublishedPackageVersions(t, target, repo, "npm", name); slices.Contains(listed, interruptedVersion) {
			t.Errorf("the owner's registry lists %s@%s, the version that was interrupted before signing", name, interruptedVersion)
		}
	}

	// Nothing of the interrupted run reached the new release.
	for _, asset := range assetNames(releaseOfTag(t, target, repo, "v"+next)) {
		if strings.Contains(asset, interruptedVersion) {
			t.Errorf("the release v%s offers %s, named for the interrupted version", next, asset)
		}
	}

	if tags := livetest.RepoCount(t, target, repo, "/tags"); tags != 4 {
		t.Errorf("the repository has %d tags after the recovery, want two request tags and two final tags", tags)
	}

	if releases := livetest.RepoCount(t, target, repo, "/releases"); releases != 1 {
		t.Errorf("the repository has %d releases after the recovery, want the one of v%s", releases, next)
	}

	requireRunIDs(t, target, repo, interrupted.ID, recovered.ID)
	t.Logf("run %d was interrupted at %q and left %s without a release; run %d released v%s of %s", interrupted.ID, livetest.ForgejoRecoveryCheckpointReached, finalTag, recovered.ID, next, released)
}

func rerunForgejoReleaseAfterHandoff(t *testing.T, scenario layerScenario, requester livetest.RequestSigner, keys releaseKeys, settings livetest.TrialSettings, external *livetest.ExternalRegistry, request, workflow string, secrets ...map[string]string) {
	t.Helper()

	target, repo := scenario.target, scenario.repo
	finalTag := "v" + settings.Version
	packages := awaitedPackages(t, target, repo, settings)
	distro := awaitedDistroPackages(t, target, repo, settings)
	owned := livetest.OwnerPackages(t, target)

	interrupted, before, prepared := interruptForgejoReleaseAtHandoff(t, scenario, requester, keys, settings, request, packages, secrets...)

	at := slices.IndexFunc(before, func(job livetest.RunJob) bool { return job.Name == livetest.ForgejoRecoveryCheckpointHold })
	if at < 0 || before[at].Status != runCancelled {
		t.Fatalf("the interrupted run has no cancelled job %q, the first after the checkpoint, to rerun", livetest.ForgejoRecoveryCheckpointHold)
	}

	hold := before[at]

	dependants, err := livetest.ForgejoRecoveryHoldDependants(workflow)
	if err != nil {
		t.Fatalf("read the jobs that need the hold from %s: %v", settings.Workflow, err)
	}

	// The interruption came before the caller of signing and publication was
	// unblocked, so Forgejo has not created their jobs yet.
	if slices.ContainsFunc(before, func(job livetest.RunJob) bool { return job.Name == forgejoSignJob }) {
		t.Errorf("job %q is in the interrupted run, want it created when the rerun unblocks its caller", forgejoSignJob)
	}

	// The rerun of the hold runs again what the interruption stopped, and
	// nothing else: every job that did not succeed is the hold or one that
	// needs it, directly or through another job, or the job that calls
	// signing and publication.
	for _, job := range before {
		if job.Status != jobSuccess && job.ID != hold.ID && !slices.Contains(dependants, job.Name) && len(job.RunsOn) != 0 {
			t.Fatalf("job %q ended %s in the interrupted run, and it is neither the hold nor a job that needs it, so a rerun of the hold would leave it as it is", job.Name, job.Status)
		}
	}

	// The hold's rerun reads the variable and ends at once; the caller of
	// signing and publication needs the hold, is rerun with it, and Forgejo
	// then creates their jobs.
	livetest.SetRepoVariable(t, target, repo, livetest.ForgejoRecoveryHoldVariable, livetest.ForgejoRecoveryHoldReleased)
	livetest.RerunJob(t, target, repo, hold)

	run := livetest.WaitForRerun(t, target, repo, settings.Workflow, "push", interrupted, hold)

	// Before the run is judged, so a run that published and then failed
	// leaves no package behind.
	distro = publishedDistroPackages(t, target, repo, distro)

	after := livetest.RunJobs(t, target, repo, run.ID)
	scenario.logJobs(t, after, secrets...)
	logArtifacts(t, run, livetest.RunArtifacts(t, target, repo, run.ID))

	if run.Status != jobSuccess {
		for _, job := range after {
			t.Logf("rerun: job %q ended %s at attempt %d", job.Name, job.Status, job.Attempt)
		}

		t.Logf("rerun: the jobs before the checkpoint were not rerun, so %q, created by the rerun, downloads the build-dist artifact of attempt 1; "+
			"if it failed there, Forgejo does not serve a rerun job an earlier attempt's artifacts", forgejoSignJob)
	}

	released := requireReleased(t, scenario, keys, settings, external, request, run, packages, distro)

	// The rerun is the one asked for: the hold at attempt 2, signing created
	// by the rerun and taken by a runner once, and every job that had
	// succeeded at its first attempt, the hand-off's build among them, not
	// run again.
	attempts := make(map[int64]int64, len(after))
	signed := 0

	for _, job := range after {
		attempts[job.ID] = job.Attempt

		if job.Name != forgejoSignJob {
			continue
		}

		signed++

		if job.Attempt != 1 || !job.Started() {
			t.Errorf("job %q ended %s at attempt %d, want it signed on a runner at its first attempt", job.Name, job.Status, job.Attempt)
		}
	}

	if signed != 1 {
		t.Errorf("the rerun has %d jobs %q, want one", signed, forgejoSignJob)
	}

	if attempts[hold.ID] != 2 {
		t.Errorf("job %q is at attempt %d after its rerun, want 2", hold.Name, attempts[hold.ID])
	}

	for _, job := range before {
		if job.Status == jobSuccess && attempts[job.ID] != 1 {
			t.Errorf("job %q succeeded at attempt 1 and is at attempt %d after the rerun, want it not run again", job.Name, attempts[job.ID])
		}
	}

	// One release of the version, on the commit its first attempt prepared.
	if released != prepared {
		t.Errorf("the rerun released %s, want the commit %s attempt 1 prepared", released, prepared)
	}

	if releases := livetest.RepoCount(t, target, repo, "/releases"); releases != 1 {
		t.Errorf("the repository has %d releases after the rerun, want the one of %s", releases, finalTag)
	}

	if tags := livetest.RepoCount(t, target, repo, "/tags"); tags != 2 {
		t.Errorf("the repository has %d tags after the rerun, want the request tag and the final tag", tags)
	}

	// The packages the release publishes, which requireReleased checks,
	// and no other. A release of files alone publishes none, so the
	// owner's packages are as they were.
	if len(packages) == 0 && len(distro) == 0 {
		scenario.requireSamePackages(t, owned)
	}

	requireRunIDs(t, target, repo, interrupted.ID)
	t.Logf("run %d was interrupted at %q; rerunning %q released %s of %s in the same run", interrupted.ID, livetest.ForgejoRecoveryCheckpointReached, hold.Name, finalTag, released)
}

// interruptForgejoReleaseAtHandoff requests the release of settings.Version
// at request and judges the run that is cancelled at the recovery
// checkpoint: interrupted there, before any signing or publication, with the
// version prepared and nothing of it released. It returns the run, its jobs
// and the prepared release commit.
func interruptForgejoReleaseAtHandoff(t *testing.T, scenario layerScenario, requester livetest.RequestSigner, keys releaseKeys, settings livetest.TrialSettings, request string, packages []string, secrets ...map[string]string) (livetest.WorkflowRun, []livetest.RunJob, string) {
	t.Helper()

	target, repo := scenario.target, scenario.repo
	requestTag, finalTag := "release-request/v"+settings.Version, "v"+settings.Version

	requester.PushTag(t, target, repo, requestTag, layerMain, "Request release "+finalTag)

	interrupted := livetest.WaitForConsumerRun(t, target, repo, settings.Workflow, "push", livetest.ForgejoRecoveryCheckpointReached)
	before := livetest.RunJobs(t, target, repo, interrupted.ID)
	scenario.logJobs(t, before, secrets...)
	requireInterrupted(t, target, repo, interrupted, finalTag)

	// Interrupted at the checkpoint, and before any signing or publication.
	reached := false

	for _, job := range before {
		switch {
		case job.Name == livetest.ForgejoRecoveryCheckpointReached:
			reached = job.Status == jobSuccess && job.Started()
		case job.Name == forgejoSignJob || slices.Contains(forgejoPublishJobs, job.Name):
			if job.Status == jobSuccess || job.Started() {
				t.Errorf("job %q ended %s on a runner in the run that was interrupted before signing", job.Name, job.Status)
			}
		case job.Name == livetest.ForgejoRecoveryCheckpointHold && job.Status == jobSuccess:
			t.Errorf("job %q succeeded in the run that was interrupted before signing", job.Name)
		}
	}

	if !reached {
		t.Fatalf("the interrupted run has no successful job %q on a runner, so it was not interrupted at the checkpoint", livetest.ForgejoRecoveryCheckpointReached)
	}

	prepared := requireReleaseGit(t, target, repo, keys, request, requestTag, finalTag)
	scenario.requireNone(t, "/releases")

	if tags := livetest.RepoCount(t, target, repo, "/tags"); tags != 2 {
		t.Errorf("the interrupted run left %d tags, want the request tag and the prepared final tag", tags)
	}

	for _, name := range packages {
		if listed := livetest.PublishedPackageVersions(t, target, repo, "npm", name); slices.Contains(listed, settings.Version) {
			t.Errorf("the interrupted run published %s@%s before signing", name, settings.Version)
		}
	}

	if t.Failed() {
		t.FailNow()
	}

	return interrupted, before, prepared
}
