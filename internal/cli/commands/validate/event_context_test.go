// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package validate_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	validatecmd "github.com/diggsweden/forge-ci/internal/cli/commands/validate"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

func TestEventContextForgejoWorkflowCallUsesOriginalTrigger(t *testing.T) {
	for _, event := range []string{"push", "workflow_dispatch", "pull_request", "merge_request_event", "workflow_call", ""} {
		t.Run(event, func(t *testing.T) {
			testenv.New(t)
			sha := strings.Repeat("a", 40)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/api/v1/repos/example/app/actions/runs/250" || r.Header.Get("Authorization") != "token fixture-token" {
					t.Errorf("wrong authenticated run request: %s", r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"id": 250, "repository": map[string]any{"id": 7, "full_name": "example/app"}, "commit_sha": sha, "event": event, "trigger_event": event})
			}))
			defer server.Close()
			for key, value := range map[string]string{"FORGEJO_ACTIONS": "true", "FORGE_CI_PROVIDER": "forgejo", "FORGEJO_SERVER_URL": server.URL, "FORGEJO_REPOSITORY": "example/app", "FORGEJO_RUN_ID": "250", "FORGEJO_SHA": sha, "FORGEJO_EVENT_NAME": "workflow_call", "FORGEJO_TOKEN": "fixture-token"} {
				t.Setenv(key, value)
			}
			// Explicit event-name cannot disguise PR origins of a reusable run.
			err := validatecmd.New().Run(t.Context(), []string{"validate", "event-context", "--event-name", "push"})
			allowed := event == "push" || event == "workflow_dispatch"
			if (err == nil) != allowed || calls != 1 {
				t.Fatalf("original %q: err=%v API calls=%d", event, err, calls)
			}
		})
	}
}

func TestGitLabSnapshotEventPolicy(t *testing.T) {
	const allowed = "push,workflow_dispatch,api,schedule,parent_pipeline"
	for _, source := range []string{"push", "web", "api", "schedule", "parent_pipeline", "trigger", "pipeline", "merge_request_event", "unknown", ""} {
		for _, marker := range []string{"", "CI_MERGE_REQUEST_ID", "CI_MERGE_REQUEST_IID", "CI_MERGE_REQUEST_REF_PATH", "CI_MERGE_REQUEST_EVENT_TYPE", "CI_MERGE_REQUEST_SOURCE_PROJECT_ID", "CI_MERGE_REQUEST_TARGET_PROJECT_ID", "CI_MERGE_REQUEST_SOURCE_BRANCH_NAME", "CI_MERGE_REQUEST_TARGET_BRANCH_NAME"} {
			t.Run(source+"/"+marker, func(t *testing.T) {
				testenv.New(t)
				t.Setenv("FORGE_CI_PROVIDER", "gitlab")
				t.Setenv("CI_PIPELINE_SOURCE", source)
				if marker != "" {
					t.Setenv(marker, "present")
				}
				err := validatecmd.New().Run(t.Context(), []string{"validate", "event-context", "--event-name", "", "--allowed-events", allowed})
				want := marker == "" && (source == "push" || source == "web" || source == "api" || source == "schedule" || source == "parent_pipeline")
				if (err == nil) != want {
					t.Fatalf("source %q marker %q: err=%v, allowed=%v", source, marker, err, want)
				}
			})
		}
	}
	for _, source := range []string{"api", "trigger", "pipeline", "parent_pipeline"} {
		t.Run("default-denies-"+source, func(t *testing.T) {
			testenv.New(t)
			t.Setenv("FORGE_CI_PROVIDER", "gitlab")
			t.Setenv("CI_PIPELINE_SOURCE", source)
			if err := validatecmd.New().Run(t.Context(), []string{"validate", "event-context"}); err == nil {
				t.Fatal("global default admitted external/child source")
			}
		})
	}
}
