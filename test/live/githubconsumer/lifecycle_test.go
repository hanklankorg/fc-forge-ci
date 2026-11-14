// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package githubconsumer_test

import (
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

// lifecycleBranch is the branch the change under review goes to, and the
// branch the development snapshot is taken from.
const lifecycleBranch = "development"

// A consumer lifecycle on hosted GitHub: the linked flow
// TestForgejoLayer_ConsumerLifecycle runs, in one disposable repository of the
// test organization, against the engine this checkout pushed there. It is the
// Maven service demo's, whose GitHub fixtures call the three orchestrators a
// consumer calls:
//
//  1. a pull request from development into main, judged by the consumer's
//     own pullrequest.yml;
//  2. its native merge through the API, told the head commit, with a merge
//     commit of the base and the change;
//  3. the development snapshot: release-dev.yml, the consumer's call of the
//     snapshot orchestrator over the service snapshot's declaration the
//     trial staged (livetest.ServiceSnapshotOverlay), dispatched on the
//     merged main and told to snapshot development; its run must succeed,
//     deliver its artifacts and leave main, the tags and the releases
//     alone, and what it published to the repository's container package is
//     judged by the snapshot oracle every forge's trial shares
//     (livetest.RequireSnapshotImages);
//  4. a signed release request at the merged main, judged as the release
//     trial's: main advanced by one signed release commit, the final tag, a
//     release whose signatures verify, and the released images image= names;
//  5. the repository holds those three runs, two tags and one release, and
//     nothing else.
//
// Each phase ends the lifecycle when it fails, and says which phase it was.
// The release's secrets are set only before the release phase; the
// development phase publishes with the workflow's own token, and the pull
// request gets none. A lifecycle runs in a disposable repository, never in a
// kept demo.
func TestGitHubLayer_ConsumerLifecycle(t *testing.T) {
	livetest.RegisterLiveScenario(t)

	root, organizationID, settings, fixtures := openHostedConsumer(t, "lifecycle")
	if settings.Event != livetest.TrialLifecycle {
		t.Fatalf("the event is %s; a lifecycle starts with %s", settings.Event, livetest.TrialLifecycle)
	}

	if livetest.DemoName() != "" {
		t.Fatal("a lifecycle runs in disposable repositories, not in a kept demo")
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
		runLifecycle(t, livetest.OpenGitHubTrial(t, organizationID), engine, root, fixtures, settings, visibility, releaseToken)
	})
}

func runLifecycle(t *testing.T, trial *livetest.GitHubTrial, engine livetest.GitHubEngine, root, fixtures string, settings livetest.GitHubTrialSettings, visibility livetest.ConsumerVisibility, releaseToken string) {
	t.Helper()

	trial, signing := prepareSigning(t, trial, engine, root, fixtures, settings)
	repo := trial.NewConsumerRepository(consumerRepositoryName(root, visibility.Name, settings.Event, settings.Workflow), visibility.Private)

	pullRequest, err := livetest.ParseGitHubTrialSettings(livetest.TrialPullRequest, nil)
	if err != nil {
		t.Fatal(err)
	}

	// The development phase snapshots the service snapshot's declaration,
	// staged as every forge's lifecycle stages it.
	trial = trial.WithFile(livetest.ServiceSnapshotDeclarationFile, livetest.ServiceSnapshotOverlay(t, root))
	tree := trial.PushConsumer(repo, root, fixtures, engine, signing.overrides)
	trial.SetVariable(repo, "FORGE_CI_REPOSITORY", engine.Owner+"/"+engine.Repository)
	trial.SetVariable(repo, "RELEASE_PLATFORMS", settings.Platforms)

	base := tree.Main

	t.Logf("consumer %s rendered from %s (%s) at %s, engine %s/%s at %s", repo, root, fixtures, base, engine.Owner, engine.Repository, engine.Commit)

	// Phase 1: the pull request.
	head := trial.PushChange(repo, tree, lifecycleBranch)
	number := trial.OpenPullRequest(repo, lifecycleBranch)
	review := judge(t, trial, repo, pullRequest.Workflow, "pull_request", head)
	requireInventory(t, trial, repo, livetest.GitHubInventory{Runs: 1})
	t.Logf("lifecycle phase 1 pull request: #%d, %s@%s into main@%s, run %d", number, lifecycleBranch, head, base, review.GetID())

	// Phase 2: the native merge.
	merge := trial.MergePullRequest(repo, number, head)
	tree = trial.FetchMain(repo, tree)
	trial.CheckMerge(repo, tree, lifecycleBranch, merge, base, head)
	t.Logf("lifecycle phase 2 merge: #%d merged natively, main at %s with parents %s and %s", number, merge, base, head)

	// Phase 3: the development snapshot on the merged main, of development.
	// The package it publishes outlives the repository, so its removal is
	// registered before the dispatch.
	repositoryID := trial.RepositoryID(repo)
	trial.RemoveSnapshotPackageAfter(repo, repositoryID)

	dispatched := trial.DispatchDevelopment(repo, livetest.GitHubDevelopmentWorkflow, map[string]any{"source-branch": lifecycleBranch})

	development := judge(t, trial, repo, livetest.GitHubDevelopmentWorkflow, "workflow_dispatch", merge)
	if dispatched != 0 && development.GetID() != dispatched {
		t.Fatal("judged development run differs from the dispatch response")
	}

	trial.CheckDevelopmentRelease(repo, merge, development.GetID())
	receipts := judgeSnapshot(t, trial, repo, visibility.Private, tree, development.GetID(), lifecycleBranch, head)
	requireInventory(t, trial, repo, livetest.GitHubInventory{Runs: 2})
	t.Logf("lifecycle phase 3 development: run %d dispatched on main@%s snapshots %s@%s as %s", development.GetID(), merge, lifecycleBranch, head, receiptRefs(receipts))

	// Phase 4: the signed release request at the merged main. The release's
	// authority is given now and not before.
	key := trial.NewTrialGPGKey()
	setReleaseSecrets(t, trial, repo, signing, key, releaseToken, false)

	request, final, err := livetest.ReleaseRequestTag(settings.Version)
	if err != nil {
		t.Fatal(err)
	}

	trial.RemoveReleasedPackagesAfter(repo, tree, settings.Workflow)
	trial.PushReleaseRequest(repo, tree, key, request, settings.Version)
	release := judge(t, trial, repo, settings.Workflow, "push", merge)

	assets := trial.CheckRelease(repo, tree, settings.Workflow, final, signing.trust, key)
	if visibility.Private {
		trial.CheckPrivateReleaseAssets(repo, assets)
	}

	checkImages(t, trial, repo, settings, final, engine, signing.trust, visibility.Private)
	t.Logf("lifecycle phase 4 release: %s requested at %s, run %d, %d assets", settings.Version, merge, release.GetID(), len(assets))

	// Phase 5: the repository holds the lifecycle and nothing else.
	requireInventory(t, trial, repo, livetest.GitHubInventory{Runs: 3, Tags: 2, Releases: 1})
	t.Logf("lifecycle complete: base %s, change %s, merge %s; runs %d, %d, %d", base, head, merge, review.GetID(), development.GetID(), release.GetID())
}

// requireInventory ends the lifecycle when the repository holds more or less
// than the phases so far made.
func requireInventory(t *testing.T, trial *livetest.GitHubTrial, repo string, want livetest.GitHubInventory) {
	t.Helper()

	if got := trial.RepositoryInventory(repo); got != want {
		t.Fatalf("%s holds %d runs, %d tags and %d releases; want %d, %d and %d", repo, got.Runs, got.Tags, got.Releases, want.Runs, want.Tags, want.Releases)
	}
}

// judgeSnapshot judges a development run by the snapshot oracle every
// forge's trial shares: the frozen plan, one receipt per planned image and
// each image in the repository's container package on GHCR, which a private
// consumer's is read from with the session's Packages token.
func judgeSnapshot(t *testing.T, trial *livetest.GitHubTrial, repo string, private bool, tree livetest.GitHubConsumerTree, runID int64, branch, head string) []snapshotimage.Receipt {
	t.Helper()

	receipts, _ := livetest.RequireSnapshotImages(livetest.Context(t), t, trial.SnapshotRun(repo, tree, runID, branch, head), trial.SnapshotRegistry(repo, private))

	return receipts
}

// receiptRefs names each receipt's image by its digest reference.
func receiptRefs(receipts []snapshotimage.Receipt) string {
	refs := make([]string, 0, len(receipts))
	for _, receipt := range receipts {
		refs = append(refs, receipt.Name+"="+receipt.Ref)
	}

	return strings.Join(refs, " ")
}
