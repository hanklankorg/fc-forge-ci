// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-github/v91/github"
)

const engineBuildRun = "/repos/example-org/fc-forge-ci/actions/runs/77"

// engineBuildTrial is a trial whose engine repository has one finished Self
// Runtime Container run for commit, ended first. A rerun of its failed jobs
// is counted and gives the run a second attempt that ended second.
func engineBuildTrial(t *testing.T, tb TB, commit, first, second string) (*GitHubTrial, *atomic.Int32) {
	t.Helper()

	reruns := &atomic.Int32{}
	run := `{"id":77,"head_sha":"` + commit + `","status":"completed","conclusion":"%s","run_attempt":%d}`

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.Method + " " + request.URL.Path {
		case "GET /repos/example-org/fc-forge-ci/git/ref/heads/main":
			_, _ = fmt.Fprintf(response, `{"object":{"sha":%q}}`, commit)
		case "GET /repos/example-org/fc-forge-ci/actions/workflows/" + githubEngineWorkflow + "/runs":
			if request.URL.Query().Get("head_sha") != commit || request.URL.Query().Get("event") != "push" {
				t.Errorf("engine build lookup not pinned to the commit: %s", request.URL)
			}

			_, _ = fmt.Fprintf(response, `{"workflow_runs":[`+run+`]}`, first, 1)
		case "POST " + engineBuildRun + "/rerun-failed-jobs":
			reruns.Add(1)
			response.WriteHeader(http.StatusCreated)
		case "GET " + engineBuildRun:
			_, _ = fmt.Fprintf(response, run, second, 2)
		case "GET " + engineBuildRun + "/jobs":
			_, _ = response.Write([]byte(`{"total_count":0,"jobs":[]}`))
		case "GET /repos/example-org/fc-forge-ci/commits/" + commit + "/check-runs":
			_, _ = response.Write([]byte(`{"total_count":0,"check_runs":[]}`))
		default:
			t.Errorf("unexpected engine request: %s %s", request.Method, request.URL)
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	client, err := github.NewClient(github.WithHTTPClient(server.Client()), github.WithURLs(new(server.URL+"/"), nil))
	if err != nil {
		t.Fatal(err)
	}

	return &GitHubTrial{tb: tb, api: client, org: "example-org", prefix: "fc-"}, reruns
}

// Preparing the engine reruns the failed jobs of a build that failed or was
// cancelled, once, and judges the new attempt, whatever it ended as.
func TestPrepareRerunsAFailedEngineBuildOnce(t *testing.T) {
	t.Parallel()

	commit := strings.Repeat("a", 40)

	for name, test := range map[string]struct {
		first, second string
		reruns        int32
		attempt       int
	}{
		"failed, the rerun passes":    {runFailure, runSuccess, 1, 2},
		"cancelled, the rerun passes": {runCancelled, runSuccess, 1, 2},
		"failed, the rerun fails too": {runFailure, runFailure, 1, 2},
		"passed, nothing to rerun":    {runSuccess, runFailure, 0, 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			trial, reruns := engineBuildTrial(t, t, commit, test.first, test.second)

			want := test.first
			if test.reruns > 0 {
				want = test.second
			}

			run := trial.engineRun("fc-forge-ci", commit, true)
			if run.GetConclusion() != want || run.GetRunAttempt() != test.attempt || reruns.Load() != test.reruns {
				t.Fatalf("judged attempt %d ended %s after %d rerun requests; want attempt %d ended %s after %d",
					run.GetRunAttempt(), run.GetConclusion(), reruns.Load(), test.attempt, want, test.reruns)
			}
		})
	}
}

// Reusing a prepared engine only reads: a failed build ends the trial as it
// is, with no rerun asked for.
func TestReuseNeverRerunsAFailedEngineBuild(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	local := &GitHubTrial{tb: t}
	local.git(root, "init", "--quiet", "--object-format=sha1", "--initial-branch=main", ".")
	local.git(root, "-c", "user.name=Trial", "-c", "user.email=trial@example.invalid", "commit", "--quiet", "--allow-empty", "-m", "engine")
	commit := strings.TrimSpace(local.git(root, "rev-parse", "HEAD"))

	trial, reruns := engineBuildTrial(t, stopOnFatalTB{TB: t}, commit, runFailure, runSuccess)

	defer func() {
		failure, _ := recover().(acceptanceFailure)
		if !strings.Contains(string(failure), "ended failure") || reruns.Load() != 0 {
			t.Fatalf("reuse of a failed engine build ended %q after %d rerun requests", failure, reruns.Load())
		}
	}()

	trial.ReuseEngine(root)
}
