// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

// Package githubconsumer_test is the hosted GitHub consumer trial. It runs in
// Forge Lab's hosted organization session, not under the local lab's suite.
package githubconsumer_test

import (
	"errors"
	"fmt"
	"maps"
	"net/mail"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	githubapi "github.com/google/go-github/v91/github"
	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

// What `just test-live-consumer github` hands over.
const (
	organizationEnv = "FORGE_CI_LIVE_GITHUB_ORG_ID"
	checkoutEnv     = "FORGE_CI_LIVE_CONSUMER"
	eventEnv        = "FORGE_CI_LIVE_CONSUMER_EVENT"
	settingsEnv     = "FORGE_CI_LIVE_CONSUMER_SETTINGS"

	trialBranch = "consumer-trial"
)

// openHostedConsumer reads what `just test-live-consumer github` hands over:
// the consumer checkout, the authorized organization, the event's settings
// and the fixture directory, whose GPG profile is validated. Without a
// checkout the test is skipped with the command that names one, events being
// the words it takes.
func openHostedConsumer(t *testing.T, events string) (string, int64, livetest.GitHubTrialSettings, string) {
	t.Helper()

	root := os.Getenv(checkoutEnv)
	if root == "" {
		t.Skipf("no consumer checkout is named; run `just test-live-consumer github <checkout> %s org=<org-id>`", events)
	}

	organizationID, err := strconv.ParseInt(os.Getenv(organizationEnv), 10, 64)
	if err != nil || organizationID <= 0 {
		t.Fatalf("%s must be the numeric ID of the authorized test organization", organizationEnv)
	}

	var words []string

	for word := range strings.SplitSeq(os.Getenv(settingsEnv), "\n") {
		if word != "" {
			words = append(words, word)
		}
	}

	settings, err := livetest.ParseGitHubTrialSettings(os.Getenv(eventEnv), words)
	if err != nil {
		t.Fatal(err)
	}

	fixtures := fixtureDir(t, root, settings.Fixtures)
	if err := livetest.ValidateGitHubGPGFixtureProfile(t.Context(), root, fixtures, settings.Overrides); err != nil {
		t.Fatal(err)
	}

	if name := settings.PullRequestWorkflow(); name != "" {
		text, err := os.ReadFile(filepath.Join(root, fixtures, "workflows", name))
		if err != nil {
			t.Fatalf("read the fixture's pull-request workflow: %v", err)
		}

		if err := livetest.RequireGitHubLintImage(name, string(text), settings.Overrides); err != nil {
			t.Fatal(err)
		}
	}

	return root, organizationID, settings, fixtures
}

// hostedReleaseToken reads the consumer's release credential for an event that
// releases, a release or a lifecycle, unless the engine is only being
// prepared; a pull request or a development build gets no release authority.
func hostedReleaseToken(t *testing.T, settings livetest.GitHubTrialSettings) string {
	t.Helper()

	var releaseToken string

	if (settings.Event == livetest.TrialRelease || settings.Event == livetest.TrialLifecycle) && os.Getenv("FORGE_CI_LIVE_ENGINE_MODE") != "prepare" {
		var err error

		releaseToken, err = livetest.GitHubReleaseToken(os.Getenv(livetest.GitHubReleaseTokenFileEnv))
		if err != nil {
			t.Fatal(err)
		}
	}

	return releaseToken
}

// A hosted GitHub consumer trial: a consumer's GitHub fixture workflows run in
// a disposable repository of the test organization, against the engine this
// checkout pushed to the organization.
//
// Like the Forgejo and GitLab trials it is not a scenario of the tier: what it
// shows depends on the consumer it is given. For a pull request the verdict is
// the run of the consumer's pull-request workflow on a pull request. For a
// release it is the release run, and then what the engine promises of any
// GitHub release: main advanced by one signed commit, the final tag exists,
// the release offers assets whose Cosign bundles verify, and, with
// image=<name>, the promoted image verifies too. Signatures verify against the
// engine's keyless identity, or, when the fixture's release workflow pins a
// `cosign-public-key-sha256`, against a Cosign key pair made for the trial.
func TestGitHubLayer_ConsumerTrial(t *testing.T) {
	livetest.RegisterLiveScenario(t)

	root, organizationID, settings, fixtures := openHostedConsumer(t, "<pull-request|dev-release|release>")
	if settings.Event == livetest.TrialLifecycle {
		t.Fatal("a lifecycle is TestGitHubLayer_ConsumerLifecycle; run `just test-live-consumer github <checkout> lifecycle org=<org-id>`")
	}

	if livetest.DemoName() != "" && settings.Event != livetest.TrialRelease {
		t.Fatal("a demo runs the release and then a pull request; start it with the release event")
	}

	releaseToken := hostedReleaseToken(t, settings)
	trial := livetest.OpenGitHubTrial(t, organizationID)

	engine, prepared := hostedEngine(t, trial, settings)
	if prepared {
		// Not a pass: a run that built the engine ran no consumer.
		t.Skip("the engine was prepared and no consumer ran; run the cell again with FORGE_CI_LIVE_ENGINE_MODE=reuse")
	}

	runVisibilities(t, root, fixtures, settings, func(t *testing.T, visibility livetest.ConsumerVisibility) {
		t.Helper()
		runTrial(t, livetest.OpenGitHubTrial(t, organizationID), engine, root, fixtures, settings, visibility, releaseToken)
	})
}

// runVisibilities runs each visibility of a cell as a subtest of its own,
// side by side. They share nothing they change: each has its own
// repository, named after the visibility, its own client, keys and
// temporary directories, and its own packages under that repository's
// name; the engine is read once, before either starts, and never changed.
//
// A release that publishes a package the organization names apart from
// the repository (livetest.GitHubReleaseSharesPackages) keeps them one
// after the other: both would publish the same package at the same
// version, which the registry takes once, and the one that came second
// would find the other's package there.
//
// A development build is taken as safe: its snapshot publishes images
// named under the repository alone. A development cell of a Maven library
// would need the predicate too.
func runVisibilities(t *testing.T, root, fixtures string, settings livetest.GitHubTrialSettings, run func(*testing.T, livetest.ConsumerVisibility)) {
	t.Helper()

	parallel := true

	if settings.Event == livetest.TrialRelease || settings.Event == livetest.TrialLifecycle {
		shared, err := livetest.GitHubReleaseSharesPackages(root, fixtures, settings.Workflow)
		if err != nil {
			t.Fatal(err)
		}

		parallel = !shared
	}

	if !parallel {
		t.Log("the visibilities run one after the other: the release publishes a package the organization names apart from the repository")
	}

	for _, visibility := range livetest.ConsumerVisibilities(t) {
		t.Run(visibility.Name, func(t *testing.T) {
			if parallel {
				t.Parallel()
			}

			run(t, visibility)
		})
	}
}

// hostedEngine is the engine under test in the organization: HEAD of the
// engine checkout, pushed and built by FORGE_CI_LIVE_ENGINE_MODE prepare or
// by default, or the one already there with reuse. It reports whether the
// run only prepared it.
func hostedEngine(t *testing.T, trial *livetest.GitHubTrial, settings livetest.GitHubTrialSettings) (livetest.GitHubEngine, bool) {
	t.Helper()

	engineRoot := os.Getenv("FORGE_CI_LIVE_ENGINE_CHECKOUT")
	if engineRoot == "" {
		engineRoot = reporoot.Path(t)
	} else if !filepath.IsAbs(engineRoot) {
		t.Fatal("FORGE_CI_LIVE_ENGINE_CHECKOUT must be an absolute committed engine checkout")
	}

	controllerCommit, err := exec.CommandContext(livetest.Context(t), "git", "-C", reporoot.Path(t), "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("read hosted controller revision: %v", err)
	}

	t.Logf("controller checkout: %s (working tree executed; HEAD %s); engine checkout: %s", reporoot.Path(t), strings.TrimSpace(string(controllerCommit)), engineRoot)

	switch mode := os.Getenv("FORGE_CI_LIVE_ENGINE_MODE"); mode {
	case "", "prepare":
		return trial.EnsureEngine(engineRoot, livetest.GitHubEnginePlatforms(settings.Platforms)), mode == "prepare"
	case "reuse":
		return trial.ReuseEngine(engineRoot), false
	default:
		t.Fatalf("unknown FORGE_CI_LIVE_ENGINE_MODE %q", mode)
	}

	return livetest.GitHubEngine{}, false
}

// trialSigning is what a release signs with, decided from the fixture's
// release workflow before the consumer is rendered.
type trialSigning struct {
	trust     livetest.CosignTrust
	cosignKey *livetest.TrialCosignKey
	sshKey    string
	overrides map[string]string
}

// prepareSigning reads the fixture's release signing profile and makes the
// keys the release signs with: a Cosign key pair when the release pins one,
// whose public hash the rendered fixture gets, and an SSH signing key when
// the release signs Git with SSH, with the allowed-signers line that names it
// for the workflow's bot-email staged in the consumer. It returns the trial
// with that file.
func prepareSigning(t *testing.T, trial *livetest.GitHubTrial, engine livetest.GitHubEngine, root, fixtures string, settings livetest.GitHubTrialSettings) (*livetest.GitHubTrial, trialSigning) {
	t.Helper()

	keySigned, sshPrincipal, err := releaseSigning(root, fixtures, settings)
	if err != nil {
		t.Fatal(err)
	}

	signing := trialSigning{trust: livetest.KeylessTrust(engine), overrides: maps.Clone(settings.Overrides)}

	// A release that pins no key is keyless and would write to the public
	// Sigstore log: the trial hands it its own key and turns the log off.
	if !keySigned {
		config, artifacts, err := keylessRelease(root, fixtures, settings)
		if err != nil {
			t.Fatal(err)
		}

		if config != "" {
			trial, keySigned = trial.WithTrialKeySigning(settings.Workflow, config, artifacts), true
		}
	}

	if keySigned {
		key := trial.NewTrialCosignKey()
		signing.cosignKey, signing.trust = &key, key.Trust()

		if signing.overrides == nil {
			signing.overrides = map[string]string{}
		}

		signing.overrides["cosign-public-key-sha256"] = key.SHA256
	}

	if sshPrincipal != "" {
		private, public := livetest.SSHKey(t, t.TempDir())
		signing.sshKey = private
		trial = trial.WithFile(".forge-ci/allowed_signers", sshPrincipal+" "+public+"\n")
	}

	return trial, signing
}

// setReleaseSecrets gives the repository the release's authority: the trial's
// GPG key, the release token and the signing keys the fixture's release uses,
// and the Cosign public key as a variable when the release pins one. A kept
// demo holds no credential once the run is over.
func setReleaseSecrets(t *testing.T, trial *livetest.GitHubTrial, repo string, signing trialSigning, key livetest.TrialGPGKey, releaseToken string, demo bool) {
	t.Helper()

	secrets := map[string]string{
		"RELEASE_GPG_PRIVATE_KEY": key.Private,
		"RELEASE_GPG_PASSPHRASE":  key.Passphrase,
		"RELEASE_GPG_PUBLIC_KEY":  key.Public,
		"RELEASE_TOKEN":           releaseToken,
	}

	if signing.sshKey != "" {
		secrets["RELEASE_SSH_SIGNING_KEY"] = signing.sshKey
	}

	if signing.cosignKey != nil {
		secrets["RELEASE_COSIGN_PRIVATE_KEY"] = signing.cosignKey.Private
		secrets["RELEASE_COSIGN_PASSPHRASE"] = signing.cosignKey.Passphrase
	}

	if demo {
		trial.DeleteSecretsAfter(repo, slices.Sorted(maps.Keys(secrets)))
	}

	for secret, value := range secrets {
		trial.SetSecret(repo, secret, value)
	}

	if signing.cosignKey != nil {
		trial.SetVariable(repo, "COSIGN_PUBLIC_KEY", signing.cosignKey.Public)
	}
}

// checkImages verifies each released image image= names at the final tag: a
// public consumer's without a credential of the trial's, a private consumer's
// with the session's Packages token, and then that the same reference is
// denied to an anonymous reader.
func checkImages(t *testing.T, trial *livetest.GitHubTrial, repo string, settings livetest.GitHubTrialSettings, final string, engine livetest.GitHubEngine, trust livetest.CosignTrust, private bool) {
	t.Helper()

	if settings.Image == "" {
		return
	}

	if private {
		t.Log("private image verification reads GHCR with the session's Packages token, through a DOCKER_CONFIG only the verification gets")
	}

	for image := range strings.SplitSeq(settings.Image, ",") {
		verified := trial.CheckImage(repo, image, final, engine, trust, private)
		if private && verified != "" {
			trial.CheckPrivateImage(verified)
		}
	}
}

func runTrial(t *testing.T, trial *livetest.GitHubTrial, engine livetest.GitHubEngine, root, fixtures string, settings livetest.GitHubTrialSettings, visibility livetest.ConsumerVisibility, releaseToken string) {
	t.Helper()

	trial, signing := prepareSigning(t, trial, engine, root, fixtures, settings)
	demo := livetest.DemoName()

	var repo string

	if demo != "" {
		trial = trial.WithHistory()
		repo = trial.NewDemoRepository(demo)
	} else {
		repo = trial.NewConsumerRepository(consumerRepositoryName(root, visibility.Name, settings.Event, settings.Workflow), visibility.Private)
	}

	// A development fixture that snapshots the service snapshot's
	// declaration gets it staged, as the lifecycle does, and is judged by
	// the snapshot oracle.
	snapshot := settings.Event == livetest.TrialDevRelease && snapshotFixture(t, root, fixtures, settings.Workflow)
	if snapshot {
		trial = trial.WithFile(livetest.ServiceSnapshotDeclarationFile, livetest.ServiceSnapshotOverlay(t, root))
	}

	// A release signs its files at the release commit's time, and one that
	// prepares no commit signs at the pushed commit's: the key must exist
	// before that commit does. Made after the push, the key was not yet valid
	// at that time and the no-changelog release refused to sign (hosted run
	// 42733793511, 2026-11-10).
	var key livetest.TrialGPGKey
	if settings.Event == livetest.TrialRelease {
		key = trial.NewTrialGPGKey()
	}

	tree := trial.PushConsumer(repo, root, fixtures, engine, signing.overrides)
	trial.SetVariable(repo, "FORGE_CI_REPOSITORY", engine.Owner+"/"+engine.Repository)
	trial.SetVariable(repo, "RELEASE_PLATFORMS", settings.Platforms)

	t.Logf("consumer %s rendered from %s (%s) at %s, engine %s/%s at %s", repo, root, fixtures, tree.Main, engine.Owner, engine.Repository, engine.Commit)

	if settings.Event == livetest.TrialPullRequest && settings.Fork {
		judgeForkPullRequest(t, trial, repo, tree, settings.Workflow, visibility)

		return
	}

	if settings.Event == livetest.TrialPullRequest {
		head := trial.PushChange(repo, tree, trialBranch)
		trial.OpenPullRequest(repo, trialBranch)
		judge(t, trial, repo, settings.Workflow, "pull_request", head)

		return
	}

	if settings.Event == livetest.TrialDevRelease {
		if snapshot && demo == "" {
			trial.RemoveSnapshotPackageAfter(repo, trial.RepositoryID(repo))
		}

		dispatched := trial.DispatchDevelopment(repo, settings.Workflow, nil)

		run := judge(t, trial, repo, settings.Workflow, "workflow_dispatch", tree.Main)
		if dispatched != 0 && run.GetID() != dispatched {
			t.Fatal("judged development run differs from the dispatch response")
		}

		trial.CheckDevelopmentRelease(repo, tree.Main, run.GetID())

		if snapshot {
			t.Logf("development snapshot of main@%s: %s", tree.Main, receiptRefs(judgeSnapshot(t, trial, repo, visibility.Private, tree, run.GetID(), "main", tree.Main)))
		}

		return
	}

	setReleaseSecrets(t, trial, repo, signing, key, releaseToken, demo != "")

	request, final, err := livetest.ReleaseRequestTag(settings.Version)
	if err != nil {
		t.Fatal(err)
	}

	// A kept demo keeps the package its release publishes.
	if demo == "" {
		trial.RemoveReleasedPackagesAfter(repo, tree, settings.Workflow)
	}

	trial.PushReleaseRequest(repo, tree, key, request, settings.Version)
	judge(t, trial, repo, settings.Workflow, "push", tree.Main)

	assets := trial.CheckRelease(repo, tree, settings.Workflow, final, signing.trust, key)
	if visibility.Private {
		trial.CheckPrivateReleaseAssets(repo, assets)
	}

	checkImages(t, trial, repo, settings, final, engine, signing.trust, visibility.Private)

	// A demo then shows a pull request, judged by the consumer's own
	// pull-request workflow.
	if demo != "" {
		head := trial.PushChange(repo, tree, trialBranch)
		trial.OpenPullRequest(repo, trialBranch)
		judge(t, trial, repo, settings.DemoPullRequest, "pull_request", head)
	}
}

// judgeForkPullRequest is head=fork: the same change, pushed to a fork of the
// consumer in the organization and opened against the consumer. A fixture
// whose build enables private dependencies must have the run refused in the
// build's first step, before its checkout. GitHub hands a fork's pull request
// no secrets, so none is set, and the run needs none to be refused.
func judgeForkPullRequest(t *testing.T, trial *livetest.GitHubTrial, repo string, tree livetest.GitHubConsumerTree, workflow string, visibility livetest.ConsumerVisibility) {
	t.Helper()

	if visibility.Private {
		livetest.NotCoveredf(t, "fork pull request admission on a private repository: GitHub runs a private fork's pull request workflows "+
			"only where the organization's policy sends them; run it with FORGE_CI_LIVE_CONSUMER_VISIBILITY=public")
	}

	fork := trial.ForkConsumer(repo)
	head := trial.PushChange(fork, tree, trialBranch)
	number := trial.OpenForkPullRequest(repo, fork, trialBranch)

	run := trial.WaitRun(repo, workflow, "pull_request", head, livetest.GitHubRunTimeout)
	t.Logf("%s run %d of %s for fork pull request #%d from %s: %s", workflow, run.GetID(), repo, number, fork, run.GetConclusion())
	trial.ReportRun(repo, run)
	trial.CheckForkRefusal(repo, run)
}

func consumerRepositoryName(root, visibility, event, workflow string) string {
	return filepath.Base(filepath.Clean(root)) + "-" + visibility + "-" + event + "-" + workflow
}

func TestConsumerRepositoryNamesSeparateConcurrentEvents(t *testing.T) {
	seen := map[string]bool{}

	for _, visibility := range []string{"public", "private"} {
		for _, event := range []string{livetest.TrialPullRequest, livetest.TrialDevRelease, livetest.TrialRelease} {
			for _, workflow := range []string{"release-dev.yml", "release-dev.yaml", "release-dev-test-signed.yml"} {
				name := consumerRepositoryName("/consumers/fc-ios-app/", visibility, event, workflow)
				if seen[name] {
					t.Fatalf("concurrent consumer cases share repository identity %q", name)
				}

				seen[name] = true
			}
		}
	}
}

// releaseSigning inspects only a stable release's selected workflow, a
// release's or a lifecycle's. PR and development fixtures need not declare a
// stable release workflow at all. An SSH signer must name its principal,
// which binds allowed_signers to the key.
func releaseSigning(root, fixtures string, settings livetest.GitHubTrialSettings) (bool, string, error) {
	if settings.Event != livetest.TrialRelease && settings.Event != livetest.TrialLifecycle {
		return false, "", nil
	}

	release, err := os.ReadFile(filepath.Join(root, fixtures, "workflows", settings.Workflow))
	if err != nil {
		return false, "", fmt.Errorf("read the fixture's release workflow: %w", err)
	}

	var workflow struct {
		Jobs map[string]struct {
			With    map[string]any `yaml:"with"`
			Secrets yaml.Node      `yaml:"secrets"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(release, &workflow); err != nil {
		return false, "", fmt.Errorf("parse the fixture's release workflow: %w", err)
	}

	var (
		keySigned bool
		principal string
	)

	for name, job := range workflow.Jobs {
		if _, pinsKey := job.With["cosign-public-key-sha256"]; pinsKey {
			keySigned = true
		}

		var inherited string
		if err := job.Secrets.Decode(&inherited); err == nil && inherited == "inherit" {
			continue
		}
		// Decode, rather than inspecting raw nodes, resolves native YAML aliases
		// in both the secrets mapping and its individual values, with library limits.
		var secrets map[string]string
		if err := job.Secrets.Decode(&secrets); err != nil {
			return false, "", fmt.Errorf("decode release job %s secrets: %w", name, err)
		}

		for _, secret := range secrets {
			if !strings.Contains(secret, "secrets.RELEASE_SSH_SIGNING_KEY") {
				continue
			}

			email, literal := job.With["bot-email"].(string)

			address, err := mail.ParseAddress(email)
			if !literal || err != nil || address.Name != "" || address.Address != email ||
				strings.ContainsAny(email, "*?![],\\") || strings.Contains(email, "${{") {
				return false, "", fmt.Errorf("release job %s requires a literal bot-email signing principal without SSH patterns", name)
			}

			if principal != "" && principal != email {
				return false, "", errors.New("release jobs require different SSH signing principals")
			}

			principal = email
		}
	}

	return keySigned, principal, nil
}

// keylessRelease names the artifacts config, with its content, of a release
// that would sign through the public Sigstore log: the trial then signs with
// its own key (WithTrialKeySigning). It names none for a pull request or a
// development trial, which sign nothing, and for a release that signs with
// GPG or sets `sign.transparency: none`.
func keylessRelease(root, fixtures string, settings livetest.GitHubTrialSettings) (string, string, error) {
	if settings.Event != livetest.TrialRelease && settings.Event != livetest.TrialLifecycle {
		return "", "", nil
	}

	release, err := os.ReadFile(filepath.Join(root, fixtures, "workflows", settings.Workflow))
	if err != nil {
		return "", "", fmt.Errorf("read the fixture's release workflow: %w", err)
	}

	var workflow struct {
		Jobs map[string]struct {
			With map[string]any `yaml:"with"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(release, &workflow); err != nil {
		return "", "", fmt.Errorf("parse the fixture's release workflow: %w", err)
	}

	// The release workflow's own default, and the caller's override of it.
	config := ".forge-ci/artifacts.yml"

	for name, job := range workflow.Jobs {
		if named, isNamed := job.With["artifacts-config"].(string); isNamed {
			config = named
		} else if _, other := job.With["artifacts-config"]; other {
			return "", "", fmt.Errorf("%w: release job %s must name its artifacts-config literally", livetest.ErrPublicTransparency, name)
		}
	}

	if override := settings.Overrides["artifacts-config"]; override != "" {
		config = override
	}

	// A release without the file derives its config and signs keyless.
	body, err := os.ReadFile(filepath.Join(root, config))
	if err != nil {
		return "", "", fmt.Errorf("%w: read the artifacts config the release signs by: %w", livetest.ErrPublicTransparency, err)
	}

	if !livetest.ReleaseWritesPublicLog(string(body)) {
		return "", "", nil
	}

	return config, string(body), nil
}

// judge waits for the run, prints its jobs, and fails the trial unless it
// succeeded. GitHub records a pull-request run under the branch head and a
// tag-push run under the tagged commit.
func judge(t *testing.T, trial *livetest.GitHubTrial, repo, workflow, event, headSHA string) *githubapi.WorkflowRun {
	t.Helper()

	run := trial.WaitRun(repo, workflow, event, headSHA, livetest.GitHubRunTimeout)
	t.Logf("%s run %d of %s: %s", workflow, run.GetID(), repo, run.GetConclusion())
	trial.ReportRun(repo, run)

	switch run.GetConclusion() {
	case "success":
	case "startup_failure":
		t.Fatalf("GitHub refused the consumer's %s before any job ran (startup_failure): most often a calling job grants less "+
			"than the called workflow's jobs ask for (permissions:), or names an input or secret it does not declare", workflow)
	default:
		t.Fatalf("the consumer's %s ended %s", workflow, run.GetConclusion())
	}

	return run
}

// snapshotFixture reports whether the consumer's fixture workflow names the
// service snapshot's declaration, and so snapshots it.
func snapshotFixture(t *testing.T, root, fixtures, workflow string) bool {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(root, fixtures, "workflows", workflow))
	if err != nil {
		t.Fatal(err)
	}

	return strings.Contains(string(body), livetest.ServiceSnapshotDeclarationFile)
}

// fixtureDir is the consumer's GitHub fixture directory: the setting, or the
// first of the conventional places that holds workflows.
func fixtureDir(t *testing.T, root, setting string) string {
	t.Helper()

	candidates := []string{"test/live/github", "tests/live/github"}
	if setting != "" {
		candidates = []string{setting}
	}

	for _, candidate := range candidates {
		if info, err := os.Stat(filepath.Join(root, candidate, "workflows")); err == nil && info.IsDir() {
			return candidate
		}
	}

	t.Fatalf("%s has no GitHub fixture workflows under %s; name the directory with fixtures=<dir>", root, strings.Join(candidates, " or "))

	return ""
}
