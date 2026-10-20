// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v91/github"
)

func TestReuseGitHubEngineOnlyReadsPreparedSessionEngine(t *testing.T) {
	t.Parallel()
	trial := &GitHubTrial{tb: t, org: "hanklankorg", prefix: "fc-"}
	root := t.TempDir()
	trial.git(root, "init", "--quiet", "--object-format=sha1", "--initial-branch=main", ".")
	trial.git(root, "-c", "user.name=Trial", "-c", "user.email=trial@example.invalid", "commit", "--quiet", "--allow-empty", "-m", "engine")
	commit := strings.TrimSpace(trial.git(root, "rev-parse", "HEAD"))
	reads := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("engine reuse attempted mutation: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		reads[r.URL.Path]++
		var body any
		switch r.URL.Path {
		case "/repos/hanklankorg/fc-forge-ci/git/ref/heads/main", "/repos/hanklankorg/fc-forge-ci/git/ref/tags/" + GitHubPrerelease:
			body = map[string]any{"object": map[string]string{"sha": commit}}
		case "/repos/hanklankorg/fc-forge-ci/actions/workflows/" + githubEngineWorkflow + "/runs":
			if r.URL.Query().Get("head_sha") != commit || r.URL.Query().Get("event") != "push" {
				t.Errorf("engine build lookup not pinned: %s", r.URL)
			}
			body = map[string]any{"workflow_runs": []map[string]string{{"status": "completed", "conclusion": "success"}}}
		default:
			t.Errorf("unexpected engine request: %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	client, err := github.NewClient(github.WithHTTPClient(server.Client()), github.WithURLs(new(server.URL+"/"), nil))
	if err != nil {
		t.Fatal(err)
	}
	trial.api = client
	got := trial.ReuseEngine(root)
	if got != (GitHubEngine{Owner: trial.org, Repository: "fc-forge-ci", Commit: commit}) || len(reads) != 3 {
		t.Fatalf("reuse = %+v; reads = %v", got, reads)
	}
}

func TestGitHubWaitRunReturnsCancelledTerminalRun(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/actions/workflows/pullrequest.yml/runs") {
			t.Errorf("unexpected run lookup: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"workflow_runs":[{"id":42,"status":"completed","conclusion":"cancelled"}]}`))
	}))
	defer server.Close()
	client, err := github.NewClient(github.WithHTTPClient(server.Client()), github.WithURLs(new(server.URL+"/"), nil))
	if err != nil {
		t.Fatal(err)
	}
	trial := &GitHubTrial{tb: t, api: client, org: "hanklankorg"}
	run := trial.WaitRun("fc-cancelled", "pullrequest.yml", "pull_request", strings.Repeat("a", 40), time.Second)
	if run.GetID() != 42 || run.GetConclusion() != "cancelled" {
		t.Fatalf("cancelled terminal run was not returned: %+v", run)
	}
}

func TestGitHubConsumerPreservesTrackedIgnoredSource(t *testing.T) {
	t.Parallel()
	trial := &GitHubTrial{tb: t}
	root := t.TempDir()
	trial.git(root, "init", "--quiet", "--initial-branch=main", ".")
	for name, body := range map[string]string{
		".gitignore":      "bin/\n*.local\n",
		"src/bin/tool.rs": "fn main() {}\n",
		"test/live/github/workflows/pullrequest.yml": fixturePullRequest,
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	trial.git(root, "add", "--force", "--all")
	trial.commit(root, consumerBaseMessage, "")
	if err := os.WriteFile(filepath.Join(root, "secret.local"), []byte("not committed"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir, _ := trial.stageConsumer(root, "test/live/github", trialEngine, nil)
	if got := trial.git(dir, "show", "HEAD:src/bin/tool.rs"); got != "fn main() {}\n" {
		t.Fatalf("tracked ignored source changed: %q", got)
	}
	if got := trial.git(dir, "ls-tree", "-r", "--name-only", "HEAD"); strings.Contains(got, "secret.local") {
		t.Fatal("uncommitted ignored file entered the consumer snapshot")
	}
}

// The release-request tag is pushed only once GitHub lists every workflow just
// pushed as active: an event before that starts no run.
func TestGitHubListsAllRequiresEveryPushedWorkflowActive(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for _, name := range []string{"pullrequest.yml", "release.yml"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	workflow := func(path, state string) *github.Workflow {
		return &github.Workflow{Path: new(".github/workflows/" + path), State: new(state)}
	}

	for name, test := range map[string]struct {
		listed []*github.Workflow
		want   bool
	}{
		"none listed yet": {listed: nil, want: false},
		"one missing":     {listed: []*github.Workflow{workflow("release.yml", "active")}, want: false},
		"one disabled": {listed: []*github.Workflow{
			workflow("release.yml", "active"), workflow("pullrequest.yml", "disabled_manually"),
		}, want: false},
		"all active": {listed: []*github.Workflow{
			workflow("release.yml", "active"), workflow("pullrequest.yml", "active"),
		}, want: true},
	} {
		if got := githubListsAll(&github.Workflows{Workflows: test.listed}, entries); got != test.want {
			t.Errorf("%s: githubListsAll() = %v, want %v", name, got, test.want)
		}
	}
}
