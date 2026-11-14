// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	gitlabapi "gitlab.com/gitlab-org/api/client-go"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/retry"
)

// WaitGitLabPushProcessed waits until GitLab has processed the push that
// moved branch to sha, not merely taken in the ref. GitLab processes a
// push later, in a background job, and that job looks up the open merge
// requests on the pushed branch before anything else: a merge request
// opened before the job runs is then refreshed by it and gets a second
// merge-request pipeline. The same job records the push as a project
// event after that lookup, so the event for sha on branch is the
// observable proof that the push can no longer touch a merge request
// opened from here on. Pushes that create more refs than GitLab's push
// event limit get no such event; the harness pushes one ref at a time.
func WaitGitLabPushProcessed(ctx context.Context, client *gitlabapi.Client, project, branch, sha string) error {
	return retry.Run(ctx, nil, 60, 2*time.Second, func() error {
		events, _, err := client.Events.ListProjectVisibleEvents(project, &gitlabapi.ListProjectVisibleEventsOptions{
			PerPage: 100,
			Action:  gitlabapi.Ptr(gitlabapi.PushedEventType),
		}, gitlabapi.WithContext(ctx))
		if err != nil {
			return fmt.Errorf("list the push events of %s: %w", project, err)
		}

		for _, event := range events {
			push := event.PushData
			if push.RefType == "branch" && push.Ref == branch && push.CommitTo == sha {
				return nil
			}
		}

		return fmt.Errorf("GitLab has not processed the push of %s to %s yet: %w", sha, branch, errs.ErrDependencyUnavailable)
	}, retry.WithBackoff(retry.Constant))
}

// CreateGitLabMergeRequest tolerates GitLab's source-branch admission lag after
// a push WaitGitLabPushProcessed has seen processed. Only a definite 400
// rejection for that missing branch is retried; an uncertain POST outcome is
// never replayed.
// The caller owns the client and exact disposable project coordinate.
func CreateGitLabMergeRequest(ctx context.Context, client *gitlabapi.Client, project string, options *gitlabapi.CreateMergeRequestOptions) (*gitlabapi.MergeRequest, error) {
	var created *gitlabapi.MergeRequest

	err := retry.Run(ctx, nil, 30, 2*time.Second, func() error {
		var err error

		created, _, err = client.MergeRequests.CreateMergeRequest(project, options, gitlabapi.WithContext(ctx),
			gitlabapi.WithRequestRetry(func(context.Context, *http.Response, error) (bool, error) { return false, nil }))
		if err == nil || gitLabSourceBranchNotAdmitted(err) {
			return err
		}

		return retry.Permanent(err)
	}, retry.WithBackoff(retry.Constant))

	return created, err
}

func gitLabSourceBranchNotAdmitted(err error) bool {
	var response *gitlabapi.ErrorResponse
	if !errors.As(err, &response) || !response.HasStatusCode(http.StatusBadRequest) {
		return false
	}

	var problem struct {
		Message map[string][]string `json:"message"`
	}
	if json.Unmarshal(response.Body, &problem) != nil {
		return false
	}

	branch := problem.Message["source_branch"]

	return len(problem.Message) == 1 && len(branch) == 1 && branch[0] == "does not exist"
}
