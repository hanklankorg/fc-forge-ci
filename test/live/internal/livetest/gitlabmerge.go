// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"time"

	gitlabapi "gitlab.com/gitlab-org/api/client-go"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/retry"
)

// gitLabMergePending are the merge statuses GitLab reports while it is still
// deciding whether a merge request can merge: its mergeability check and the
// head pipeline the project requires. Any other status is a decision, which
// waiting would not change.
var gitLabMergePending = []string{"checking", "unchecked", "preparing", "ci_still_running"}

// MergeGitLabMergeRequest merges a merge request natively, as a maintainer
// does in GitLab, told which head commit is meant: GitLab refuses the merge
// when the source branch has moved. It waits for GitLab to call the request
// mergeable first, which a project that requires a passing pipeline does only
// once the head pipeline has passed, merges once, and reads the merged
// request back. An uncertain outcome of the merge is never replayed; a
// request whose status is a refusal is reported, not waited for. It takes
// the caller's client, the one the scenario reads the project with, so a
// test can hand it a server of its own.
func MergeGitLabMergeRequest(ctx context.Context, client *gitlabapi.Client, project string, iid int64, head string) (*gitlabapi.MergeRequest, error) {
	if err := waitGitLabMergeable(ctx, client, project, iid, head); err != nil {
		return nil, err
	}

	merged, _, err := client.MergeRequests.AcceptMergeRequest(project, iid, &gitlabapi.AcceptMergeRequestOptions{
		SHA: &head, Squash: new(false), ShouldRemoveSourceBranch: new(false),
	}, gitlabapi.WithContext(ctx), gitlabapi.WithRequestRetry(func(context.Context, *http.Response, error) (bool, error) { return false, nil }))
	if err != nil {
		return nil, fmt.Errorf("merge !%d at %s: %w", iid, head, err)
	}

	return readBackGitLabMerge(ctx, client, project, iid, head, merged)
}

// waitGitLabMergeable waits while GitLab still decides, and returns the
// decision when it is a refusal.
func waitGitLabMergeable(ctx context.Context, client *gitlabapi.Client, project string, iid int64, head string) error {
	return retry.Run(ctx, nil, 90, 2*time.Second, func() error {
		current, _, err := client.MergeRequests.GetMergeRequest(project, iid, nil, gitlabapi.WithContext(ctx))
		if err != nil {
			return retry.Permanent(err)
		}

		switch {
		case current.SHA != head:
			return retry.Permanent(fmt.Errorf("merge request !%d is at %s, not the head %s the lifecycle reviewed: %w", iid, current.SHA, head, errs.ErrValidation))
		case current.DetailedMergeStatus == "mergeable":
			return nil
		case slices.Contains(gitLabMergePending, current.DetailedMergeStatus):
			return fmt.Errorf("merge request !%d is %s: %w", iid, current.DetailedMergeStatus, errs.ErrDependencyUnavailable)
		}

		return retry.Permanent(fmt.Errorf("merge request !%d cannot merge: %s: %w", iid, current.DetailedMergeStatus, errs.ErrValidation))
	}, retry.WithBackoff(retry.Constant))
}

// readBackGitLabMerge reads the merged request until GitLab records the
// merge commit, which it may write after the merge has returned.
func readBackGitLabMerge(ctx context.Context, client *gitlabapi.Client, project string, iid int64, head string, merged *gitlabapi.MergeRequest) (*gitlabapi.MergeRequest, error) {
	err := retry.Run(ctx, nil, 30, 2*time.Second, func() error {
		if merged != nil && merged.State == "merged" && merged.MergeCommitSHA != "" {
			return nil
		}

		var err error

		merged, _, err = client.MergeRequests.GetMergeRequest(project, iid, nil, gitlabapi.WithContext(ctx))
		if err != nil {
			return retry.Permanent(err)
		}

		if merged.State != "merged" && merged.State != "opened" && merged.State != "locked" {
			return retry.Permanent(fmt.Errorf("merge request !%d is %s after its merge: %w", iid, merged.State, errs.ErrValidation))
		}

		return fmt.Errorf("merge request !%d has no merge commit yet: %w", iid, errs.ErrDependencyUnavailable)
	}, retry.WithBackoff(retry.Constant))
	if err != nil {
		return nil, err
	}

	if merged.SHA != head {
		return nil, fmt.Errorf("merge request !%d merged %s, not the head %s: %w", iid, merged.SHA, head, errs.ErrValidation)
	}

	return merged, nil
}
