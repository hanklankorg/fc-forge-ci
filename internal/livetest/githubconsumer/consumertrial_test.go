// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

// Package githubconsumer_test is the hosted GitHub consumer trial. It runs in
// Forge Lab's hosted organization session, not under the local lab's suite.
package githubconsumer_test

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	githubapi "github.com/google/go-github/v91/github"

	"github.com/diggsweden/forge-ci/internal/livetest"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// What `just test-live-github-consumer` hands over.
const (
	organizationEnv = "FORGE_CI_LIVE_GITHUB_ORG_ID"
	checkoutEnv     = "FORGE_CI_LIVE_CONSUMER"
	eventEnv        = "FORGE_CI_LIVE_CONSUMER_EVENT"
	settingsEnv     = "FORGE_CI_LIVE_CONSUMER_SETTINGS"

	trialBranch = "consumer-trial"
)

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
	root := os.Getenv(checkoutEnv)
	if root == "" {
		t.Skip("no consumer checkout is named; run `just test-live-github-consumer <org-id> <checkout> <pull-request|dev-release|release>`")
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
	visibilities := livetest.ConsumerVisibilities(t)

	if livetest.DemoName() != "" && settings.Event != livetest.TrialRelease {
		t.Fatal("a demo runs the release and then a pull request; start it with the release event")
	}

	trial := livetest.OpenGitHubTrial(t, organizationID)

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

	var engine livetest.GitHubEngine

	switch mode := os.Getenv("FORGE_CI_LIVE_ENGINE_MODE"); mode {
	case "", "prepare":
		engine = trial.EnsureEngine(engineRoot, livetest.GitHubEnginePlatforms(settings.Platforms))

		if mode == "prepare" {
			return
		}
	case "reuse":
		engine = trial.ReuseEngine(engineRoot)
	default:
		t.Fatalf("unknown FORGE_CI_LIVE_ENGINE_MODE %q", mode)
	}

	for _, visibility := range visibilities {
		t.Run(visibility.Name, func(t *testing.T) {
			runTrial(t, livetest.OpenGitHubTrial(t, organizationID), engine, root, fixtures, settings, visibility)
		})
	}
}

func runTrial(t *testing.T, trial *livetest.GitHubTrial, engine livetest.GitHubEngine, root, fixtures string, settings livetest.GitHubTrialSettings, visibility livetest.ConsumerVisibility) {
	t.Helper()

	demo := livetest.DemoName()

	var repo string

	if demo != "" {
		trial = trial.WithHistory()
		repo = trial.NewDemoRepository(demo)
	} else {
		repo = trial.NewConsumerRepository(consumerRepositoryName(root, visibility.Name, settings.Event, settings.Workflow), visibility.Private)
	}

	overrides := maps.Clone(settings.Overrides)
	trust := livetest.KeylessTrust(engine)

	var cosignKey *livetest.TrialCosignKey

	if settings.Event == livetest.TrialRelease && keySigned(t, root, fixtures) {
		key := trial.NewTrialCosignKey()
		cosignKey, trust = &key, key.Trust()

		if overrides == nil {
			overrides = map[string]string{}
		}

		overrides["cosign-public-key-sha256"] = key.SHA256
	}

	tree := trial.PushConsumer(repo, root, fixtures, engine, overrides)
	trial.SetVariable(repo, "FORGE_CI_REPOSITORY", engine.Owner+"/"+engine.Repository)
	trial.SetVariable(repo, "RELEASE_PLATFORMS", settings.Platforms)

	t.Logf("consumer %s rendered from %s (%s) at %s, engine %s/%s at %s", repo, root, fixtures, tree.Main, engine.Owner, engine.Repository, engine.Commit)

	if settings.Event == livetest.TrialPullRequest {
		head := trial.PushChange(repo, tree, trialBranch)
		trial.OpenPullRequest(repo, trialBranch)
		judge(t, trial, repo, settings.Workflow, "pull_request", head)

		return
	}

	if settings.Event == livetest.TrialDevRelease {
		dispatched := trial.DispatchDevelopment(repo, settings.Workflow)

		run := judge(t, trial, repo, settings.Workflow, "workflow_dispatch", tree.Main)
		if dispatched != 0 && run.GetID() != dispatched {
			t.Fatal("judged development run differs from the dispatch response")
		}

		trial.CheckDevelopmentRelease(repo, tree.Main, run.GetID())

		return
	}

	key := trial.NewTrialGPGKey()
	secrets := map[string]string{
		"RELEASE_GPG_PRIVATE_KEY": key.Private,
		"RELEASE_GPG_PASSPHRASE":  key.Passphrase,
		"RELEASE_GPG_PUBLIC_KEY":  key.Public,
		"RELEASE_TOKEN":           trial.Token(),
	}

	if cosignKey != nil {
		secrets["RELEASE_COSIGN_PRIVATE_KEY"] = cosignKey.Private
		secrets["RELEASE_COSIGN_PASSPHRASE"] = cosignKey.Passphrase
	}

	// A kept demo holds no credential once the run is over.
	if demo != "" {
		trial.DeleteSecretsAfter(repo, slices.Sorted(maps.Keys(secrets)))
	}

	for secret, value := range secrets {
		trial.SetSecret(repo, secret, value)
	}

	if cosignKey != nil {
		trial.SetVariable(repo, "COSIGN_PUBLIC_KEY", cosignKey.Public)
	}

	request, final, err := livetest.ReleaseRequestTag(settings.Version)
	if err != nil {
		t.Fatal(err)
	}

	trial.PushReleaseRequest(repo, tree, key, request, settings.Version)
	judge(t, trial, repo, settings.Workflow, "push", tree.Main)

	assets := trial.CheckRelease(repo, tree, final, trust)
	if visibility.Private {
		trial.CheckPrivateReleaseAssets(repo, assets)
	}

	if settings.Image != "" {
		if visibility.Private {
			t.Log("private image verification uses the host's registry credentials; provide an isolated DOCKER_CONFIG for GHCR access")
		}

		for image := range strings.SplitSeq(settings.Image, ",") {
			verified := trial.CheckImage(repo, image, final, engine, trust)
			if visibility.Private && verified != "" {
				trial.CheckPrivateImage(verified)
			}
		}
	}

	// A demo then shows a pull request, judged by the consumer's own
	// pull-request workflow.
	if demo != "" {
		pullRequest, err := livetest.ParseGitHubTrialSettings(livetest.TrialPullRequest, nil)
		if err != nil {
			t.Fatal(err)
		}

		head := trial.PushChange(repo, tree, trialBranch)
		trial.OpenPullRequest(repo, trialBranch)
		judge(t, trial, repo, pullRequest.Workflow, "pull_request", head)
	}
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

// keySigned reports whether the fixture's release workflow signs with a Cosign
// key: it then pins the key's digest beside the variable it reads the key from.
func keySigned(t *testing.T, root, fixtures string) bool {
	t.Helper()

	release, err := os.ReadFile(filepath.Join(root, fixtures, "workflows", "release.yml"))
	if err != nil {
		t.Fatalf("read the fixture's release workflow: %v", err)
	}

	return strings.Contains(string(release), "cosign-public-key-sha256:")
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
