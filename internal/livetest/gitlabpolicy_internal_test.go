// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	gitlabapi "gitlab.com/gitlab-org/api/client-go"
)

func TestGitLabReleaseBranchUpdatesDefaultProtectionWithoutAnUnprotectedWindow(t *testing.T) {
	t.Parallel()

	const existing = `{"name":"main","allow_force_push":true,"push_access_levels":[{"id":12,"access_level":40}]}`

	const restricted = `{"name":"main","push_access_levels":[{"id":99,"deploy_key_id":7}]}`

	for _, test := range []struct {
		name       string
		createCode int
		initial    string
		updated    string
		wantCalls  []string
		wantError  bool
	}{
		{"new_rule", http.StatusCreated, restricted, restricted, []string{"POST", "GET"}, false},
		{"first_push_created_rule", http.StatusConflict, existing, restricted, []string{"POST", "GET", "PATCH", "GET"}, false},
		{"already_restricted", http.StatusConflict, restricted, restricted, []string{"POST", "GET"}, false},
		{"unauthorized", http.StatusUnauthorized, "", "", []string{"POST"}, true},
		{"missing_permission_identity", http.StatusConflict, `{"name":"main","push_access_levels":[{}]}`, "", []string{"POST", "GET"}, true},
		{"missing_rule", http.StatusConflict, `{}`, "", []string{"POST", "GET"}, true},
		{"update_not_effective", http.StatusConflict, existing, existing, []string{"POST", "GET", "PATCH", "GET"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var calls []string

			current := test.initial
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				calls = append(calls, request.Method)

				w.Header().Set("Content-Type", "application/json")

				if !strings.HasPrefix(request.URL.EscapedPath(), "/api/v4/projects/rc%2Frc-release-consumer/protected_branches") {
					t.Errorf("unexpected project route: %s", request.URL.EscapedPath())
				}

				switch request.Method {
				case http.MethodPost:
					w.WriteHeader(test.createCode)
					_, _ = fmt.Fprint(w, `{}`)
				case http.MethodGet:
					_, _ = fmt.Fprint(w, current)
				case http.MethodPatch:
					var options gitlabapi.UpdateProtectedBranchOptions
					if err := json.NewDecoder(request.Body).Decode(&options); err != nil {
						t.Error(err)
					}

					want := `{"allow_force_push":false,"allowed_to_push":[{"deploy_key_id":7},{"id":12,"_destroy":true}]}`

					encoded, err := json.Marshal(options)
					if err != nil || string(encoded) != want {
						t.Errorf("protection update = %s (%v), want %s", encoded, err, want)
					}

					current = test.updated
					_, _ = fmt.Fprint(w, current)
				default:
					t.Errorf("unexpected protection mutation: %s", request.Method)
					w.WriteHeader(http.StatusMethodNotAllowed)
				}
			}))
			t.Cleanup(server.Close)

			client, err := gitlabapi.NewClient("fixture", gitlabapi.WithBaseURL(server.URL+"/api/v4/"))
			if err != nil {
				t.Fatal(err)
			}

			err = restrictGitLabReleaseBranch(t.Context(), client, "rc/rc-release-consumer", 7)
			if (err != nil) != test.wantError {
				t.Errorf("restrict release branch = %v, want error %t", err, test.wantError)
			}

			if !slices.Equal(calls, test.wantCalls) {
				t.Errorf("API operations = %v, want %v", calls, test.wantCalls)
			}
		})
	}
}
