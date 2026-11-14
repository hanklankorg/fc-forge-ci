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

// A snapshot on Forgejo: the lifecycle's development phase alone,
// over every image the planner plans from the consumer's declaration
// (docs/live-runs.md, Snapshot). The consumer's main and a change on
// branch development are pushed with Actions off; the harness's service
// development workflow is then dispatched on main and snapshots development
// through the snapshot-images policy. The trial requests that run and
// observes what it left: the frozen plan, every planned image's receipt and
// the registry's tags (requireServiceDevelopment), and a repository with
// that run, its two branches unmoved and nothing released; a snapshot
// probes no readiness. The consumer's own workflows are
// pinned to the layer and never run.
//
// A consumer whose declaration has no container is snapshotted for its file
// outputs instead: the workflow is given no image repository, registry user
// or registry secret and is asked for the files, and the trial observes the
// frozen plan and the bundle the run kept of every planned file output
// (requireSnapshotFiles), in a repository linked to no package. Where the
// consumer ships its own snapshot-files caller for its default declaration,
// that caller is planned and dispatched on development in place of the
// harness's workflow (livetest.ForgejoSnapshotRoute).
func TestForgejoLayer_Snapshot(t *testing.T) {
	livetest.RegisterLiveScenario(t)

	forEachConsumerCheckout(t, provider.ForgeForgejo, "just test-live-consumer forgejo <checkout> snapshot CI_JOB_IMAGE=<digest reference>", func(t *testing.T, root string) {
		t.Helper()

		if livetest.DemoName() != "" {
			t.Fatal("a snapshot runs in disposable scratch repositories, not in a kept demo")
		}

		for _, visibility := range livetest.ConsumerVisibilities(t) {
			t.Run(visibility.Name, func(t *testing.T) {
				runForgejoSnapshot(t, root, visibility.Private)
			})
		}
	}, livetest.TrialSnapshot)
}

func runForgejoSnapshot(t *testing.T, root string, private bool) {
	t.Helper()

	settings, err := livetest.ParseTrialSettings(os.Getenv(consumerEventEnv), consumerSettingsWords())
	if err != nil {
		t.Fatal(err)
	}

	if settings.Event != livetest.TrialSnapshot {
		t.Fatalf("the event is %s; this trial runs %s", settings.Event, livetest.TrialSnapshot)
	}

	builder, err := livetest.ForgejoConsumerImageBuilder(settings.Variables, os.Getenv(livetest.ForgejoImageBuilderEnv))
	if err != nil {
		t.Fatal(err)
	}

	target := livetest.Accept(t, provider.ForgeForgejo)
	if !livetest.Requires(t, provider.ForgeForgejo, livetest.NeedsInRunner, livetest.NeedsPackages) {
		livetest.NotCoveredf(t, "a snapshot needs a native runner and packages")
	}

	if livetest.RunnerArch(t, target) != "amd64" {
		t.Fatal("the snapshot requires native linux/amd64")
	}

	containerLabel := livetest.RunnerLabel(t, target)
	if builder == livetest.ImageBuilderBuildah {
		containerLabel = livetest.ContainerRunnerLabel(t, target, "the snapshot builds images with Buildah")
	}

	// The declaration the consumer's release builds its images from, unless
	// the trial names a variant of it (snapshot-declaration=).
	declaration := settings.SnapshotDeclaration
	if declaration == "" {
		if declaration, err = livetest.ServiceSnapshotSource(root); err != nil {
			t.Fatal(err)
		}
	}

	config := livetest.ServiceDevelopmentConfig{
		RecipeDirectory: root, Declaration: declaration, JobImage: settings.Variables["CI_JOB_IMAGE"], Branch: lifecycleBranch,
		Images: true,
	}

	const name = livetest.SnapshotScenario

	source := livetest.OpenConsumer(t, target, root)
	consumer := livetest.NewServiceDevelopmentFixture(t, source, config)

	// A declaration without a container plans file outputs alone, and its
	// snapshot is given no registry.
	files := len(livetest.SnapshotFileOutputs(livetest.Context(t), t, consumer)) != 0

	facts := livetest.ConsumerFacts{Inputs: map[string]string{}, Secrets: map[string]string{}}
	if !files {
		useLabRegistry(t, target, livetest.ScratchRepo(name), nil, &facts)
	}

	// The workflow that runs alone is planned, and the consumer's others are
	// pinned to the layer by the same plan: the demo's own snapshot-files
	// caller where it ships one for this snapshot, else the harness's.
	workflows := consumer.Workflows(t)
	workflow, ref, reads := livetest.ForgejoSnapshotRoute(workflows, files, declaration, lifecycleBranch)

	caller, found := workflows[workflow]
	if !found {
		t.Fatalf("the consumer has no workflow %s", workflow)
	}

	scenario := layerScenario{target: target, label: livetest.RunnerLabel(t, target)}
	scenario.layer = newConsumerLayer(t, target, livetest.ConsumerTrial)
	scenario.repo = newConsumerRepo(t, target, consumer, name, private)
	repo := scenario.repo

	facts.Inputs["forge-ci-repository"] = scenario.layer.Repository

	plan, err := scenario.layer.PlanConsumer(map[string]string{workflow: caller}, facts)
	if err != nil {
		t.Fatal(err)
	}

	registry, err := livetest.RegistryHost(target)
	if err != nil {
		t.Fatal(err)
	}

	image := registry + "/" + strings.ToLower(livetest.RepoSlug(target, repo))

	t.Logf("consumer %s at %s (%s) names the layer at %s; image builder %s; declaration %s; workflow %s on %s, reading %s", root, source.Commit, consumer.ObjectFormat, plan.Pin.Commit, builder, declaration, workflow, ref, reads)
	t.Logf("layer under test: %s at %s, binary sha256 %s, runner label %q", scenario.layer.Repository, scenario.layer.Commit, scenario.layer.BinarySHA256, scenario.label)

	base := consumer.Push(t, repo, plan)
	requireObjectFormats(t, consumer.ObjectFormat, base, scenario.layer.Commit)

	head := consumer.PushChange(t, repo, lifecycleBranch, base)

	trialRuntime := map[string]string{
		layerRunnerVariable: runnerLabelsJSON(t, scenario.label), containerRunnerVariable: runnerLabelsJSON(t, containerLabel),
		"FORGE_CI_REPOSITORY": scenario.layer.Repository,
	}
	if !files {
		trialRuntime["SERVICE_IMAGE_REPOSITORY"] = image
		trialRuntime["DEVELOPMENT_REGISTRY_USERNAME"] = target.CredentialUsername
	}

	runtime, err := livetest.MergeConsumerVariables(plan.Variables, trialRuntime)
	if err != nil {
		t.Fatal(err)
	}

	// Each producer family builds in its own image where the trial names
	// one, as CI_JOB_IMAGE_NODE, and in CI_JOB_IMAGE otherwise.
	maps.Copy(runtime, livetest.ServiceSnapshotJobImageVariables(livetest.Context(t), t, consumer, settings.Variables))

	// The pushes arrived with Actions off; nothing runs but the dispatch.
	livetest.EnableActions(t, target, repo)

	for variable, value := range trialVariables(t, target, runtime, settings.ResolvedVariables(scenario.layer.Commit, base)) {
		livetest.SetRepoVariable(t, target, repo, variable, value)
	}

	for secret, value := range callerSecrets(t, target, settings, plan.Secrets) {
		livetest.SetRepoSecret(t, target, repo, secret, value)
	}

	if !files {
		livetest.SetRepoSecret(t, target, repo, "DEVELOPMENT_REGISTRY_TOKEN", target.Token)
	}

	livetest.DispatchConsumerWorkflow(t, target, repo, workflow, ref)

	run := livetest.WaitForConsumerRun(t, target, repo, workflow, "workflow_dispatch", "")
	scenario.logJobs(t, livetest.RunJobs(t, target, repo, run.ID), facts.Secrets, plan.Secrets)

	if files {
		// The plan is judged against the declaration the dispatched workflow
		// reads, the demo's own on its caller's route.
		kept := requireSnapshotFiles(t, target, repo, run, workflow, livetest.ConsumerDeclaration(livetest.Context(t), t, consumer, reads), config, head)

		requireRunIDs(t, target, repo, run.ID)
		scenario.requireNone(t, "/tags", "/releases")
		scenario.requireBranches(t, map[string]string{layerMain: base, lifecycleBranch: head})
		requireNoRepoPackages(t, target, repo)

		t.Logf("snapshot: run %d of %s dispatched on %s snapshots %s@%s as the unsigned file bundles %s; no image, no package", run.ID, workflow, ref, lifecycleBranch, head, strings.Join(kept, " "))

		return
	}

	receipts, producers := requireServiceDevelopment(t, target, repo, run, consumer, config, head, image)

	requireRunIDs(t, target, repo, run.ID)
	scenario.requireNone(t, "/tags", "/releases")
	scenario.requireBranches(t, map[string]string{layerMain: base, lifecycleBranch: head})

	refs := make([]string, 0, len(receipts))
	for _, receipt := range receipts {
		refs = append(refs, receipt.Name+"="+receipt.Ref)
	}

	t.Logf("snapshot: run %d dispatched on %s@%s snapshots %s@%s as %s; producers %d, readiness false", run.ID, layerMain, base, lifecycleBranch, head, strings.Join(refs, " "), producers)
}

// requireNoRepoPackages holds the owner to no package of the repository: none
// linked to it, and none under its name, which is where a container image of
// it would be. Forgejo scopes packages to the owner, and other lanes publish
// there meanwhile, so the question is asked of this repository alone.
func requireNoRepoPackages(t *testing.T, target livetest.Target, repo string) {
	t.Helper()

	name := strings.ToLower(repo)

	for _, pkg := range livetest.OwnerPackages(t, target) {
		if pkg.Repository == repo || pkg.Name == name || strings.HasPrefix(pkg.Name, name+"/") {
			t.Errorf("the snapshot of files left package %s %s %s (repository %q), want none", pkg.Type, pkg.Name, pkg.Version, pkg.Repository)
		}
	}
}
