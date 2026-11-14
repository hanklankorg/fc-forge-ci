// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	gitlabapi "gitlab.com/gitlab-org/api/client-go"
)

func TestCreateGitLabMergeRequestRetriesOnlyDefiniteBranchRejection(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, body    string
		status, calls int
		wantSuccess   bool
	}{
		{"branch admission lag", `{"message":{"source_branch":["does not exist"]}}`, 400, 2, true},
		{"other validation", `{"message":{"source_branch":["already has an open merge request"]}}`, 400, 1, false},
		{"additional validation", `{"message":{"source_branch":["does not exist"],"target_branch":["invalid"]}}`, 400, 1, false},
		{"uncertain server outcome", `{"message":{"source_branch":["does not exist"]}}`, 500, 1, false},
		{"malformed rejection", `not json`, 400, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var calls atomic.Int32

			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("unexpected method %s", r.Method)
				}

				response.Header().Set("Content-Type", "application/json")

				if calls.Add(1) == 1 {
					response.WriteHeader(tc.status)
					_, _ = io.WriteString(response, tc.body)

					return
				}

				response.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(response, `{"iid":1,"source_branch":"topic","target_branch":"main"}`)
			}))
			defer server.Close()

			client, err := gitlabapi.NewClient("inert", gitlabapi.WithBaseURL(server.URL+"/api/v4/"), gitlabapi.WithHTTPClient(server.Client()))
			if err != nil {
				t.Fatal(err)
			}

			mr, err := CreateGitLabMergeRequest(t.Context(), client, "trial/fc-example", &gitlabapi.CreateMergeRequestOptions{
				SourceBranch: new("topic"), TargetBranch: new("main"), Title: new("test: branch admission"),
			})
			if (err == nil) != tc.wantSuccess || int(calls.Load()) != tc.calls {
				t.Fatalf("success=%v calls=%d; want success=%v calls=%d; err=%v", err == nil, calls.Load(), tc.wantSuccess, tc.calls, err)
			}

			if tc.wantSuccess && (mr == nil || mr.IID != 1) {
				t.Fatalf("missing created merge request: %+v", mr)
			}
		})
	}
}

type failedMRTransport struct {
	calls atomic.Int32
	err   error
}

func (transport *failedMRTransport) RoundTrip(*http.Request) (*http.Response, error) {
	transport.calls.Add(1)

	return nil, transport.err
}

func TestCreateGitLabMergeRequestDoesNotReplayLostResponse(t *testing.T) {
	t.Parallel()

	cause := errors.New("response lost after POST")
	transport := &failedMRTransport{err: cause}

	client, err := gitlabapi.NewClient("inert", gitlabapi.WithBaseURL("https://gitlab.invalid/api/v4/"), gitlabapi.WithHTTPClient(&http.Client{Transport: transport}))
	if err != nil {
		t.Fatal(err)
	}

	_, err = CreateGitLabMergeRequest(t.Context(), client, "trial/fc-example", &gitlabapi.CreateMergeRequestOptions{
		SourceBranch: new("topic"), TargetBranch: new("main"), Title: new("test: lost response"),
	})
	if !errors.Is(err, cause) || transport.calls.Load() != 1 {
		t.Fatalf("uncertain POST was replayed or cause lost: calls=%d err=%v", transport.calls.Load(), err)
	}
}

// GitLab run 6: the branch was readable at once, but GitLab processed the
// push in a later job, refreshed the merge request opened meanwhile, and
// gave it a second pipeline. The fake keeps that order: the push event
// appears only when the push is processed, after events for other refs and
// for the branch's earlier commit, and a merge request opened before then
// is the race.
func TestWaitGitLabPushProcessedPrecedesTheMergeRequest(t *testing.T) {
	t.Parallel()

	const (
		branch = "topic"
		head   = "2222222222222222222222222222222222222222"
	)

	other := `{"push_data":{"ref_type":"tag","ref":"topic","commit_to":"` + head + `"}},` +
		`{"push_data":{"ref_type":"branch","ref":"main","commit_to":"` + head + `"}},` +
		`{"push_data":{"ref_type":"branch","ref":"topic","commit_to":"1111111111111111111111111111111111111111"}}`

	var (
		mu        sync.Mutex
		polls     int
		processed bool
		raced     bool
	)

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		response.Header().Set("Content-Type", "application/json")

		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v4/projects/trial/fc-example/events":
			if action := request.URL.Query().Get("action"); action != "pushed" {
				t.Errorf("events read with action %q", action)
			}

			polls++

			switch polls {
			case 1:
				_, _ = io.WriteString(response, `[]`)
			case 2:
				_, _ = io.WriteString(response, `[`+other+`]`)
			default:
				processed = true
				_, _ = io.WriteString(response, `[{"push_data":{"ref_type":"branch","ref":"topic","commit_to":"`+head+`"}},`+other+`]`)
			}
		case request.Method == http.MethodPost && request.URL.Path == "/api/v4/projects/trial/fc-example/merge_requests":
			raced = !processed

			response.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(response, `{"iid":1,"source_branch":"topic","target_branch":"main"}`)
		default:
			t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := gitlabapi.NewClient("inert", gitlabapi.WithBaseURL(server.URL+"/api/v4/"), gitlabapi.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}

	if err = WaitGitLabPushProcessed(t.Context(), client, "trial/fc-example", branch, head); err != nil {
		t.Fatal(err)
	}

	if _, err = CreateGitLabMergeRequest(t.Context(), client, "trial/fc-example", &gitlabapi.CreateMergeRequestOptions{
		SourceBranch: new(branch), TargetBranch: new("main"), Title: new("test: processed push"),
	}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()

	if raced || polls != 3 {
		t.Fatalf("merge request opened before GitLab processed the push: raced=%v polls=%d", raced, polls)
	}
}
