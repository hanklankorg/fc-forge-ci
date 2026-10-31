// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/livetest"
)

// What `just test-live-forgejo-consumer` hands over: the checkout, the event, and the
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
	root := os.Getenv(consumerCheckoutEnv)
	if root == "" {
		t.Skip("no consumer checkout is named; run `just test-live-forgejo-consumer <checkout> <pull-request|release>`")
	}

	for _, visibility := range livetest.ConsumerVisibilities(t) {
		t.Run(visibility.Name, func(t *testing.T) {
			runForgejoConsumerTrial(t, root, visibility.Private)
		})
	}
}

func runForgejoConsumerTrial(t *testing.T, root string, private bool) {
	t.Helper()

	// One setting per line, so a variable's value may hold a space.
	var words []string

	for word := range strings.SplitSeq(os.Getenv(consumerSettingsEnv), "\n") {
		if word != "" {
			words = append(words, word)
		}
	}

	settings, err := livetest.ParseTrialSettings(os.Getenv(consumerEventEnv), words)
	if err != nil {
		t.Fatal(err)
	}

	target := livetest.Accept(t, provider.ForgeForgejo)
	if !livetest.Requires(t, provider.ForgeForgejo, livetest.NeedsInRunner) {
		t.Skip("no Forgejo runner in this environment, so the consumer's workflow cannot run")
	}

	consumer := livetest.OpenConsumer(t, target, root)
	if image := os.Getenv("FORGE_CI_LIVE_NPM_APPLICATION_IMAGE"); image != "" {
		if private || livetest.RunnerArch(t, target) != "amd64" || livetest.DemoName() != "" {
			t.Fatal("npm application acceptance requires public disposable native amd64 repositories")
		}
		recipe, err := filepath.Abs("../../../test/live/demos/hello-npm-service/consumer")
		if err != nil {
			t.Fatal(err)
		}
		consumer = livetest.NewNPMApplicationFixture(t, consumer, livetest.NPMApplicationFixtureConfig{RecipeDirectory: recipe, NodeImage: image, Oracle: os.Getenv("FORGE_CI_LIVE_NPM_APPLICATION_ORACLE")}, false)
	}
	if image := os.Getenv("FORGE_CI_LIVE_MAVEN_APPLICATION_IMAGE"); image != "" {
		recipe, err := filepath.Abs("../../../test/live/demos/hello-maven/consumer")
		if err != nil {
			t.Fatal(err)
		}
		consumer = livetest.NewMavenApplicationConsumerFixture(t, consumer, recipe, image)
	}
	if image := os.Getenv("FORGE_CI_LIVE_MAVEN_TESTCONTAINERS_IMAGE"); image != "" {
		if private || settings.Event != livetest.TrialRelease || livetest.RunnerArch(t, target) != "amd64" || livetest.DemoName() != "" || livetest.PrivilegedRunnerLabel(t, target) == "" || os.Getenv("FORGE_CI_LIVE_MAVEN_APPLICATION_IMAGE") == "" {
			t.Fatal("Maven Testcontainers acceptance requires a public disposable native amd64 Maven application fixture and acknowledged privileged runner capacity")
		}
		consumer = livetest.WithMavenContainerTests(t, consumer, image, livetest.PrivilegedRunnerLabel(t, target))
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
	if privileged := livetest.PrivilegedRunnerLabel(t, target); privileged != "" && settings.Variables["CI_IMAGE_BUILDER"] != "kaniko" {
		variables[containerRunnerVariable] = runnerLabelsJSON(t, privileged)
	}

	if release {
		variables[layerRegistryUserVariable] = registryUser(target, external)
	}

	for _, more := range []map[string]string{plan.Variables, settings.ResolvedVariables(scenario.layer.Commit, main)} {
		for variable, value := range more {
			if _, set := variables[variable]; set {
				t.Fatalf("variable %s is set by the trial itself, from the contract or the layer under test, so it cannot be given a second value", variable)
			}

			variables[variable] = value
		}
	}

	// The consumer builds for the lab's runners, and only for them.
	variables, err = livetest.NativeReleasePlatforms(variables, livetest.RunnerArch(t, target))
	if err != nil {
		t.Fatal(err)
	}

	for _, secret := range settings.TokenSecrets {
		if _, set := plan.Secrets[secret]; set {
			t.Fatalf("secret %s is set by the trial itself, from the consumer's own workflow; leave it out", secret)
		}

		plan.Secrets[secret] = target.Token
	}

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

	event := "pull_request"

	if release {
		event = "push"

		requester.PushTag(t, target, repo, "release-request/v"+settings.Version, layerMain, "Request release v"+settings.Version)
	} else {
		requirePullRequest(t, livetest.OpenPullRequest(t, target, repo, trialBranch, layerMain, "test: consumer trial"), trialBranch, head, main)
	}

	run := livetest.WaitForConsumerRun(t, target, repo, settings.Workflow, event, settings.InterruptAfter)

	scenario.logJobs(t, livetest.RunJobs(t, target, repo, run.ID), facts.Secrets, plan.Secrets)
	logArtifacts(t, run, livetest.RunArtifacts(t, target, repo, run.ID))

	if settings.InterruptAfter != "" {
		requireInterrupted(t, target, repo, run, "v"+settings.Version)

		return
	}

	if run.Status != jobSuccess {
		t.Fatalf("run %d of %s ended %q, want %s", run.ID, run.WorkflowID, run.Status, jobSuccess)
	}

	if release {
		requireReleaseGit(t, target, repo, keys, main, "release-request/v"+settings.Version, "v"+settings.Version)
		requireSignedRelease(t, target, repo, keys, "v"+settings.Version)
		requireExternalImages(t, external, keys, settings)

		if settings.NextVersion != "" {
			_, npmErr := os.Stat(filepath.Join(root, "package.json"))
			if npmErr != nil && !os.IsNotExist(npmErr) {
				t.Fatal(npmErr)
			}

			if npmErr == nil {
				requireNPMVersionContract(t, target, repo, "v"+settings.Version)
			}
			// The generated release commit carries [skip ci]. Admit a fresh
			// ordinary candidate without rewriting the first release or tags.
			parent := livetest.CommitFiles(t, target, repo, livetest.FileCommit{
				Branch: layerMain, Message: "test: prepare next release candidate",
				Files: map[string]string{"next-release-request.txt": "request the next release\n"},
			})

			previous := settings.ResolvedVariables(scenario.layer.Commit, main)
			for variable, value := range settings.ResolvedVariables(scenario.layer.Commit, parent) {
				if value != previous[variable] {
					livetest.SetRepoVariable(t, target, repo, variable, value)
				}
			}

			requestTag, finalTag := "release-request/v"+settings.NextVersion, "v"+settings.NextVersion
			requester.PushTag(t, target, repo, requestTag, layerMain, "Request release "+finalTag)
			next := livetest.WaitForConsumerRunAfter(t, target, repo, settings.Workflow, event, run.ID)
			scenario.logJobs(t, livetest.RunJobs(t, target, repo, next.ID), facts.Secrets, plan.Secrets)

			if next.Status != jobSuccess {
				t.Fatalf("second release run %d ended %q", next.ID, next.Status)
			}

			requireReleaseGit(t, target, repo, keys, parent, requestTag, finalTag)
			requireSignedRelease(t, target, repo, keys, finalTag)

			settings.Version = settings.NextVersion
			requireExternalImages(t, external, keys, settings)

			if npmErr == nil {
				requireNPMVersionContract(t, target, repo, finalTag)
			}

			t.Logf("second consecutive Forgejo release %s: run %d, same repository and signing authority", finalTag, next.ID)
		}
	}

	if demo != "" {
		runDemoPullRequest(t, scenario, consumer, main, facts.Secrets, plan.Secrets)
	}
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

	layer := livetest.PushLayer(t, target, livetest.NewScratchRepo(t, target, livetest.LayerScenario(name)))
	repo := livetest.NewBareScratchRepo(t, target, name, consumer.ObjectFormat, private)

	// A consumer's release publishes under names of its own choosing.
	livetest.SweepPackagesAfter(t, target, repo)

	return layer, repo
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

	if run.Status != jobSuccess {
		t.Fatalf("pull-request run %d of %s ended %q, want %s", run.ID, run.WorkflowID, run.Status, jobSuccess)
	}
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

// requireSignedRelease holds the repository to one release of the tag whose
// Cosign bundles each sign a file the release offers and verify against the
// trial's public key. Draft or published is the consumer's choice.
func requireSignedRelease(t *testing.T, target livetest.Target, repo string, keys releaseKeys, tag string) {
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

	assets := make(map[string]string, len(found[0].Assets))

	var size int64

	for _, asset := range found[0].Assets {
		assets[asset.Name] = asset.URL
		size += asset.Size
	}

	verified := 0

	for name, address := range assets {
		signed, isBundle := strings.CutSuffix(name, bundleSuffix)
		if !isBundle {
			continue
		}

		if assets[signed] == "" {
			t.Errorf("the release offers %s and not the file it signs", name)

			continue
		}

		blob := livetest.Download(t, target, assets[signed], maxReleaseFile)
		if err := livetest.VerifyBlob(t, target, keys.cosignPublicPath, blob, livetest.Download(t, target, address, maxReleaseFile)); err != nil {
			t.Errorf("the bundle of %s does not verify against the trial's public key: %v", signed, err)

			continue
		}

		verified++
	}

	if verified == 0 {
		t.Errorf("the release of %s offers no Cosign bundle that verifies, among %d files", tag, len(assets))
	}

	// The size is what one release adds to the owner's storage quota; the
	// trial deletes its repository afterwards, so it is measured here.
	t.Logf("release %s: draft %t, %d files (%.1f MB), %d Cosign bundles verified with the trial's key",
		tag, found[0].Draft, len(assets), float64(size)/1e6, verified)
}
