// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("unexpected method %s", r.Method)
				}
				w.Header().Set("Content-Type", "application/json")
				if calls.Add(1) == 1 {
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.body)
					return
				}
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, `{"iid":1,"source_branch":"topic","target_branch":"main"}`)
			}))
			defer server.Close()
			client, err := gitlabapi.NewClient("inert", gitlabapi.WithBaseURL(server.URL+"/api/v4/"), gitlabapi.WithHTTPClient(server.Client()))
			if err != nil {
				t.Fatal(err)
			}
			mr, err := CreateGitLabMergeRequest(t.Context(), client, "trial/fc-example", &gitlabapi.CreateMergeRequestOptions{
				SourceBranch: gitlabapi.Ptr("topic"), TargetBranch: gitlabapi.Ptr("main"), Title: gitlabapi.Ptr("test: branch admission"),
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
		SourceBranch: gitlabapi.Ptr("topic"), TargetBranch: gitlabapi.Ptr("main"), Title: gitlabapi.Ptr("test: lost response"),
	})
	if !errors.Is(err, cause) || transport.calls.Load() != 1 {
		t.Fatalf("uncertain POST was replayed or cause lost: calls=%d err=%v", transport.calls.Load(), err)
	}
}
