// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	gitlabapi "gitlab.com/gitlab-org/api/client-go"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

const (
	gitlabMergeHead   = "1111111111111111111111111111111111111111"
	gitlabMergeCommit = "2222222222222222222222222222222222222222"
)

// gitlabMergeServer serves one merge request: its status readbacks in order,
// the last one repeating, and the merge's response.
type gitlabMergeServer struct {
	reads  []string
	merge  func(http.ResponseWriter)
	gets   atomic.Int32
	merges atomic.Int32
}

func (server *gitlabMergeServer) handle(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")

	if request.Method == http.MethodPut {
		server.merges.Add(1)
		server.merge(response)

		return
	}

	index := min(int(server.gets.Add(1))-1, len(server.reads)-1)
	_, _ = io.WriteString(response, server.reads[index])
}

func gitlabMergeRequestJSON(sha, status, state, mergeCommit string) string {
	return fmt.Sprintf(`{"iid":7,"sha":%q,"detailed_merge_status":%q,"state":%q,"merge_commit_sha":%q}`, sha, status, state, mergeCommit)
}

func TestMergeGitLabMergeRequestWaitsForMergeableAndReadsTheMergeBack(t *testing.T) {
	t.Parallel()

	server := &gitlabMergeServer{
		reads: []string{
			gitlabMergeRequestJSON(gitlabMergeHead, "checking", "opened", ""),
			gitlabMergeRequestJSON(gitlabMergeHead, "ci_still_running", "opened", ""),
			gitlabMergeRequestJSON(gitlabMergeHead, "mergeable", "opened", ""),
			// GitLab records the merge commit after the merge has returned.
			gitlabMergeRequestJSON(gitlabMergeHead, "mergeable", "merged", gitlabMergeCommit),
		},
		merge: func(response http.ResponseWriter) {
			_, _ = io.WriteString(response, gitlabMergeRequestJSON(gitlabMergeHead, "mergeable", "merged", ""))
		},
	}

	merged, err := MergeGitLabMergeRequest(t.Context(), gitlabMergeClient(t, server), "trial/fc-example", 7, gitlabMergeHead)
	if err != nil {
		t.Fatal(err)
	}

	if merged.MergeCommitSHA != gitlabMergeCommit || merged.State != "merged" || server.merges.Load() != 1 || server.gets.Load() != 4 {
		t.Fatalf("merge=%+v merges=%d reads=%d", merged, server.merges.Load(), server.gets.Load())
	}
}

func TestMergeGitLabMergeRequestDoesNotMergeWhatGitLabRefuses(t *testing.T) {
	t.Parallel()

	for name, read := range map[string]string{
		"a pipeline that must pass": gitlabMergeRequestJSON(gitlabMergeHead, "ci_must_pass", "opened", ""),
		"a conflict":                gitlabMergeRequestJSON(gitlabMergeHead, "conflict", "opened", ""),
		"a moved head":              gitlabMergeRequestJSON("3333333333333333333333333333333333333333", "mergeable", "opened", ""),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			server := &gitlabMergeServer{reads: []string{read}, merge: func(response http.ResponseWriter) {
				response.WriteHeader(http.StatusOK)
			}}

			_, err := MergeGitLabMergeRequest(t.Context(), gitlabMergeClient(t, server), "trial/fc-example", 7, gitlabMergeHead)
			if !errors.Is(err, errs.ErrValidation) || server.merges.Load() != 0 || server.gets.Load() != 1 {
				t.Fatalf("err=%v merges=%d reads=%d; want one refused read and no merge", err, server.merges.Load(), server.gets.Load())
			}
		})
	}
}

func TestMergeGitLabMergeRequestNeverReplaysTheMerge(t *testing.T) {
	t.Parallel()

	server := &gitlabMergeServer{reads: []string{gitlabMergeRequestJSON(gitlabMergeHead, "mergeable", "opened", "")}, merge: func(response http.ResponseWriter) {
		response.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(response, `{"message":"SHA does not match HEAD of source branch"}`)
	}}

	_, err := MergeGitLabMergeRequest(t.Context(), gitlabMergeClient(t, server), "trial/fc-example", 7, gitlabMergeHead)
	if err == nil || !strings.Contains(err.Error(), "SHA does not match") || server.merges.Load() != 1 {
		t.Fatalf("err=%v merges=%d; want the refusal once", err, server.merges.Load())
	}
}

func gitlabMergeClient(t *testing.T, server *gitlabMergeServer) *gitlabapi.Client {
	t.Helper()

	httpServer := httptest.NewServer(http.HandlerFunc(server.handle))
	t.Cleanup(httpServer.Close)

	client, err := gitlabapi.NewClient("inert", gitlabapi.WithBaseURL(httpServer.URL+"/api/v4/"), gitlabapi.WithHTTPClient(httpServer.Client()))
	if err != nil {
		t.Fatal(err)
	}

	return client
}
