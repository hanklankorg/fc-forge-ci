// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	gitlabapi "gitlab.com/gitlab-org/api/client-go"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// RestrictGitLabJobToken configures the project's explicit self-scope, not just
// its cross-project allowlist. On GitLab 19.4, an absent self entry retains broad
// access. GraphQL exposes the policy update that the REST SDK does not.
// Registry writes use a separate authorization path and are not restricted here.
func RestrictGitLabJobToken(tb TB, target Target, repo string) {
	tb.Helper()
	requireAccepted(tb, target)

	if target.Forge != provider.ForgeGitLab || !inRunNamespace(target, repo) {
		tb.Fatalf("livetest: job-token policy requires a GitLab scratch project")
	}

	ctx, cancel := context.WithTimeout(Context(tb), timeoutAPI)
	defer cancel()

	project := RepoSlug(target, repo)
	endpoint := target.BaseURL() + "/api/v4/projects/" + url.PathEscape(project) + "/job_token_scope"

	var scope struct {
		InboundEnabled *bool `json:"inbound_enabled"`
	}
	if _, err := decode(ctx, target, http.MethodGet, endpoint, nil, &scope, http.StatusOK); err != nil {
		tb.Fatalf("livetest: read inbound job-token scope: %v", err)
	}

	if scope.InboundEnabled == nil || !*scope.InboundEnabled {
		tb.Fatalf("livetest: GitLab must enforce the project's inbound job-token allowlist")
	}

	const query = `mutation($project: ID!) {
  ciJobTokenScopeUpdatePolicies(input: {
    projectPath: $project, targetPath: $project, defaultPermissions: false,
    jobTokenPolicies: [READ_JOBS, READ_PACKAGES, READ_PIPELINES, READ_RELEASES, READ_REPOSITORIES]
  }) { errors }
}`

	var result struct {
		Errors []any `json:"errors"`
		Data   struct {
			Update *struct {
				Errors []string `json:"errors"`
			} `json:"ciJobTokenScopeUpdatePolicies"`
		} `json:"data"`
	}

	request := map[string]any{"query": query, "variables": map[string]string{"project": project}}
	if _, err := decode(ctx, target, http.MethodPost, target.BaseURL()+"/api/graphql", request, &result, http.StatusOK); err != nil {
		tb.Fatalf("livetest: set job-token self-scope: %v", err)
	}

	if len(result.Errors) != 0 || result.Data.Update == nil || len(result.Data.Update.Errors) != 0 {
		tb.Fatalf("livetest: GitLab refused the explicit read-only job-token self-scope")
	}
}

// RestrictGitLabReleaseBranch permits only the fixture deploy key to push main.
// GitLab may create default protection asynchronously after the first push, so
// update an existing rule in place instead of deleting and recreating it.
func RestrictGitLabReleaseBranch(tb TB, target Target, repo string, deployKey int64) {
	tb.Helper()
	requireAccepted(tb, target)

	if target.Forge != provider.ForgeGitLab || !inRunNamespace(target, repo) || deployKey <= 0 {
		tb.Fatalf("livetest: release protection requires a GitLab scratch project and deploy key")
	}

	client, err := gitlabapi.NewClient(target.Token, gitlabapi.WithBaseURL(target.BaseURL()+"/api/v4/"),
		gitlabapi.WithHTTPClient(HTTPClient(tb, target, timeoutAPI)))
	if err != nil {
		tb.Fatalf("livetest: create GitLab release policy client: %v", err)
	}

	ctx, cancel := context.WithTimeout(Context(tb), timeoutAPI)
	defer cancel()

	if err = restrictGitLabReleaseBranch(ctx, client, RepoSlug(target, repo), deployKey); err != nil {
		tb.Fatalf("livetest: restrict release branch: %v", err)
	}
}

func restrictGitLabReleaseBranch(ctx context.Context, client *gitlabapi.Client, project string, deployKey int64) error {
	push := []*gitlabapi.BranchPermissionOptions{{DeployKeyID: gitlabapi.Ptr(deployKey)}}

	_, response, err := client.ProtectedBranches.ProtectRepositoryBranches(project, &gitlabapi.ProtectRepositoryBranchesOptions{
		Name: gitlabapi.Ptr(defaultBranch), AllowedToPush: &push, AllowForcePush: gitlabapi.Ptr(false),
	}, gitlabapi.WithContext(ctx))
	if err != nil && (response == nil || response.StatusCode != http.StatusConflict) {
		return err
	}

	branch, _, readErr := client.ProtectedBranches.GetProtectedBranch(project, defaultBranch, gitlabapi.WithContext(ctx))
	if readErr != nil {
		return readErr
	}

	if releaseBranchRestricted(branch, deployKey) {
		return nil
	}

	// A conflict means the rule already exists; it is not a reason to retry
	// creation. Remove its observed access entries and add the key atomically.
	push, err = releaseBranchPushPermissions(branch, deployKey)
	if err != nil {
		return err
	}

	if _, _, err = client.ProtectedBranches.UpdateProtectedBranch(project, defaultBranch, &gitlabapi.UpdateProtectedBranchOptions{
		AllowedToPush: &push, AllowForcePush: gitlabapi.Ptr(false),
	}, gitlabapi.WithContext(ctx)); err != nil {
		return err
	}

	branch, _, err = client.ProtectedBranches.GetProtectedBranch(project, defaultBranch, gitlabapi.WithContext(ctx))
	if err != nil {
		return err
	}

	if !releaseBranchRestricted(branch, deployKey) {
		return fmt.Errorf("main protection is not deploy-key-only (expected key %d; %s): %w", deployKey, releaseBranchPermissionsDescription(branch), errs.ErrValidation)
	}

	return nil
}

func releaseBranchPushPermissions(branch *gitlabapi.ProtectedBranch, deployKey int64) ([]*gitlabapi.BranchPermissionOptions, error) {
	if branch == nil || branch.Name != defaultBranch || len(branch.PushAccessLevels) == 0 {
		return nil, fmt.Errorf("main protection is missing its name or push permissions: %w", errs.ErrValidation)
	}

	push := []*gitlabapi.BranchPermissionOptions{{DeployKeyID: gitlabapi.Ptr(deployKey)}}

	for _, access := range branch.PushAccessLevels {
		if access == nil || access.ID <= 0 {
			return nil, fmt.Errorf("main protection has a push permission without an identity: %w", errs.ErrValidation)
		}

		push = append(push, &gitlabapi.BranchPermissionOptions{ID: gitlabapi.Ptr(access.ID), Destroy: gitlabapi.Ptr(true)})
	}

	return push, nil
}

func releaseBranchPermissionsDescription(branch *gitlabapi.ProtectedBranch) string {
	if branch == nil {
		return "missing rule"
	}

	var grants []string

	for _, access := range branch.PushAccessLevels {
		if access == nil {
			grants = append(grants, "missing grant")
		} else {
			grants = append(grants, fmt.Sprintf("id=%d level=%d key=%d user=%d group=%d", access.ID, access.AccessLevel, access.DeployKeyID, access.UserID, access.GroupID))
		}
	}

	return fmt.Sprintf("force-push=%t grants=[%s]", branch.AllowForcePush, strings.Join(grants, "; "))
}

func releaseBranchRestricted(branch *gitlabapi.ProtectedBranch, deployKey int64) bool {
	return branch != nil && branch.Name == defaultBranch && !branch.AllowForcePush &&
		len(branch.PushAccessLevels) == 1 && branch.PushAccessLevels[0] != nil &&
		branch.PushAccessLevels[0].ID > 0 && branch.PushAccessLevels[0].DeployKeyID == deployKey &&
		branch.PushAccessLevels[0].UserID == 0 && branch.PushAccessLevels[0].GroupID == 0
}

// SignGitLabRequest signs an exact scratch commit using an ephemeral test key.
// Authentication stays in the existing closed Git environment, never the URL.
func SignGitLabRequest(tb TB, target Target, repo, commit, tag, keyPath string) {
	tb.Helper()
	requireAccepted(tb, target)

	if target.Forge != provider.ForgeGitLab || !inRunNamespace(target, repo) {
		tb.Fatalf("livetest: signed request requires a GitLab scratch project")
	}

	stage, err := os.MkdirTemp("", "livetest-gitlab-request-")
	if err != nil {
		tb.Fatalf("livetest: create request workspace: %v", err)
	}

	tb.Cleanup(func() { _ = os.RemoveAll(stage) })

	gitBase := strings.TrimSuffix(target.endpoint.GitURL(), "/")
	git := layerGit{target: target, env: ToolEnvironment(tb, target, layerGitEnvironment(gitBase, target.CredentialUsername, target.Token))}

	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()

	format := "sha1"
	if len(commit) == 64 {
		format = "sha256"
	}

	if _, err := git.run(ctx, stage, "init", gitQuiet, "--bare", "--object-format="+format); err != nil {
		tb.Fatalf("livetest: initialize request workspace: %v", err)
	}

	if _, err := git.run(ctx, stage, "fetch", gitQuiet, scratchRemote(target, repo), commit); err != nil {
		tb.Fatalf("livetest: fetch request source: %v", err)
	}

	consumer := Consumer{git: git, target: target, stage: stage}
	consumer.PushReleaseRequest(tb, repo, commit, tag, keyPath)
}
