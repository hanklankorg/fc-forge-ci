// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gitlabapi "gitlab.com/gitlab-org/api/client-go"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/livetest"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// A real component include must resolve its own commit, download its recorded
// binary and check out the consumer's pipeline commit in a clean job. The two
// scratch projects have different histories, so checking out the engine instead
// of the consumer cannot satisfy the commit assertion.
func TestGitLabCheck_ChecksOutTheConsumerPipelineCommit(t *testing.T) {
	runGitLabCheck(t, false)
}

func TestGitLabCheck_ChecksOutPrivateConsumerWithJobToken(t *testing.T) {
	runGitLabCheck(t, true)
}

func runGitLabCheck(t *testing.T, private bool) {
	t.Helper()

	if !livetest.Requires(t, provider.ForgeGitLab, livetest.NeedsInRunner) {
		livetest.NotCoveredf(t, "the selected endpoints provide no GitLab runner for component execution")
	}

	target := livetest.Accept(t, provider.ForgeGitLab)
	engine := livetest.NewScratchRepo(t, target, "check-engine")
	root := reporoot.Path(t)

	client, err := gitlabapi.NewClient(target.Token,
		gitlabapi.WithBaseURL(target.BaseURL()+"/api/v4/"),
		gitlabapi.WithHTTPClient(livetest.HTTPClient(t, target, time.Minute)))
	if err != nil {
		t.Fatal(err)
	}

	paths := []string{"templates/check.yml", ".forgejo/bin/lib-forge-ci.sh", ".forgejo/bin/forge-ci-linux-amd64.sha256"}
	actions := make([]*gitlabapi.CommitActionOptions, 0, len(paths))

	for _, path := range paths {
		body, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if readErr != nil {
			t.Fatal(readErr)
		}

		actions = append(actions, &gitlabapi.CommitActionOptions{
			Action: gitlabapi.Ptr(gitlabapi.FileCreate), FilePath: gitlabapi.Ptr(path), Content: gitlabapi.Ptr(string(body)),
		})
	}

	ctx, cancel := context.WithTimeout(livetest.Context(t), time.Minute)
	defer cancel()

	commit, _, err := client.Commits.CreateCommit(livetest.RepoSlug(target, engine), &gitlabapi.CreateCommitOptions{
		Branch: gitlabapi.Ptr("main"), CommitMessage: gitlabapi.Ptr("livetest: stage the check component"), Actions: actions,
	}, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}

	publishGitLabBootstrap(t, target, engine)

	var consumer string
	if private {
		// Exercise the other object format too: the component must derive it
		// from the pipeline SHA without a private project-metadata request.
		consumer = livetest.NewBareScratchRepo(t, target, "check-consumer", "sha256", true)
		visibilityContext, visibilityCancel := context.WithTimeout(livetest.Context(t), time.Minute)
		project, _, visibilityErr := client.Projects.EditProject(livetest.RepoSlug(target, consumer), &gitlabapi.EditProjectOptions{
			CIPushRepositoryForJobTokenAllowed: gitlabapi.Ptr(false),
		}, gitlabapi.WithContext(visibilityContext))

		visibilityCancel()

		if visibilityErr != nil || project == nil || project.Visibility != gitlabapi.PrivateVisibility {
			t.Fatalf("private consumer visibility was not configured: %v", visibilityErr)
		}
	} else {
		consumer = livetest.NewScratchRepo(t, target, "check-consumer")
	}

	workflow := `include:
  - component: ` + strings.TrimPrefix(target.BaseURL(), "https://") + "/" + livetest.RepoSlug(target, engine) + "/check@" + commit.ID + `
    inputs:
      forge-ci-repository: ` + livetest.RepoSlug(target, engine) + `
      job-image: ` + livetest.ProbeImage + `
      commands:
        - 'test "$(git rev-parse HEAD)" = "$CI_COMMIT_SHA"'
        - 'test "$(git rev-parse refs/heads/main)" = "$CI_COMMIT_SHA"'
        - 'test -f .gitlab-ci.yml'
        - 'test "$(git config --get remote.origin.url)" = "$CI_SERVER_URL/$CI_PROJECT_PATH.git"'
        - '! git config --local --get-regexp "^http\\..*extraheader$"'
        - 'forge-ci --version'
`
	if private {
		workflow += `        - 'test "$CI_PROJECT_VISIBILITY" = private'
        - 'test "${CI_JOB_TOKEN:+present}" = present'
`
	}

	if conclusion := livetest.RunWorkflow(t, target, consumer, "check-component", workflow); conclusion != "success" {
		t.Fatalf("the native check component concluded %q", conclusion)
	}
}
