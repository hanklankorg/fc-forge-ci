// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v91/github"
)

func TestReuseGitHubEngineOnlyReadsPreparedSessionEngine(t *testing.T) {
	t.Parallel()
	trial := &GitHubTrial{tb: t, org: "example-org", prefix: "fc-"}
	root := t.TempDir()
	trial.git(root, "init", "--quiet", "--object-format=sha1", "--initial-branch=main", ".")
	trial.git(root, "-c", "user.name=Trial", "-c", "user.email=trial@example.invalid", "commit", "--quiet", "--allow-empty", "-m", "engine")
	commit := strings.TrimSpace(trial.git(root, "rev-parse", "HEAD"))
	reads := map[string]int{}

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Errorf("engine reuse attempted mutation: %s %s", request.Method, request.URL)
			response.WriteHeader(http.StatusMethodNotAllowed)

			return
		}

		reads[request.URL.Path]++

		var body any

		switch request.URL.Path {
		case "/repos/example-org/fc-forge-ci/git/ref/heads/main", "/repos/example-org/fc-forge-ci/git/ref/tags/" + GitHubPrerelease:
			body = map[string]any{"object": map[string]string{"sha": commit}}
		case "/repos/example-org/fc-forge-ci/actions/workflows/" + githubEngineWorkflow + "/runs":
			if request.URL.Query().Get("head_sha") != commit || request.URL.Query().Get("event") != "push" {
				t.Errorf("engine build lookup not pinned: %s", request.URL)
			}

			body = map[string]any{"workflow_runs": []map[string]string{{"status": "completed", "conclusion": "success"}}}
		default:
			t.Errorf("unexpected engine request: %s", request.URL)
			response.WriteHeader(http.StatusNotFound)

			return
		}

		if err := json.NewEncoder(response).Encode(body); err != nil {
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
	if got.admin == nil || got.admin.commit != commit {
		t.Fatal("reused engine lost the controller's administrator policy")
	}

	if _, present := got.admin.values["enforce-reproducible-builds"]; !present {
		t.Fatal("reused engine lost an administrator setting")
	}

	got.admin = nil
	if got != (GitHubEngine{Owner: trial.org, Repository: "fc-forge-ci", Commit: commit, Namespace: "ghcr.io/example-org/fc-forge-ci"}) || len(reads) != 3 {
		t.Fatalf("reuse = %+v; reads = %v", got, reads)
	}
}

func TestGitHubWaitRunReturnsCancelledTerminalRun(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/actions/workflows/pullrequest.yml/runs") {
			t.Errorf("unexpected run lookup: %s %s", r.Method, r.URL.Path)
			response.WriteHeader(http.StatusNotFound)

			return
		}

		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"workflow_runs":[{"id":42,"status":"completed","conclusion":"cancelled"}]}`))
	}))
	defer server.Close()

	client, err := github.NewClient(github.WithHTTPClient(server.Client()), github.WithURLs(new(server.URL+"/"), nil))
	if err != nil {
		t.Fatal(err)
	}

	trial := &GitHubTrial{tb: t, api: client, org: "example-org"}

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

// These are the native release wrapper and policy coordinates from Gommitlint's
// migration draft. The hosted fixture must not leave their immutable-pin guard
// checking a different identity than the native workflows it reads.
func TestGitHubConsumerBindsNativeDraftPolicyInputs(t *testing.T) {
	t.Parallel()

	const marker = "PIN_FORGE_CI_SHA"
	const native = `jobs:
  token-preflight:
    steps:
      - uses: https://codefloe.com/itiquette/forge-ci/.forgejo/actions/verify-forgejo-token@PIN_FORGE_CI_SHA
  prepare:
    uses: itiquette/forge-ci/.forgejo/workflows/prepare-release.yml@PIN_FORGE_CI_SHA
    with:
      forge-ci-repository: itiquette/forge-ci
`
	const policy = `package policy_test
const (
  forgeCIPin = "PIN_FORGE_CI_SHA"
  layerSetupAction = "https://codefloe.com/itiquette/forge-ci/.forgejo/actions/setup-forge-ci@PIN_FORGE_CI_SHA"
  layerPrepareWorkflow = "itiquette/forge-ci/.forgejo/workflows/prepare-release.yml@PIN_FORGE_CI_SHA"
)
`
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			trial := &GitHubTrial{tb: t}
			root := t.TempDir()
			trial.git(root, "init", "--quiet", "--object-format="+format, "--initial-branch=main", ".")
			files := map[string]string{
				".forgejo/workflows/release.yml":             native,
				"test/policy/workflow_policy_test.go":        policy,
				"test/live/github/workflows/pullrequest.yml": fixturePullRequest,
			}
			for path, text := range files {
				writeLayerFile(t, root, path, text)
			}
			trial.git(root, "add", "--all")
			trial.commit(root, consumerBaseMessage, "")
			head := trial.git(root, "rev-parse", "HEAD")
			dir, _ := trial.stageConsumer(root, "test/live/github", trialEngine, nil)

			immutable := regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
			if immutable.MatchString(marker) || !immutable.MatchString(trialEngine.Commit) {
				t.Fatal("production pin grammar must reject the draft and admit the frozen commit")
			}
			for path, original := range files {
				got := trial.git(dir, "show", "HEAD:"+path)
				want := original
				if !strings.HasPrefix(path, "test/live/") {
					want = strings.NewReplacer(
						"https://codefloe.com/itiquette/forge-ci/.forgejo/actions/", "https://github.com/"+trialEngine.Owner+"/"+trialEngine.Repository+"/.forgejo/actions/",
						"itiquette/forge-ci/.forgejo/workflows/", trialEngine.Owner+"/"+trialEngine.Repository+"/.forgejo/workflows/",
						marker, trialEngine.Commit,
						"forge-ci-repository: itiquette/forge-ci", "forge-ci-repository: "+trialEngine.Owner+"/"+trialEngine.Repository,
					).Replace(original)
				}
				if got != want {
					t.Errorf("private %s = %q, want %q", path, got, want)
				}
				if got := trial.git(root, "show", "HEAD:"+path); got != original {
					t.Errorf("source blob changed: %s", path)
				}
			}
			if got := trial.git(dir, "show", "HEAD:.github/workflows/pullrequest.yml"); !strings.Contains(got, trialEngine.Owner+"/"+trialEngine.Repository+"/.github/workflows/pullrequest-orchestrator.yml@"+trialEngine.Commit) {
				t.Fatalf("GitHub fixture not rendered against the same frozen engine: %s", got)
			}
			if trial.git(root, "rev-parse", "HEAD") != head || trial.git(root, "status", "--porcelain") != "" {
				t.Fatal("source HEAD or working tree changed")
			}
		})
	}
}

func TestGitHubConsumerNativeChecksumPolicy(t *testing.T) {
	t.Parallel()

	const nativeDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const engineDigest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const independentDigest = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	for _, recorded := range []bool{false, true} {
		name := "preserved-source-checksum-is-not-engine-proof"
		if recorded {
			name = "checksum-from-frozen-engine-tree"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			trial := &GitHubTrial{tb: t}
			engine := trialEngine
			wantDigest := nativeDigest
			if recorded {
				engineRoot := t.TempDir()
				trial.git(engineRoot, "init", "--quiet", "--object-format=sha1", "--initial-branch=main", ".")
				for path, text := range map[string]string{
					layerChecksum: engineDigest + "  bin/forge-ci\n",
					".forgejo/actions/setup-forge-ci/action.yml":     "name: Fixture setup\n",
					".github/workflows/pullrequest-orchestrator.yml": "name: Fixture orchestrator\n",
				} {
					writeLayerFile(t, engineRoot, path, text)
				}
				trial.git(engineRoot, "add", "--all")
				trial.commit(engineRoot, consumerBaseMessage, "")
				engine.Commit = strings.TrimSpace(trial.git(engineRoot, "rev-parse", "HEAD"))
				var err error
				engine.admin, err = newWorkflowAdmin(t.Context(), engineRoot, engine.Commit, func(string) string { return "" })
				if err != nil {
					t.Fatal(err)
				}
				// The reader must use the frozen commit, not the current worktree.
				writeLayerFile(t, engineRoot, layerChecksum, independentDigest+"  bin/forge-ci\n")
				wantDigest = engineDigest
			}
			root := t.TempDir()
			trial.git(root, "init", "--quiet", "--object-format=sha256", "--initial-branch=main", ".")
			native := "jobs:\n  check:\n    steps:\n      - uses: https://codefloe.com/itiquette/forge-ci/.forgejo/actions/setup-forge-ci@PIN_FORGE_CI_SHA\n        with:\n          expected-sha256: " + nativeDigest + "\n"
			policy := "package policy_test\nconst forgeCIBinarySHA256 = \"" + nativeDigest + "\"\nconst independentSHA256 = \"" + independentDigest + "\"\n"
			files := map[string]string{
				".forgejo/workflows/pullrequest.yml":         native,
				"test/policy/workflow_policy_test.go":        policy,
				"test/live/github/workflows/pullrequest.yml": fixturePullRequest,
			}
			for path, text := range files {
				writeLayerFile(t, root, path, text)
			}
			trial.git(root, "add", "--all")
			trial.commit(root, consumerBaseMessage, "")
			dir, _ := trial.stageConsumer(root, "test/live/github", engine, nil)
			got := trial.git(dir, "show", "HEAD:.forgejo/workflows/pullrequest.yml")
			if !strings.Contains(got, "expected-sha256: "+wantDigest) || !strings.Contains(got, "https://github.com/"+engine.Owner+"/"+engine.Repository+"/.forgejo/actions/setup-forge-ci@"+engine.Commit) {
				t.Fatalf("native checksum or frozen identity lost: %s", got)
			}
			got = trial.git(dir, "show", "HEAD:test/policy/workflow_policy_test.go")
			if got != strings.ReplaceAll(policy, nativeDigest, wantDigest) {
				t.Fatalf("checksum policy constant or independent digest changed incorrectly: %s", got)
			}
			for path, original := range files {
				if trial.git(root, "show", "HEAD:"+path) != original {
					t.Fatalf("source changed: %s", path)
				}
			}
			if trial.git(root, "status", "--porcelain") != "" {
				t.Fatal("source working tree changed")
			}
		})
	}
}

func TestGitHubNativePolicyChecksumRequiresFrozenRecordedData(t *testing.T) {
	t.Parallel()
	trial := &GitHubTrial{tb: t}
	root := t.TempDir()
	trial.git(root, "init", "--quiet", "--initial-branch=main", ".")
	writeLayerFile(t, root, ".forgejo/workflows/pullrequest.yml", "jobs:\n  check:\n    steps:\n      - uses: https://codefloe.com/itiquette/forge-ci/.forgejo/actions/setup-forge-ci@PIN_FORGE_CI_SHA\n        with:\n          expected-sha256: "+strings.Repeat("a", 64)+"\n")
	trial.git(root, "add", "--all")
	trial.commit(root, consumerBaseMessage, "")
	consumer, err := openConsumer(t.Context(), githubStageGit(), root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, admin := range map[string]*workflowAdmin{
		"wrong commit":     {commit: strings.Repeat("e", 40)},
		"missing reader":   {commit: trialEngine.Commit},
		"missing checksum": {commit: trialEngine.Commit, read: func(string) ([]byte, error) { return nil, os.ErrNotExist }},
		"empty checksum":   {commit: trialEngine.Commit, read: func(string) ([]byte, error) { return nil, nil }},
		"wrong binary": {commit: trialEngine.Commit, read: func(string) ([]byte, error) {
			return []byte(strings.Repeat("b", 64) + "  unrelated-binary\n"), nil
		}},
	} {
		t.Run(name, func(t *testing.T) {
			engine := trialEngine
			engine.admin = admin
			if _, err := githubNativePolicyLayer(t.Context(), consumer, engine); err == nil {
				t.Fatal("unavailable or mismatched recorded checksum was accepted")
			}
		})
	}
}

// A file the trial makes replaces the fixture's at that path in the pushed
// commit, and the source checkout is left alone.
func TestGitHubConsumerCommitsTrialFiles(t *testing.T) {
	t.Parallel()
	trial := &GitHubTrial{tb: t}
	root := t.TempDir()
	trial.git(root, "init", "--quiet", "--initial-branch=main", ".")

	for name, body := range map[string]string{
		".forge-ci/allowed_signers":                  "fixture\n",
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

	trial.git(root, "add", "--all")
	trial.commit(root, consumerBaseMessage, "")

	signers := trial.WithFile(".forge-ci/allowed_signers", "bot@example.com ssh-ed25519 AAAA\n")
	if trial.files != nil {
		t.Fatal("WithFile changed the trial it was called on")
	}

	dir, _ := signers.WithFile("extra/notes.txt", "made by the trial\n").stageConsumer(root, "test/live/github", trialEngine, nil)
	if got := trial.git(dir, "show", "HEAD:.forge-ci/allowed_signers"); got != "bot@example.com ssh-ed25519 AAAA\n" {
		t.Fatalf("allowed_signers = %q", got)
	}

	if got := trial.git(dir, "show", "HEAD:extra/notes.txt"); got != "made by the trial\n" {
		t.Fatalf("notes = %q", got)
	}

	if got := trial.git(root, "show", "HEAD:.forge-ci/allowed_signers"); got != "fixture\n" {
		t.Fatalf("source changed: %q", got)
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

func TestGPGSignatureMustBeTheTrialKeys(t *testing.T) {
	t.Parallel()

	trialKey := strings.Repeat("A1", 20)
	otherKey := strings.Repeat("B2", 20)

	tests := []struct {
		name   string
		status string
		want   bool
	}{
		{"the trial key", "[GNUPG:] GOODSIG A1A1A1A1A1A1A1A1 Trial\n[GNUPG:] VALIDSIG " + trialKey + " 2026-10-31 1793450000 0 4 0 22 10 00 " + trialKey + "\n", true},
		{"a subkey of the trial key", "[GNUPG:] VALIDSIG " + otherKey + " 2026-10-31 1793450000 0 4 0 22 10 00 " + strings.ToLower(trialKey) + "\n", true},
		{"another key", "[GNUPG:] VALIDSIG " + otherKey + " 2026-10-31 1793450000 0 4 0 22 10 00 " + otherKey + "\n", false},
		{"a bad signature", "[GNUPG:] BADSIG A1A1A1A1A1A1A1A1 Trial\n", false},
		{"the fingerprint outside a status line", "gpg: VALIDSIG " + trialKey + "\n", false},
	}

	for _, test := range tests {
		if got := signedBy(test.status, trialKey); got != test.want {
			t.Errorf("%s: signedBy = %v, want %v", test.name, got, test.want)
		}
	}
}

// A job with no log says whether a runner ever took it: a job the forge
// fails before dispatch has no runner trace to look for.
func TestNoLogReasonTellsAForgeDecisionFromAMissingLog(t *testing.T) {
	t.Parallel()

	none, task := int64(0), int64(3883)

	undispatched := RunJob{Name: "Build", RunsOn: []string{"ubuntu-latest"}, Needs: []string{"prepare"}, TaskID: &none}
	if got := undispatched.NoLogReason(); !strings.Contains(got, "no runner ever took it") ||
		!strings.Contains(got, "[ubuntu-latest]") || !strings.Contains(got, "[prepare]") {
		t.Errorf("an undispatched job: %q", got)
	}

	ran := RunJob{Name: "Build", TaskID: &task}
	if got := ran.NoLogReason(); !strings.Contains(got, "task 3883") || strings.Contains(got, "no runner") {
		t.Errorf("a job that ran: %q", got)
	}

	// A forge that does not report the task says nothing either way.
	if got := (RunJob{Name: "Build"}).NoLogReason(); !strings.Contains(got, "does not say") || strings.Contains(got, "before dispatch") {
		t.Errorf("a job of a forge without task_id: %q", got)
	}
}

// A signed release signs every subject: one signature beside unsigned files
// is reported, and signature files are not subjects of their own.
func TestUnsignedSubjectsListsEveryAssetWithoutAVerifiedSignature(t *testing.T) {
	t.Parallel()

	assets := map[string]int64{
		"app.tar.gz": 1, "app.tar.gz.bundle": 2,
		"app.jar": 3, "app.jar.asc": 4,
		"checksums.txt": 5,
		"sbom.cdx.json": 6, "sbom.cdx.json.sig": 7,
	}
	bundles := map[string]bool{"app.tar.gz": true}
	signatures := map[string]bool{"app.jar": true}

	got := unsignedSubjects(assets, bundles, signatures)
	want := []string{"checksums.txt", "sbom.cdx.json"}

	if !slices.Equal(got, want) {
		t.Errorf("unsignedSubjects = %v, want %v", got, want)
	}
}
