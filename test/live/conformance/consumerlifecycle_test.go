// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"maps"
	"os"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

// lifecycleBranch is the branch the change under review goes to, and the
// branch the service development snapshot is taken from.
const lifecycleBranch = "development"

// A consumer lifecycle: the linked flow one consumer goes through in one
// repository, run on the lab against the layer of this checkout. It is a
// demo's with a development route: the harness's service development
// workflow over the service snapshot of its one container, for a consumer of
// any snapshot family (docs/live-runs.md, Lifecycle):
//
//  1. a pull request from development into main, judged by the consumer's
//     own pull-request workflow;
//  2. its native merge, with a merge commit of the base and the change;
//  3. the service development snapshot, dispatched on the merged main and
//     taken from development;
//  4. a signed release request at the merged main, judged as a release
//     trial's is: main advanced by one signed release commit, signed tag,
//     release with Cosign bundles that verify;
//  5. the repository holds those three runs, two tags and one release, and
//     nothing else.
//
// Each phase ends the lifecycle when it fails, and says which phase it was.
// The requester's public trust goes into the consumer's tree before the base
// commit, so the merged main carries it; a release trial commits it remotely,
// which a linked flow cannot. The release's secrets are set only before the
// release phase; the development phase gets the development registry
// credential alone, and the pull request none but the caller's.
func TestForgejoLayer_ConsumerLifecycle(t *testing.T) {
	livetest.RegisterLiveScenario(t)

	forEachConsumerCheckout(t, provider.ForgeForgejo, "just test-live-consumer forgejo <checkout> lifecycle", func(t *testing.T, root string) {
		t.Helper()

		if livetest.DemoName() != "" {
			t.Fatal("a lifecycle runs in disposable scratch repositories, not in a kept demo")
		}

		for _, visibility := range livetest.ConsumerVisibilities(t) {
			t.Run(visibility.Name, func(t *testing.T) {
				runForgejoConsumerLifecycle(t, root, visibility.Private)
			})
		}
	}, livetest.TrialLifecycle)
}

func runForgejoConsumerLifecycle(t *testing.T, root string, private bool) {
	t.Helper()

	settings, err := livetest.ParseTrialSettings(os.Getenv(consumerEventEnv), consumerSettingsWords())
	if err != nil {
		t.Fatal(err)
	}

	if settings.Event != livetest.TrialLifecycle {
		t.Fatalf("the event is %s; a lifecycle starts with %s", settings.Event, livetest.TrialLifecycle)
	}

	builder, err := livetest.ForgejoConsumerImageBuilder(settings.Variables, os.Getenv(livetest.ForgejoImageBuilderEnv))
	if err != nil {
		t.Fatal(err)
	}

	target := livetest.Accept(t, provider.ForgeForgejo)
	if !livetest.Requires(t, provider.ForgeForgejo, livetest.NeedsInRunner, livetest.NeedsPackages) {
		livetest.NotCoveredf(t, "a lifecycle needs a native runner and packages")
	}

	if livetest.RunnerArch(t, target) != "amd64" {
		t.Fatal("the service development phase requires native linux/amd64")
	}

	// The service's readiness runs the published image with Podman, on the
	// contract's runner that permits it, whichever builder builds the image.
	containerLabel := livetest.ContainerRunnerLabel(t, target, "service readiness runs the published image with Podman")

	// The development phase snapshots the consumer's one container from the
	// declaration its release builds its images from; the snapshot builds
	// the producers that declaration names in the lifecycle's job image.
	declaration, err := livetest.ServiceSnapshotSource(root)
	if err != nil {
		t.Fatal(err)
	}

	config := livetest.ServiceDevelopmentConfig{
		RecipeDirectory: root, Declaration: declaration, JobImage: settings.Variables["CI_JOB_IMAGE"], Branch: lifecycleBranch,
		ReadinessURL: os.Getenv(serviceReadinessURLEnv), Fixture: os.Getenv(serviceFixtureEnv),
	}

	const name = livetest.ConsumerLifecycle

	repoName := livetest.ScratchRepo(name)
	facts := livetest.ConsumerFacts{Inputs: map[string]string{}, Secrets: map[string]string{}}
	keys := trialReleaseFacts(t, target, repoName, &facts)
	requester := livetest.NewRequestSigner(t, t.TempDir())
	external := externalRegistry(t, settings)
	useExternalRegistry(t, external, &facts)
	useLabRegistry(t, target, repoName, external, &facts)

	source := livetest.OpenConsumer(t, target, root)
	consumer := livetest.NewServiceDevelopmentFixture(t, source, config).WithRequestTrust(t, trialRequestSigners, requester.AllowedSigners)
	workflows := consumer.Workflows(t)

	for _, workflow := range []string{demoPullRequestWorkflow, developmentWorkflow, settings.Workflow} {
		if _, found := workflows[workflow]; !found {
			t.Fatalf("the consumer has no workflow %s, which a lifecycle runs", workflow)
		}
	}

	// The layer is the consumer trial's, as for its other events: a kaniko
	// job image is labelled for that repository, and a run admits it for no
	// other (livetest.ConsumerTrialLayer).
	scenario := layerScenario{target: target, label: livetest.RunnerLabel(t, target)}
	scenario.layer = newConsumerLayer(t, target, livetest.ConsumerTrial)
	scenario.repo = newConsumerRepo(t, target, consumer, name, private)
	repo := scenario.repo

	facts.Inputs["forge-ci-repository"] = scenario.layer.Repository

	plan, err := scenario.layer.PlanConsumer(workflows, facts)
	if err != nil {
		t.Fatal(err)
	}

	registry, err := livetest.RegistryHost(target)
	if err != nil {
		t.Fatal(err)
	}

	image := registry + "/" + strings.ToLower(livetest.RepoSlug(target, repo))

	t.Logf("consumer %s at %s (%s) names the layer at %s, binary %q; image builder %s", root, source.Commit, consumer.ObjectFormat, plan.Pin.Commit, plan.Pin.BinarySHA256, builder)
	t.Logf("layer under test: %s at %s, binary sha256 %s, runner label %q", scenario.layer.Repository, scenario.layer.Commit, scenario.layer.BinarySHA256, scenario.label)

	// Phase 1: the pull request.
	base := consumer.Push(t, repo, plan)
	requireObjectFormats(t, consumer.ObjectFormat, base, scenario.layer.Commit)

	head := consumer.PushChange(t, repo, lifecycleBranch, base)

	// The plan's bindings first: the service's development workflow takes the
	// layer from the forge-ci-repository fact, so the plan already names
	// FORGE_CI_REPOSITORY, as in every development trial. The trial's runtime
	// values must agree with it, then come the caller's. The commit the
	// release requests is not known before the merge; until then it is the base.
	runtime, err := livetest.MergeConsumerVariables(plan.Variables, map[string]string{
		layerRunnerVariable: runnerLabelsJSON(t, scenario.label), containerRunnerVariable: runnerLabelsJSON(t, containerLabel),
		"FORGE_CI_REPOSITORY": scenario.layer.Repository, layerRegistryUserVariable: registryUser(target, external),
		"SERVICE_IMAGE_REPOSITORY": image, "DEVELOPMENT_REGISTRY_USERNAME": target.CredentialUsername,
	})
	if err != nil {
		t.Fatal(err)
	}

	maps.Copy(runtime, livetest.ServiceSnapshotJobImageVariables(livetest.Context(t), t, consumer, settings.Variables))

	variables := trialVariables(t, target, runtime, settings.ResolvedVariables(scenario.layer.Commit, base))

	// Nothing has run so far: the pushes arrived with Actions off.
	livetest.EnableActions(t, target, repo)

	for variable, value := range variables {
		livetest.SetRepoVariable(t, target, repo, variable, value)
	}

	for secret, value := range callerSecrets(t, target, settings, plan.Secrets) {
		livetest.SetRepoSecret(t, target, repo, secret, value)
	}

	pull := livetest.OpenPullRequest(t, target, repo, lifecycleBranch, layerMain, "test: consumer lifecycle")
	requirePullRequest(t, pull, lifecycleBranch, head, base)

	review := livetest.WaitForConsumerRun(t, target, repo, demoPullRequestWorkflow, "pull_request", "")
	scenario.logJobs(t, livetest.RunJobs(t, target, repo, review.ID), facts.Secrets, plan.Secrets)
	logArtifacts(t, review, livetest.RunArtifacts(t, target, repo, review.ID))
	requireRunSuccess(t, review)
	requireRunIDs(t, target, repo, review.ID)
	scenario.requireNone(t, "/tags", "/releases")
	t.Logf("lifecycle phase 1 pull request: #%d, %s@%s into %s@%s, run %d", pull.Number, lifecycleBranch, head, layerMain, base, review.ID)

	// Phase 2: the native merge.
	merged := livetest.MergePullRequest(t, target, repo, pull.Number, head)
	request := merged.MergeCommitSHA
	requireMerge(t, scenario, request, base, head)
	requireRunIDs(t, target, repo, review.ID)
	t.Logf("lifecycle phase 2 merge: #%d merged natively, %s at %s with parents %s and %s", pull.Number, layerMain, request, base, head)

	// Phase 3: the service development snapshot on the merged main. The
	// release requests that main: a variable that names it is set again.
	previous := settings.ResolvedVariables(scenario.layer.Commit, base)
	for variable, value := range settings.ResolvedVariables(scenario.layer.Commit, request) {
		if value != previous[variable] {
			livetest.SetRepoVariable(t, target, repo, variable, value)
		}
	}

	livetest.SetRepoSecret(t, target, repo, "DEVELOPMENT_REGISTRY_TOKEN", target.Token)
	livetest.DispatchConsumerDevelopment(t, target, repo)

	development := livetest.WaitForConsumerRunAfter(t, target, repo, developmentWorkflow, "workflow_dispatch", review.ID)
	scenario.logJobs(t, livetest.RunJobs(t, target, repo, development.ID), facts.Secrets, plan.Secrets)
	receipts, producers := requireServiceDevelopment(t, target, repo, development, consumer, config, head, image)
	requireRunIDs(t, target, repo, review.ID, development.ID)
	scenario.requireNone(t, "/tags", "/releases")
	// The snapshot moves neither branch: main stays the merge, the source the
	// reviewed head.
	scenario.requireBranches(t, map[string]string{layerMain: request, lifecycleBranch: head})
	t.Logf("lifecycle phase 3 development: run %d dispatched on %s@%s snapshots %s@%s as %s; producers %d", development.ID, layerMain, request, lifecycleBranch, head, receipts[0].Ref, producers)

	// Phase 4: the signed release request at the merged main. The release's
	// authority is given now and not before.
	livetest.AddDeployKey(t, target, repo, "livetest release bot", keys.sshPublic)

	for secret, value := range plan.Secrets {
		livetest.SetRepoSecret(t, target, repo, secret, value)
	}

	release, released := requestRelease(t, scenario, requester, keys, settings, external, request, development.ID, facts.Secrets, plan.Secrets)
	t.Logf("lifecycle phase 4 release: v%s requested at %s, release commit %s, run %d", settings.Version, request, released, release.ID)

	// Phase 5: the repository holds the lifecycle and nothing else.
	scenario.requireBranches(t, map[string]string{layerMain: released, lifecycleBranch: head})
	requireRunIDs(t, target, repo, review.ID, development.ID, release.ID)

	if tags := livetest.RepoCount(t, target, repo, "/tags"); tags != 2 {
		t.Errorf("the repository has %d tags, want the request and the release", tags)
	}

	if releases := livetest.Releases(t, target, repo); len(releases) != 1 {
		t.Errorf("the repository has %d releases, want one", len(releases))
	}

	if deployKeys := livetest.RepoCount(t, target, repo, "/keys"); deployKeys != 1 {
		t.Errorf("the repository has %d deploy keys, want only the bot's", deployKeys)
	}

	t.Logf("lifecycle complete: base %s, change %s, merge %s, release %s; runs %d, %d, %d", base, head, request, released, review.ID, development.ID, release.ID)
}

// requireMerge checks what the native merge did: the forge's branches are at
// the merge commit and the change, and a mirror shows the merge commit to be
// one of the base the pull request was opened on and the change under
// review, with the change's tree.
func requireMerge(t *testing.T, scenario layerScenario, merge, base, head string) {
	t.Helper()

	scenario.requireBranches(t, map[string]string{layerMain: merge, lifecycleBranch: head})
	livetest.MirrorRepo(t, scenario.target, scenario.repo).RequireMergeOf(t, merge, base, head)
}
