// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

const (
	mergeTestHead  = "1111111111111111111111111111111111111111"
	mergeTestMerge = "2222222222222222222222222222222222222222"
)

// A native merge names the head commit it is meant for and is read back
// merged and closed with a merge commit; a forge that reports otherwise, or
// refuses the merge, ends the scenario.
func TestMergePullRequest_NamesTheHeadAndReadsTheMergeBack(t *testing.T) {
	for name, test := range map[string]struct {
		mergeStatus int
		readBack    string
		fatal       string
	}{
		"merged":          {http.StatusOK, `{"number":7,"state":"closed","merged":true,"merge_commit_sha":"` + mergeTestMerge + `"}`, ""},
		"branch moved":    {http.StatusConflict, `{}`, "merge pull request #7 at " + mergeTestHead},
		"not merged":      {http.StatusOK, `{"number":7,"state":"open","merged":false}`, "want it merged and closed"},
		"no merge commit": {http.StatusOK, `{"number":7,"state":"closed","merged":true,"merge_commit_sha":"short"}`, "want it merged and closed"},
	} {
		t.Run(name, func(t *testing.T) {
			var merges []map[string]any

			server, ca := labTLSServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")

				switch {
				case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/repos/fixture/scratch/pulls/7/merge"):
					var body map[string]any
					if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
						t.Error(err)
					}

					merges = append(merges, body)

					writer.WriteHeader(test.mergeStatus)
				case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/repos/fixture/scratch/pulls/7"):
					_, _ = io.WriteString(writer, test.readBack)
				default:
					t.Errorf("unexpected request: %s %s", request.Method, request.URL.Path)
					writer.WriteHeader(http.StatusNotFound)
				}
			}))

			original := http.DefaultTransport
			transport := original.(*http.Transport).Clone()
			transport.DialContext = (&dialRecorder{to: server.Listener.Addr().String()}).dial
			http.DefaultTransport = transport

			defer func() { http.DefaultTransport = original }()
			defer transport.CloseIdleConnections()

			target := Target{Forge: provider.ForgeForgejo, Host: labHost, Owner: "fixture", CAFile: "captured", caPEM: ca, accepted: true}

			var (
				merged  PullRequest
				failure acceptanceFailure
				stopped bool
			)

			func() {
				defer func() { failure, stopped = recover().(acceptanceFailure) }()

				merged = MergePullRequest(stopOnFatalTB{TB: &recordingTB{}}, target, "scratch", 7, mergeTestHead)
			}()

			if len(merges) != 1 || merges[0]["Do"] != "merge" || merges[0]["head_commit_id"] != mergeTestHead ||
				merges[0]["delete_branch_after_merge"] != false || merges[0]["force_merge"] != false || merges[0]["merge_when_checks_succeed"] != false {
				t.Fatalf("the forge was asked %v, want one merge of the head %s", merges, mergeTestHead)
			}

			if test.fatal == "" {
				if stopped || merged.MergeCommitSHA != mergeTestMerge || !merged.Merged || merged.State != "closed" {
					t.Fatalf("merged = %+v (stopped %t: %s), want the merge commit read back", merged, stopped, failure)
				}

				return
			}

			if !stopped || !strings.Contains(string(failure), test.fatal) || merged.MergeCommitSHA != "" {
				t.Fatalf("a refused merge stopped %t with %q and returned %+v, want the refusal %q", stopped, failure, merged, test.fatal)
			}
		})
	}
}
