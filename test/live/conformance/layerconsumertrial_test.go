// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"cmp"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

// What `just test-live-consumer forgejo` hands over: the checkout, the event, and the
// caller's settings, one per line.
const (
	consumerCheckoutEnv = "FORGE_CI_LIVE_CONSUMER"
	consumerEventEnv    = "FORGE_CI_LIVE_CONSUMER_EVENT"
	consumerSettingsEnv = "FORGE_CI_LIVE_CONSUMER_SETTINGS"

	trialBranch         = "consumer-trial"
	trialRequestSigners = ".forgejo/release-allowed-signers"
)

// A consumer trial: a real consumer's own workflow, run on the lab against
// the layer of this checkout.
//
// It is not a scenario of the tier. It claims nothing about the layer that
// holds for every run, it is not part of a full run, and what it shows
// depends on the checkout it is given: that this consumer, as it is at HEAD,
// passes its own pull-request workflow, or completes its own release, with
// the layer under test in place of the one it pins.
//
// The consumer goes to main of one scratch repository and the layer to a
// branch of another, because a consumer's history may be SHA-256 and the
// layer's is SHA-1. The consumer's tree is rendered as PlanConsumer says, and
// nothing in the checkout is written.
//
// The verdict is generic. The run of the named workflow succeeded; every job
// is printed with its status, and the end of the log of each that failed.
// For a release, the host then checks what the layer promises of any
// release: main advanced by one commit signed by the bot's key, the final
// tag is signed and points at it, and the release, draft or published as the
// consumer asked, offers at least one Cosign bundle and each verifies against
// the trial's public key. Job names and asset counts are the consumer's.
func TestForgejoLayer_ConsumerTrial(t *testing.T) {
	livetest.RegisterLiveScenario(t)

	forEachConsumerCheckout(t, provider.ForgeForgejo, "just test-live-consumer forgejo <checkout> <pull-request|release>", func(t *testing.T, root string) {
		t.Helper()

		visibilities := livetest.ConsumerVisibilities(t)
		// The npm registry belongs to the owner, not to the scratch repository, so
		// a second case would publish the same name@version again and be refused.
		if os.Getenv("FORGE_CI_LIVE_NPM_APPLICATION_IMAGE") != "" && len(visibilities) != 1 {
			t.Fatal("npm application acceptance publishes one package version to the owner's registry; set FORGE_CI_LIVE_CONSUMER_VISIBILITY to public or private")
		}

		for _, visibility := range visibilities {
			t.Run(visibility.Name, func(t *testing.T) {
				runForgejoConsumerTrial(t, root, forgejoConsumerRelease, visibility.Private)
			})
		}
	}, livetest.TrialPullRequest, livetest.TrialRelease)
}

// consumerSettingsWords reads the caller's settings, one per line so a
// variable's value may hold a space.
func consumerSettingsWords() []string {
	var words []string

	for word := range strings.SplitSeq(os.Getenv(consumerSettingsEnv), "\n") {
		if word != "" {
			words = append(words, word)
		}
	}

	return words
}

// forgejoReleaseCase is what a consumer trial does with its release: the
// trial's own acceptance, or one of the scenarios that expect another end.
type forgejoReleaseCase int

const (
	forgejoConsumerRelease forgejoReleaseCase = iota
	forgejoPreseededProvenanceRelease
	forgejoInterruptedRecoveryRelease
	forgejoRerunAfterInterruptionRelease
)

func runForgejoConsumerTrial(t *testing.T, root string, releaseCase forgejoReleaseCase, private bool) {
	t.Helper()

	settings, err := livetest.ParseTrialSettings(os.Getenv(consumerEventEnv), consumerSettingsWords())
	if err != nil {
		t.Fatal(err)
	}

	requireForgejoReleaseCase(t, releaseCase, settings)

	trialBuilder, err := livetest.ForgejoConsumerImageBuilder(settings.Variables, os.Getenv(livetest.ForgejoImageBuilderEnv))
	if err != nil {
		t.Fatal(err)
	}

	target := livetest.Accept(t, provider.ForgeForgejo)
	if !livetest.Requires(t, provider.ForgeForgejo, livetest.NeedsInRunner) {
		t.Skip("no Forgejo runner in this environment, so the consumer's workflow cannot run")
	}

	consumer := livetest.OpenConsumer(t, target, root)
	if image := os.Getenv("FORGE_CI_LIVE_NPM_APPLICATION_IMAGE"); image != "" {
		if livetest.RunnerArch(t, target) != "amd64" || livetest.DemoName() != "" {
			t.Fatal("npm application acceptance requires disposable native amd64 repositories")
		}

		recipe, err := filepath.Abs("../../../test/live/fixtures/consumers/hello-npm-service/consumer")
		if err != nil {
			t.Fatal(err)
		}

		consumer = livetest.NewNPMApplicationFixture(t, consumer, livetest.NPMApplicationFixtureConfig{RecipeDirectory: recipe, NodeImage: image, Oracle: os.Getenv("FORGE_CI_LIVE_NPM_APPLICATION_ORACLE"), Library: os.Getenv("FORGE_CI_LIVE_NPM_LIBRARY") == "true"}, false)
	}

	if image := os.Getenv("FORGE_CI_LIVE_MAVEN_APPLICATION_IMAGE"); image != "" {
		recipe, err := filepath.Abs("../../../test/live/fixtures/consumers/hello-maven/consumer")
		if err != nil {
			t.Fatal(err)
		}

		consumer = livetest.NewMavenApplicationConsumerFixture(t, consumer, recipe, image)
	}

	if image := os.Getenv("FORGE_CI_LIVE_MAVEN_TESTCONTAINERS_IMAGE"); image != "" {
		if settings.Event != livetest.TrialRelease || livetest.RunnerArch(t, target) != "amd64" || livetest.DemoName() != "" || livetest.PrivilegedRunnerLabel(t, target) == "" || os.Getenv("FORGE_CI_LIVE_MAVEN_APPLICATION_IMAGE") == "" {
			t.Fatal("Maven Testcontainers acceptance requires a disposable native amd64 Maven application fixture and acknowledged privileged runner capacity")
		}

		consumer = livetest.WithMavenContainerTests(t, consumer, image, livetest.PrivilegedRunnerLabel(t, target))
	}

	// The consumer's own fixtures, then a scenario's, go into the private
	// index before the workflows are read, so the plan binds the workflow
	// that runs.
	if settings.Fixtures != "" {
		consumer = livetest.WithForgejoFixtures(t, consumer, settings.Fixtures)

		// The host reads the consumer's workflow, declaration and projects
		// where the checkout is named. From here that is the consumer as
		// staged, so every such reader plans the release that runs.
		staged := t.TempDir()
		livetest.ForgejoTrialRoot(t, consumer, staged)
		t.Setenv(consumerCheckoutEnv, staged)
	}

	switch releaseCase {
	case forgejoPreseededProvenanceRelease:
		consumer = livetest.WithPreseededForgejoProvenance(t, consumer)
	case forgejoInterruptedRecoveryRelease, forgejoRerunAfterInterruptionRelease:
		consumer = livetest.WithForgejoRecoveryCheckpoint(t, consumer)
	case forgejoConsumerRelease:
	}

	workflows := consumer.Workflows(t)
	if _, found := workflows[settings.Workflow]; !found {
		t.Fatalf("the consumer has no workflow %s; name the one to judge with workflow=<file>", settings.Workflow)
	}

	const name = livetest.ConsumerTrial

	// A demo shows the whole flow in one repository it keeps: the release,
	// then a pull request on the released main.
	demo := livetest.DemoName()
	repoName := livetest.ScratchRepo(name)

	if demo != "" {
		requireDemoReady(t, settings, workflows)

		repoName = livetest.DemoRepo(demo)
		consumer = consumer.WithHistory()
	}

	release := settings.Event == livetest.TrialRelease
	facts := livetest.ConsumerFacts{Inputs: map[string]string{}, Secrets: map[string]string{}}

	var (
		keys      releaseKeys
		requester livetest.RequestSigner
	)

	external := externalRegistry(t, settings)
	if settings.ReleaseImagesConfig != "" && external != nil {
		t.Fatal("release-images-config readback requires the accepted local registry, not an external registry")
	}

	if release {
		keys = trialReleaseFacts(t, target, repoName, &facts)
		requester = livetest.NewRequestSigner(t, t.TempDir())
		useExternalRegistry(t, external, &facts)
		useLabRegistry(t, target, repoName, external, &facts)
	}

	scenario := layerScenario{target: target, label: livetest.RunnerLabel(t, target)}
	scenario.layer, scenario.repo = newConsumerRepos(t, target, consumer, name, private)
	repo := scenario.repo

	facts.Inputs["forge-ci-repository"] = scenario.layer.Repository

	plan, err := scenario.layer.PlanConsumer(workflows, facts)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("consumer %s at %s (%s) names the layer at %s, binary %q", root, consumer.Commit, consumer.ObjectFormat, plan.Pin.Commit, plan.Pin.BinarySHA256)
	t.Logf("layer under test: %s at %s, binary sha256 %s, runner label %q",
		scenario.layer.Repository, scenario.layer.Commit, scenario.layer.BinarySHA256, scenario.label)

	main := consumer.Push(t, repo, plan)
	if release {
		// Install only the disposable requester's public trust material while
		// Actions are still disabled. The private key stays on the host.
		main = livetest.CommitFiles(t, target, repo, livetest.FileCommit{
			Branch: layerMain, Message: "test: trust the disposable release requester",
			Files: map[string]string{trialRequestSigners: requester.AllowedSigners},
		})
	}

	requireObjectFormats(t, consumer.ObjectFormat, main, scenario.layer.Commit)

	head := ""
	if !release {
		head = consumer.PushChange(t, repo, trialBranch, main)
	}

	// The layer's own variables, then the consumer's names for the lab's
	// facts, then the caller's. One name has one source.
	variables := map[string]string{layerRunnerVariable: runnerLabelsJSON(t, scenario.label), "FORGE_CI_REPOSITORY": scenario.layer.Repository}
	if os.Getenv("FORGE_CI_LIVE_NPM_APPLICATION_IMAGE") != "" {
		variables["NPM_APPLICATION_REGISTRY"] = strings.TrimRight(target.BaseURL(), "/") + "/api/packages/" + target.Owner + "/npm/"
	}

	// Buildah's image builds go to the contract's runner that permits user
	// namespaces; kaniko's stay on the ordinary runner, which may deny them.
	if privileged := livetest.PrivilegedRunnerLabel(t, target); privileged != "" && trialBuilder != livetest.ImageBuilderKaniko {
		variables[containerRunnerVariable] = runnerLabelsJSON(t, privileged)
	}

	if release {
		variables[layerRegistryUserVariable] = registryUser(target, external)
	}

	variables = trialVariables(t, target, variables, plan.Variables, settings.ResolvedVariables(scenario.layer.Commit, main),
		livetest.ForgejoReleaseOptionVariables(settings.ReleaseOption))
	maps.Copy(plan.Secrets, callerSecrets(t, target, settings, plan.Secrets))

	if release {
		livetest.AddDeployKey(t, target, repo, "livetest release bot", keys.sshPublic)
	}

	// Nothing has run so far: the pushes arrived with Actions off.
	livetest.EnableActions(t, target, repo)

	for variable, value := range variables {
		livetest.SetRepoVariable(t, target, repo, variable, value)
	}

	// A kept demo holds no credential once the run is over.
	if demo != "" {
		livetest.DeleteSecretsAfter(t, target, repo, slices.Sorted(maps.Keys(plan.Secrets)))
	}

	for secret, value := range plan.Secrets {
		livetest.SetRepoSecret(t, target, repo, secret, value)
	}

	if !release {
		requirePullRequest(t, livetest.OpenPullRequest(t, target, repo, trialBranch, layerMain, "test: consumer trial"), trialBranch, head, main)

		run := livetest.WaitForConsumerRun(t, target, repo, settings.Workflow, "pull_request", "")
		scenario.logJobs(t, livetest.RunJobs(t, target, repo, run.ID), facts.Secrets, plan.Secrets)
		logArtifacts(t, run, livetest.RunArtifacts(t, target, repo, run.ID))
		requireRunSuccess(t, run)
	} else if releaseCase == forgejoPreseededProvenanceRelease {
		requireForgejoPreseededProvenanceRefusal(t, scenario, requester, keys, settings, main, facts.Secrets, plan.Secrets)

		return
	} else if releaseCase == forgejoInterruptedRecoveryRelease {
		recoverForgejoReleaseAfterHandoff(t, scenario, requester, keys, settings, external, main, facts.Secrets, plan.Secrets)

		return
	} else if releaseCase == forgejoRerunAfterInterruptionRelease {
		rerunForgejoReleaseAfterHandoff(t, scenario, requester, keys, settings, external, main, workflows[settings.Workflow], facts.Secrets, plan.Secrets)

		return
	} else if run, released := requestRelease(t, scenario, requester, keys, settings, external, main, 0, facts.Secrets, plan.Secrets); released == "" {
		// Interrupted: what the cancelled run left is shown by the next request.
		return
	} else if settings.NextVersion != "" {
		_, npmErr := os.Stat(filepath.Join(root, "package.json"))
		if npmErr != nil && !os.IsNotExist(npmErr) {
			t.Fatal(npmErr)
		}

		if npmErr == nil {
			requireNPMVersionContract(t, target, repo, "v"+settings.Version)
		}

		first := "v" + settings.Version
		before := assetNames(releaseOfTag(t, target, repo, first))
		parent := nextReleaseCandidate(t, scenario, settings, main)

		settings.Version = settings.NextVersion
		next, _ := requestRelease(t, scenario, requester, keys, settings, external, parent, run.ID, facts.Secrets, plan.Secrets)

		if npmErr == nil {
			requireNPMVersionContract(t, target, repo, "v"+settings.Version)
		}

		t.Logf("second consecutive Forgejo release v%s: run %d, same repository and signing authority", settings.Version, next.ID)

		if settings.ReleaseOption == livetest.OptionRetention {
			after := assetNames(releaseOfTag(t, target, repo, first))
			for _, finding := range livetest.ReleaseRetentionFindings(before, after, assetNames(releaseOfTag(t, target, repo, "v"+settings.Version))) {
				t.Error(finding)
			}

			t.Logf("retention: %s keeps %d of %d files", first, len(after), len(before))
		}
	}

	if demo != "" {
		runDemoPullRequest(t, scenario, consumer, main, facts.Secrets, plan.Secrets)
	}
}

// nextReleaseCandidate puts an ordinary commit on main, the candidate of the
// next release, and returns it. The generated release commit carries
// [skip ci], and the earlier release and its tags are not rewritten. The
// variables that follow main are set for the candidate; previous is the
// commit they were set for.
func nextReleaseCandidate(t *testing.T, scenario layerScenario, settings livetest.TrialSettings, previous string) string {
	t.Helper()

	target, repo := scenario.target, scenario.repo
	candidate := livetest.CommitFiles(t, target, repo, livetest.FileCommit{
		Branch: layerMain, Message: "test: prepare next release candidate",
		Files: map[string]string{"next-release-request.txt": "request the next release\n"},
	})

	before := settings.ResolvedVariables(scenario.layer.Commit, previous)
	for variable, value := range settings.ResolvedVariables(scenario.layer.Commit, candidate) {
		if value != before[variable] {
			livetest.SetRepoVariable(t, target, repo, variable, value)
		}
	}

	return candidate
}

// requestRelease pushes a signed request for settings.Version at request, the
// head of main, waits for the release run (the repository's first, or the one
// after afterID), logs it and judges the release: the run succeeded, main
// advanced by one signed release commit, the tags, the signed release, the
// packages its workflow publishes to the owner's registries, its distro
// packages' signatures and the external images. It returns the run and the
// release commit. A run the
// settings interrupt is checked to have released nothing, and its release
// commit is "".
func requestRelease(t *testing.T, scenario layerScenario, requester livetest.RequestSigner, keys releaseKeys, settings livetest.TrialSettings, external *livetest.ExternalRegistry, request string, afterID int64, secrets ...map[string]string) (livetest.WorkflowRun, string) {
	t.Helper()

	target, repo := scenario.target, scenario.repo
	requestTag, finalTag := "release-request/v"+settings.Version, "v"+settings.Version
	packages := awaitedPackages(t, target, repo, settings)
	distro := awaitedDistroPackages(t, target, repo, settings)
	requester.PushTag(t, target, repo, requestTag, layerMain, "Request release "+finalTag)

	var run livetest.WorkflowRun
	if afterID == 0 {
		run = livetest.WaitForConsumerRun(t, target, repo, settings.Workflow, "push", settings.InterruptAfter)
	} else {
		run = livetest.WaitForConsumerRunAfter(t, target, repo, settings.Workflow, "push", afterID)
	}

	// Before the run is judged, so a run that published and then failed
	// leaves no package behind.
	distro = publishedDistroPackages(t, target, repo, distro)

	scenario.logJobs(t, livetest.RunJobs(t, target, repo, run.ID), secrets...)
	logArtifacts(t, run, livetest.RunArtifacts(t, target, repo, run.ID))

	if settings.InterruptAfter != "" {
		requireInterrupted(t, target, repo, run, finalTag)

		return run, ""
	}

	return run, requireReleased(t, scenario, keys, settings, external, request, run, packages, distro)
}

// requireReleased judges a release run that is over: it succeeded, main
// advanced by one signed release commit, the tags, the signed release, the
// npm packages named, the distro packages (distro holds what it published of
// each) and the images. It returns the release commit.
func requireReleased(t *testing.T, scenario layerScenario, keys releaseKeys, settings livetest.TrialSettings, external *livetest.ExternalRegistry, request string, run livetest.WorkflowRun, packages []string, distro []distroPackage) string {
	t.Helper()

	target, repo := scenario.target, scenario.repo
	requestTag, finalTag := "release-request/v"+settings.Version, "v"+settings.Version

	requireRunSuccess(t, run)

	released := requireReleaseGit(t, target, repo, keys, request, requestTag, finalTag)
	requireReleaseCommitScope(t, target, repo, request, released, settings)

	gpgFiles, err := livetest.ForgejoSignsReleaseFilesWithGPG(os.Getenv(consumerCheckoutEnv), settings.Workflow)
	if err != nil {
		t.Fatalf("release file signing: %v", err)
	}

	requireSignedRelease(t, target, repo, keys, finalTag, request, gpgFiles)

	// Draft or published is the consumer's choice, unless the trial gave
	// the release an option: it is then that option's draft or prerelease.
	if settings.ReleaseOption != "" {
		release := releaseOfTag(t, target, repo, finalTag)
		for _, finding := range livetest.ReleaseOptionFindings(settings.ReleaseOption, release.Draft, release.Prerelease) {
			t.Error(finding)
		}
	}

	for _, name := range packages {
		if listed := livetest.PublishedPackageVersions(t, target, repo, "npm", name); !slices.Contains(listed, settings.Version) {
			t.Errorf("the release run succeeded, but the owner's package registry lists %v of %s and not %s", listed, name, settings.Version)
		}
	}

	requireForgejoDistroPackages(t, target, repo, keys, settings, finalTag, distro)

	if settings.ReleaseImagesConfig != "" {
		plan := requireLocalReleaseImageOutputs(t, target, repo, run.ID, released, keys, settings)
		requireImagesCarryReleasedFiles(t, target, repo, run.ID, released, finalTag, plan)
	}

	requireBaseImageInventory(t, target, repo, released, finalTag, keys)
	requireExternalImages(t, external, keys, settings)

	return released
}

// awaitedPackages are the npm packages the consumer's release workflow
// publishes to the owner's package registry, before the release is requested:
// none may hold the version yet, since finding it afterwards would then show
// nothing. The registry is the owner's and outlives the repository, so the
// version is removed when the test ends. A kept demo keeps its package.
func awaitedPackages(t *testing.T, target livetest.Target, repo string, settings livetest.TrialSettings) []string {
	t.Helper()

	packages, err := livetest.ForgejoPlannedNPMPackages(os.Getenv(consumerCheckoutEnv), settings.Workflow)
	if err != nil {
		t.Fatalf("planned packages: %v", err)
	}

	for _, name := range packages {
		if listed := livetest.PublishedPackageVersions(t, target, repo, "npm", name); slices.Contains(listed, settings.Version) {
			t.Fatalf("the owner's package registry already holds %s@%s, which a release cannot publish again; remove it or request another version", name, settings.Version)
		}

		if livetest.DemoName() == "" {
			t.Cleanup(func() { livetest.DeletePublishedPackage(t, target, repo, "npm", name, settings.Version) })
		}
	}

	return packages
}

// requireReleaseCommitScope reads from a mirror what the release commit
// changed since its request and holds it to the release plan of the consumer's
// own declaration: the changelog, which gains the released version, and the
// version files of the projects the plan bumps.
func requireReleaseCommitScope(t *testing.T, target livetest.Target, repo, request, released string, settings livetest.TrialSettings) {
	t.Helper()

	planned, err := livetest.ForgejoPlannedVersionBumps(os.Getenv(consumerCheckoutEnv), settings.Workflow)
	if err != nil {
		t.Fatalf("release commit scope: %v", err)
	}

	bumps := make([]releaseBump, 0, len(planned))
	for _, item := range planned {
		bumps = append(bumps, releaseBump{projectType: item.ProjectType, dir: path.Clean(cmp.Or(item.WorkingDirectory, "."))})
	}

	mirror := livetest.MirrorRepo(t, target, repo)

	changed, err := mirror.Git(t, "diff-tree", "--no-commit-id", "--name-only", "-r", request, released)
	if err != nil {
		t.Fatalf("release commit scope: %v", err)
	}

	requested := func(file string) bool {
		_, err := mirror.Git(t, "cat-file", "-e", request+":"+file)

		return err == nil
	}

	for _, finding := range releaseCommitScopeFindings(strings.Fields(changed), bumps, requested) {
		t.Error(finding)
	}

	// A first release may have no changelog to start from.
	before, _ := mirror.Git(t, "show", request+":"+releaseChangelog)

	after, err := mirror.Git(t, "show", released+":"+releaseChangelog)
	if err != nil {
		t.Fatalf("release commit scope: %v", err)
	}

	for _, finding := range releaseChangelogFindings(before, after, settings.Version) {
		t.Error(finding)
	}
}

// trialVariables adds the layer's and the caller's variables to the trial's
// own and binds the release platforms to the lab's runners. One name has one
// source.
func trialVariables(t *testing.T, target livetest.Target, variables map[string]string, more ...map[string]string) map[string]string {
	t.Helper()

	for _, source := range more {
		for variable, value := range source {
			if _, set := variables[variable]; set {
				t.Fatalf("variable %s is set by the trial itself, from the contract or the layer under test, so it cannot be given a second value", variable)
			}

			variables[variable] = value
		}
	}

	// The consumer builds for the lab's runners, and only for them.
	variables, err := livetest.NativeReleasePlatforms(variables, livetest.RunnerArch(t, target))
	if err != nil {
		t.Fatal(err)
	}

	return variables
}

// callerSecrets is the secrets the caller asked for: the lab account's token
// under the names of token=, and the caller's own files under those of
// secret-file=. None may be one the trial sets itself, from the consumer's own
// workflow.
func callerSecrets(t *testing.T, target livetest.Target, settings livetest.TrialSettings, reserved map[string]string) map[string]string {
	t.Helper()

	secrets := map[string]string{}

	for _, secret := range settings.TokenSecrets {
		if _, set := reserved[secret]; set {
			t.Fatalf("secret %s is set by the trial itself, from the consumer's own workflow; leave it out", secret)
		}

		secrets[secret] = target.Token
	}

	for _, secret := range slices.Sorted(maps.Keys(settings.FileSecrets)) {
		if _, set := reserved[secret]; set {
			t.Fatalf("secret %s is set by the trial itself, from the consumer's own workflow; leave it out", secret)
		}

		secrets[secret] = livetest.ReadSecretFile(t, secret, settings.FileSecrets[secret])
	}

	return secrets
}

// newConsumerRepos makes the layer's and the consumer's repositories: fixed
// scratch ones a trial resets and deletes, or in demo mode kept ones under the
// demo's name that nothing deletes.
func newConsumerRepos(t *testing.T, target livetest.Target, consumer livetest.Consumer, name string, private bool) (livetest.Layer, string) {
	t.Helper()

	if demo := livetest.DemoName(); demo != "" {
		layer := livetest.PushLayer(t, target, livetest.NewDemoRepo(t, target, livetest.DemoLayerRepo(demo), "sha1", true))

		return layer, livetest.NewDemoRepo(t, target, livetest.DemoRepo(demo), consumer.ObjectFormat, false)
	}

	return newConsumerLayer(t, target, name), newConsumerRepo(t, target, consumer, name, private)
}

// newConsumerLayer serves the layer under test from the scratch repository
// of LayerScenario(name), public as every engine layer is.
func newConsumerLayer(t *testing.T, target livetest.Target, name string) livetest.Layer {
	t.Helper()

	return livetest.PushLayer(t, target, livetest.NewPublicScratchRepo(t, target, livetest.LayerScenario(name), livetest.ReadLayerAnonymously))
}

// newConsumerRepo makes the consumer's scratch repository, in the consumer's
// object format and the run's visibility.
func newConsumerRepo(t *testing.T, target livetest.Target, consumer livetest.Consumer, name string, private bool) string {
	t.Helper()

	repo := livetest.NewBareScratchRepo(t, target, name, consumer.ObjectFormat, private)

	// A consumer's release publishes under names of its own choosing.
	livetest.SweepPackagesAfter(t, target, repo)

	return repo
}

// requireDemoReady refuses a demo that could not show both halves.
func requireDemoReady(t *testing.T, settings livetest.TrialSettings, workflows map[string]string) {
	t.Helper()

	if settings.Event != livetest.TrialRelease {
		t.Fatalf("a demo runs the release and then a pull request; start it with the release event")
	}

	if _, found := workflows[demoPullRequestWorkflow]; !found {
		t.Fatalf("a demo also opens a pull request, and the consumer has no workflow %s", demoPullRequestWorkflow)
	}
}

// demoPullRequestWorkflow is the workflow a demo's pull request is judged by.
const demoPullRequestWorkflow = "pullrequest.yml"

// runDemoPullRequest opens a demo's pull request into the main its release
// left, and requires the consumer's own pull-request workflow to pass on it.
// The change is one commit on pushed, the commit whose tree the trial staged:
// on the released main it would carry that tree and undo the release commit.
func runDemoPullRequest(t *testing.T, scenario layerScenario, consumer livetest.Consumer, pushed string, secrets ...map[string]string) {
	t.Helper()

	target, repo := scenario.target, scenario.repo
	released := livetest.BranchCommitSHA(t, target, repo, layerMain)
	head := consumer.PushChange(t, repo, trialBranch, pushed)

	requirePullRequest(t, livetest.OpenPullRequest(t, target, repo, trialBranch, layerMain, "test: consumer trial"), trialBranch, head, released)

	run := livetest.WaitForConsumerRun(t, target, repo, demoPullRequestWorkflow, "pull_request", "")
	scenario.logJobs(t, livetest.RunJobs(t, target, repo, run.ID), secrets...)
	requireRunSuccess(t, run)
}

// logArtifacts records what the run left in the forge's artifact store, which
// counts against the owner's quota until the artifacts expire: with the
// release's own size, what a release costs the forge.
func logArtifacts(t *testing.T, run livetest.WorkflowRun, artifacts []livetest.RunArtifact) {
	t.Helper()

	var size int64

	for _, artifact := range artifacts {
		size += artifact.SizeInBytes
	}

	t.Logf("run %d left %d artifacts (%.1f MB) in the forge's store", run.ID, len(artifacts), float64(size)/1e6)
}

// requireInterrupted checks a run the trial cancelled: it ended cancelled,
// not failed before the interrupt, and published no release. What it left on
// an external registry is shown by requesting the same version again, which
// must then succeed.
func requireInterrupted(t *testing.T, target livetest.Target, repo string, run livetest.WorkflowRun, tag string) {
	t.Helper()

	if run.Status != runCancelled {
		t.Fatalf("run %d of %s ended %q, want %s: it was to be interrupted", run.ID, run.WorkflowID, run.Status, runCancelled)
	}

	for _, release := range livetest.Releases(t, target, repo) {
		if release.TagName == tag {
			t.Fatalf("the interrupted run published release %s", tag)
		}
	}

	t.Logf("run %d was interrupted and published no release %s; request the same version again to see the rerun", run.ID, tag)
}

// externalRegistry is the external image registry's credential the caller
// gave, or nil. An image= setting needs one.
func externalRegistry(t *testing.T, settings livetest.TrialSettings) *livetest.ExternalRegistry {
	t.Helper()

	external, found := livetest.ExternalRegistryCredential(t)
	if !found {
		if len(settings.Images) > 0 {
			t.Fatal("image= verifies an image on an external registry; give its token with FORGE_CI_LIVE_EXTERNAL_REGISTRY_TOKEN_FILE and FORGE_CI_LIVE_EXTERNAL_REGISTRY_USER")
		}

		return nil
	}

	return &external
}

// useExternalRegistry makes the consumer's registry secret, which every image
// step of the layer takes, the external registry's token instead of the lab's.
func useExternalRegistry(t *testing.T, external *livetest.ExternalRegistry, facts *livetest.ConsumerFacts) {
	t.Helper()

	if external == nil {
		return
	}

	facts.Secrets["RELEASE_REGISTRY_TOKEN"] = external.Token

	t.Logf("images go to an external registry as %s, with the token from FORGE_CI_LIVE_EXTERNAL_REGISTRY_TOKEN_FILE", external.User)
}

// layerRegistryUserVariable is the user of the layer's RELEASE_REGISTRY_TOKEN
// secret, which the layer reads when a consumer passes no registry-user.
const layerRegistryUserVariable = "RELEASE_REGISTRY_USER"

// registryUser is the user the trial's registry token belongs to: the
// external registry's, or the scratch repository's owner on the lab's own.
func registryUser(target livetest.Target, external *livetest.ExternalRegistry) string {
	if external != nil {
		return external.User
	}

	return target.Owner
}

// useLabRegistry publishes the images to the lab's own registry wherever the
// consumer names its repository: a literal such as ghcr.io/owner/app is
// replaced like a variable, so a trial never writes to the consumer's real
// registry. The user is RELEASE_REGISTRY_USER alone, which the layer reads
// when the consumer's own registry-user is empty in the lab. An external
// registry keeps the consumer's own names.
func useLabRegistry(t *testing.T, target livetest.Target, repo string, external *livetest.ExternalRegistry, facts *livetest.ConsumerFacts) {
	t.Helper()

	if external != nil {
		return
	}

	registry, err := livetest.RegistryHost(target)
	if err != nil {
		t.Fatal(err)
	}

	facts.Inputs["image-repository"] = strings.ToLower(registry + "/" + target.Owner + "/" + repo)
	facts.Inputs["publication-image-repository"] = facts.Inputs["image-repository"]
	facts.Inputs["base-repository"] = facts.Inputs["image-repository"] + "-base"
}

// requireExternalImages verifies each image= against the trial's key on the
// external registry, with that registry's own login.
func requireExternalImages(t *testing.T, external *livetest.ExternalRegistry, keys releaseKeys, settings livetest.TrialSettings) {
	t.Helper()

	for _, image := range settings.Images {
		reference := livetest.TrialImageReference(image, settings.Version)
		if err := livetest.VerifyExternalImage(t, *external, keys.cosignPublicPath, reference); err != nil {
			t.Errorf("the promoted image %s does not verify against the trial's key: %v", reference, err)

			continue
		}

		t.Logf("image %s verifies against the trial's key", reference)
	}
}

// trialReleaseFacts makes the keys of one release and names them as the
// layer's inputs and secrets. It skips where a release cannot run at all.
func trialReleaseFacts(t *testing.T, target livetest.Target, repo string, facts *livetest.ConsumerFacts) releaseKeys {
	t.Helper()

	signer, ok := livetest.SignerImage(t)
	if !ok {
		t.Skip("no signer image, so the signer jobs cannot run")
	}

	gitURL, fingerprint, ok := livetest.SSHRoute(t, target, repo)
	if !ok {
		t.Skip("no SSH route, so the release commit cannot be pushed")
	}

	keys := newReleaseKeys(t)
	gpgPrivate, gpgFingerprint := livetest.GPGKey(t, keys.dir, keys.passphrase)
	keys.gpgFingerprint = gpgFingerprint

	facts.Inputs = map[string]string{
		"signer-image":                     signer,
		"cosign-public-key":                keys.cosignPublic,
		"cosign-public-key-sha256":         keys.cosignPublicSHA256,
		"release-git-url":                  gitURL,
		"release-git-host-key-fingerprint": fingerprint,
		"release-request-allowed-signers":  trialRequestSigners,
	}
	facts.Secrets = map[string]string{
		"RELEASE_SSH_SIGNING_KEY":    keys.sshPrivate,
		"RELEASE_COSIGN_PRIVATE_KEY": keys.cosignPrivate,
		"RELEASE_COSIGN_PASSPHRASE":  keys.passphrase,
		"RELEASE_GPG_PRIVATE_KEY":    gpgPrivate,
		"RELEASE_GPG_FINGERPRINT":    gpgFingerprint,
		"RELEASE_GPG_PASSPHRASE":     keys.passphrase,
		"RELEASE_TOKEN":              target.Token,
		"RELEASE_REGISTRY_TOKEN":     target.Token,
	}

	return keys
}

// releaseOfTag is the repository's one release of the tag, a draft too.
func releaseOfTag(t *testing.T, target livetest.Target, repo, tag string) livetest.Release {
	t.Helper()

	var found []livetest.Release

	releases := livetest.Releases(t, target, repo)
	for _, release := range releases {
		if release.TagName == tag {
			found = append(found, release)
		}
	}

	if len(found) != 1 {
		t.Fatalf("the repository has %d releases of %s among %+v, want one", len(found), tag, releases)
	}

	return found[0]
}

// assetNames are the names of the files a release offers.
func assetNames(release livetest.Release) []string {
	names := make([]string, 0, len(release.Assets))
	for _, asset := range release.Assets {
		names = append(names, asset.Name)
	}

	return names
}

// requireSignedRelease holds the repository to one release of the tag whose
// Cosign bundles each sign a file the release offers and verify against the
// trial's public key, and whose OpenPGP signatures each verify as the trial's
// GPG key's. A release whose declaration signs its files with GPG (gpgFiles)
// signs its checksums so and gives no file it signs with GPG a Cosign
// bundle. Draft or published is the consumer's choice.
func requireSignedRelease(t *testing.T, target livetest.Target, repo string, keys releaseKeys, tag, source string, gpgFiles bool) {
	t.Helper()

	release := releaseOfTag(t, target, repo, tag)
	assets := make(map[string]string, len(release.Assets))

	var size int64

	for _, asset := range release.Assets {
		assets[asset.Name] = asset.URL
		size += asset.Size
	}

	names := assetNames(release)

	evidence, err := livetest.NewReleaseEvidence(target, repo, tag, source, names, releaseEvidenceSecrets(keys))
	if err != nil {
		t.Fatal("retain release inventory: ", err)
	}

	verified, err := livetest.VerifyReleaseReadback(evidence, names, func(name string) ([]byte, error) {
		return livetest.Download(t, target, assets[name], maxReleaseFile), nil
	}, func(blob, bundle []byte) error {
		return livetest.VerifyBlob(t, target, keys.cosignPublicPath, blob, bundle)
	}, func(blob, signature []byte) error {
		return livetest.VerifyOpenPGPSignature(livetest.Context(t), filepath.Join(keys.dir, "gnupg"), livetest.ToolEnvironment(t, target, nil), keys.gpgFingerprint, blob, signature)
	})
	if err != nil {
		t.Error(err)
	}

	if gpgFiles {
		for _, finding := range livetest.GPGReleaseFileFindings(names) {
			t.Error(finding)
		}
	}

	if len(verified) == 0 {
		t.Errorf("the release of %s offers no signature that verifies, among %d files", tag, len(assets))
	}

	// The size is what one release adds to the owner's storage quota; the
	// trial deletes its repository afterwards, so it is measured here.
	t.Logf("release %s: draft %t, %d files (%.1f MB), %d files whose signature verified with the trial's keys, GPG-signed files %t",
		tag, release.Draft, len(assets), float64(size)/1e6, len(verified), gpgFiles)
}

func releaseEvidenceSecrets(keys releaseKeys) map[string]string {
	return map[string]string{"ssh": keys.sshPrivate, "cosign": keys.cosignPrivate, "passphrase": keys.passphrase}
}
