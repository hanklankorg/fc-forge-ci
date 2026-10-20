// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	gitlabapi "gitlab.com/gitlab-org/api/client-go"

	"github.com/diggsweden/forge-ci/internal/retry"
)

// CreateGitLabMergeRequest tolerates GitLab's source-branch admission lag after
// a successful native push and branch read. Only a definite 400 rejection for
// that missing branch is retried; an uncertain POST outcome is never replayed.
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
