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
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/livetest"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// What `just test-live-github-consumer` hands over.
const (
	organizationEnv = "RC_LIVE_GITHUB_ORG_ID"
	checkoutEnv     = "RC_LIVE_CONSUMER"
	eventEnv        = "RC_LIVE_CONSUMER_EVENT"
	settingsEnv     = "RC_LIVE_CONSUMER_SETTINGS"

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
	root := os.Getenv(checkoutEnv)
	if root == "" {
		t.Skip("no consumer checkout is named; run `just test-live-github-consumer <org-id> <checkout> <pull-request|release>`")
	}

	organizationID, err := strconv.ParseInt(os.Getenv(organizationEnv), 10, 64)
	if err != nil || organizationID <= 0 {
		t.Fatalf("%s must be the numeric ID of the authorized test organization", organizationEnv)
	}

	var words []string

	for _, word := range strings.Split(os.Getenv(settingsEnv), "\n") {
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

	trial := livetest.OpenGitHubTrial(t, organizationID)
	engine := trial.EnsureEngine(reporoot.Path(t))

	for _, visibility := range visibilities {
		t.Run(visibility.Name, func(t *testing.T) {
			runTrial(t, livetest.OpenGitHubTrial(t, organizationID), engine, root, fixtures, settings, visibility)
		})
	}
}

func runTrial(t *testing.T, trial *livetest.GitHubTrial, engine livetest.GitHubEngine, root, fixtures string, settings livetest.GitHubTrialSettings, visibility livetest.ConsumerVisibility) {
	t.Helper()

	name := filepath.Base(filepath.Clean(root)) + "-" + visibility.Name
	repo := trial.NewConsumerRepository(name, visibility.Private)

	overrides := maps.Clone(settings.Overrides)
	trust := livetest.KeylessTrust(engine)

	var cosignKey *livetest.TrialCosignKey

	if settings.Event != livetest.TrialPullRequest && keySigned(t, root, fixtures) {
		key := trial.NewTrialCosignKey()
		cosignKey, trust = &key, key.Trust()

		if overrides == nil {
			overrides = map[string]string{}
		}

		overrides["cosign-public-key-sha256"] = key.SHA256
	}

	tree := trial.PushConsumer(repo, root, fixtures, engine, overrides)
	trial.SetVariable(repo, "FORGE_CI_REPOSITORY", engine.Owner+"/"+engine.Repository)

	t.Logf("consumer %s rendered from %s (%s) at %s, engine %s/%s at %s", repo, root, fixtures, tree.Main, engine.Owner, engine.Repository, engine.Commit)

	if settings.Event == livetest.TrialPullRequest {
		head := trial.PushChange(repo, tree, trialBranch)
		trial.OpenPullRequest(repo, trialBranch)
		judge(t, trial, repo, settings.Workflow, "pull_request", head)

		return
	}

	key := trial.NewTrialGPGKey()
	for secret, value := range map[string]string{
		"RELEASE_GPG_PRIVATE_KEY": key.Private,
		"RELEASE_GPG_PASSPHRASE":  key.Passphrase,
		"RELEASE_GPG_PUBLIC_KEY":  key.Public,
		"RELEASE_TOKEN":           trial.Token(),
	} {
		trial.SetSecret(repo, secret, value)
	}

	if cosignKey != nil {
		trial.SetSecret(repo, "RELEASE_COSIGN_PRIVATE_KEY", cosignKey.Private)
		trial.SetSecret(repo, "RELEASE_COSIGN_PASSPHRASE", cosignKey.Passphrase)
		trial.SetVariable(repo, "COSIGN_PUBLIC_KEY", cosignKey.Public)
	}

	request, final, err := livetest.ReleaseRequestTag(settings.Version)
	if err != nil {
		t.Fatal(err)
	}

	trial.PushReleaseRequest(repo, tree, key, request, settings.Version)
	judge(t, trial, repo, settings.Workflow, "push", tree.Main)
	trial.CheckRelease(repo, tree, final, trust)

	switch {
	case settings.Image == "":
	case visibility.Private:
		t.Logf("image %s of private %s not verified: its package needs registry credentials", settings.Image, repo)
	default:
		trial.CheckImage(repo, settings.Image, final, engine, trust)
	}
}

// keySigned reports whether the fixture's release workflow signs with a Cosign
// key: it then pins the key's digest beside the variable it reads the key from.
func keySigned(t *testing.T, root, fixtures string) bool {
	t.Helper()

	release, err := os.ReadFile(filepath.Join(root, fixtures, "workflows", "release.yml")) //nolint:gosec // the caller's own checkout; settings refuse ".." and absolute paths.
	if err != nil {
		t.Fatalf("read the fixture's release workflow: %v", err)
	}

	return strings.Contains(string(release), "cosign-public-key-sha256:")
}

// judge waits for the run, prints its jobs, and fails the trial unless it
// succeeded. GitHub records a pull-request run under the branch head and a
// tag-push run under the tagged commit.
func judge(t *testing.T, trial *livetest.GitHubTrial, repo, workflow, event, headSHA string) {
	t.Helper()

	run := trial.WaitRun(repo, workflow, event, headSHA, livetest.GitHubRunTimeout)
	t.Logf("%s run %d of %s: %s", workflow, run.GetID(), repo, run.GetConclusion())
	trial.ReportRun(repo, run)

	if run.GetConclusion() != "success" {
		t.Fatalf("the consumer's %s ended %s", workflow, run.GetConclusion())
	}
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
		if info, err := os.Stat(filepath.Join(root, candidate, "workflows")); err == nil && info.IsDir() { //nolint:gosec // the caller's own checkout; settings refuse ".." and absolute paths.
			return candidate
		}
	}

	t.Fatalf("%s has no GitHub fixture workflows under %s; name the directory with fixtures=<dir>", root, strings.Join(candidates, " or "))

	return ""
}
