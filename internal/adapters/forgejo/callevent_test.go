// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejo_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/forgejo"
	"github.com/diggsweden/forge-ci/internal/domain/validate"
)

func TestWorkflowCallOriginalEventBoundToAuthenticatedRun(t *testing.T) {
	for _, tc := range []struct {
		name, event, trigger string
		mutate               string
		allowed              bool
	}{
		{name: "push", event: "push", trigger: "push", allowed: true},
		{name: "manual nested call", event: "workflow_dispatch", trigger: "workflow_dispatch", allowed: true},
		{name: "pull request", event: "pull_request", trigger: "pull_request"},
		{name: "PR relabeled push", event: "pull_request_sync", trigger: "push"},
		{name: "MR", event: "merge_request_event", trigger: "merge_request_event"},
		{name: "unknown", event: "future_event", trigger: "future_event"},
		{name: "ambiguous call", event: "workflow_call", trigger: "workflow_call"},
		{name: "missing trigger", event: "push"},
		{name: "disagreement", event: "push", trigger: "workflow_dispatch"},
		{name: "wrong run", event: "push", trigger: "push", mutate: "id"},
		{name: "wrong repo", event: "push", trigger: "push", mutate: "repository"},
		{name: "wrong commit", event: "push", trigger: "push", mutate: "commit_sha"},
		{name: "no credentials", event: "push", trigger: "push", mutate: "token"},
		{name: "missing run", event: "push", trigger: "push", mutate: "run"},
		{name: "missing SHA", event: "push", trigger: "push", mutate: "sha"},
		{name: "API forbidden", event: "push", trigger: "push", mutate: "forbidden"},
		{name: "empty webhook event push", trigger: "push", allowed: true},
		{name: "empty webhook event manual", trigger: "workflow_dispatch", allowed: true},
		{name: "absent webhook event manual", trigger: "workflow_dispatch", mutate: "absent_event", allowed: true},
		{name: "empty webhook PR", trigger: "pull_request"},
		{name: "empty webhook PR variant", trigger: "pull_request_sync"},
		{name: "empty webhook MR", trigger: "merge_request_event"},
		{name: "empty webhook unknown trigger", trigger: "future_event"},
		{name: "both empty"},
		{name: "both absent", mutate: "absent_both"},
		{name: "null trigger", mutate: "null_trigger"},
		{name: "absent trigger", event: "push", mutate: "absent_trigger"},
		{name: "object trigger", mutate: "object_trigger"},
		{name: "null webhook", trigger: "push", mutate: "null_event"},
		{name: "number webhook", trigger: "push", mutate: "number_event"},
		{name: "empty webhook fork", trigger: "workflow_dispatch", mutate: "fork"},
		{name: "null repository", trigger: "push", mutate: "null_repository"},
		{name: "absent repository", trigger: "push", mutate: "absent_repository"},
		{name: "null source SHA", trigger: "push", mutate: "null_sha"},
		{name: "null run ID", trigger: "push", mutate: "null_id"},
		{name: "fork flag", event: "push", trigger: "push", mutate: "fork"},
		{name: "wrong numeric repo", event: "push", trigger: "push", mutate: "repository_id"},
		{name: "runner compatibility aliases", event: "push", trigger: "push", mutate: "aliases", allowed: true},
		{name: "malformed response", event: "push", trigger: "push", mutate: "malformed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sha := strings.Repeat("a", 40)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/api/v1/repos/example/app/actions/runs/250" || r.Header.Get("Authorization") != "token fixture-token" {
					t.Errorf("unbound request: %s", r.URL.Path)
				}
				if tc.mutate == "forbidden" {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				if tc.mutate == "malformed" {
					_, _ = w.Write([]byte(`{"id":`))
					return
				}
				body := map[string]any{"id": 250, "repository": map[string]any{"id": 7, "full_name": "example/app"}, "commit_sha": sha, "event": tc.event, "trigger_event": tc.trigger}
				switch tc.mutate {
				case "id":
					body["id"] = 251
				case "repository":
					body["repository"] = map[string]any{"id": 8, "full_name": "other/app"}
				case "commit_sha":
					body["commit_sha"] = strings.Repeat("b", 40)
				case "fork":
					body["is_fork_pull_request"] = true
				case "absent_event":
					delete(body, "event")
				case "absent_trigger":
					delete(body, "trigger_event")
				case "absent_both":
					delete(body, "event")
					delete(body, "trigger_event")
				case "null_trigger":
					body["trigger_event"] = nil
				case "object_trigger":
					body["trigger_event"] = map[string]string{"event": "push"}
				case "null_event":
					body["event"] = nil
				case "number_event":
					body["event"] = 1
				case "null_repository":
					body["repository"] = nil
				case "absent_repository":
					delete(body, "repository")
				case "null_sha":
					body["commit_sha"] = nil
				case "null_id":
					body["id"] = nil
				}
				_ = json.NewEncoder(w).Encode(body)
			}))
			defer server.Close()
			env := map[string]string{"FORGEJO_ACTIONS": "true", "FORGEJO_SERVER_URL": server.URL, "FORGEJO_REPOSITORY": "example/app", "FORGEJO_REPOSITORY_ID": "7", "FORGEJO_SHA": sha, "FORGEJO_RUN_ID": "250", "FORGEJO_EVENT_NAME": "workflow_call", "FORGEJO_TOKEN": "fixture-token"}
			switch tc.mutate {
			case "token":
				delete(env, "FORGEJO_TOKEN")
			case "run":
				delete(env, "FORGEJO_RUN_ID")
			case "sha":
				delete(env, "FORGEJO_SHA")
			case "repository_id":
				env["FORGEJO_REPOSITORY_ID"] = "8"
			case "aliases":
				for _, suffix := range []string{"SERVER_URL", "REPOSITORY", "REPOSITORY_ID", "SHA", "RUN_ID", "EVENT_NAME", "TOKEN"} {
					env["GITHUB_"+suffix] = env["FORGEJO_"+suffix]
					delete(env, "FORGEJO_"+suffix)
				}
			}
			p := &forgejo.Provider{Env: func(key string) string { return env[key] }, HTTPClient: server.Client()}
			event, err := p.ResolveWorkflowCallEvent(t.Context())
			if err == nil {
				err = validate.RequireAllowedEvent(event, nil)
			}
			if (err == nil) != tc.allowed {
				t.Fatalf("event %q, error %v, want allowed %t", event, err, tc.allowed)
			}
			if (tc.mutate == "token" || tc.mutate == "run" || tc.mutate == "sha") && calls != 0 {
				t.Fatal("incomplete identity reached API")
			}
		})
	}
}

func TestWorkflowCallManualPilotResponseWithoutWebhookEvent(t *testing.T) {
	const sha = "41b8e20eecfbaadcde110e580f6f0a5af42888ba"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/forgeci/fci-snapshot-artifact/actions/runs/252" || r.Header.Get("Authorization") != "token fixture-token" {
			t.Errorf("unbound pilot metadata request: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":252,"event":"","trigger_event":"workflow_dispatch","is_fork_pull_request":false,"commit_sha":"41b8e20eecfbaadcde110e580f6f0a5af42888ba","repository":{"id":555,"full_name":"forgeci/fci-snapshot-artifact"}}`))
	}))
	defer server.Close()
	env := map[string]string{"FORGEJO_ACTIONS": "true", "FORGEJO_SERVER_URL": server.URL, "FORGEJO_REPOSITORY": "forgeci/fci-snapshot-artifact", "FORGEJO_REPOSITORY_ID": "555", "FORGEJO_SHA": sha, "FORGEJO_RUN_ID": "252", "FORGEJO_EVENT_NAME": "workflow_call", "FORGEJO_TOKEN": "fixture-token"}
	p := &forgejo.Provider{Env: func(key string) string { return env[key] }, HTTPClient: server.Client()}
	event, err := p.ResolveWorkflowCallEvent(t.Context())
	if err != nil || event != "workflow_dispatch" {
		t.Fatalf("authenticated manual pilot: event=%q err=%v", event, err)
	}
	if err := validate.RequireAllowedEvent(event, nil); err != nil {
		t.Fatal(err)
	}
}
