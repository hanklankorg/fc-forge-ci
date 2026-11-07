// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-github/v91/github"
	"golang.org/x/crypto/nacl/box"
)

func TestGitHubFailureEvidenceBeforeCleanup(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-created-yet")
	t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", root)

	const management = "inert-management-credential"

	const release = "inert-release-credential"

	public, _, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	log := "FAIL earlier/package: actual diagnostic\n" + strings.Repeat("later/package PASS\n", 100) +
		management + " " + base64.StdEncoding.EncodeToString([]byte(release)) + "\n##[error]go test exit 1\n"
	logReads := 0

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/repos/") {
			if request.Header.Get("Authorization") != "Bearer "+management {
				t.Error("SDK request lacks its configured credential")
			}
		} else if request.Header.Get("Authorization") != "" {
			t.Error("credential forwarded to pre-signed redirect chain")
		}

		switch request.URL.Path {
		case "/repos/fixture/fc-app/actions/secrets/public-key":
			_ = json.NewEncoder(response).Encode(map[string]string{"key_id": "fixture", "key": base64.StdEncoding.EncodeToString(public[:])})
		case "/repos/fixture/fc-app/actions/secrets/RELEASE_TOKEN":
			response.WriteHeader(http.StatusCreated)
		case "/repos/fixture/fc-app/actions/runs/99/jobs":
			_, _ = io.WriteString(response, `{"jobs":[{"id":42,"name":"test","conclusion":"failure"},{"id":43,"conclusion":"success"},{"id":44,"conclusion":"skipped"}]}`)
		case "/repos/fixture/fc-app/actions/jobs/42/logs":
			http.Redirect(response, request, "http://"+request.Host+"/redirect?signature=inert-private-signature", http.StatusFound)
		case "/redirect":
			http.Redirect(response, request, "http://"+request.Host+"/download?signature=inert-private-signature", http.StatusFound)
		case "/download":
			logReads++

			if request.Header.Get("Authorization") != "" {
				t.Error("credential forwarded to pre-signed download")
			}

			_, _ = io.WriteString(response, log)
		default:
			t.Errorf("unexpected diagnostic request: %s", request.URL.Path)
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	api, err := github.NewClient(github.WithHTTPClient(server.Client()), github.WithURLs(new(server.URL+"/"), nil), github.WithAuthToken(management))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("disposable", func(t *testing.T) {
		// Model the already-registered repository cleanup: evidence must exist
		// when it runs, not in a later cleanup registered by the caller.
		t.Cleanup(func() {
			files, err := filepath.Glob(filepath.Join(root, "*-run-99-job-42-attempt-2.log"))
			if err != nil || len(files) != 1 {
				t.Fatalf("evidence missing before disposable cleanup: %v %v", files, err)
			}

			body, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}

			if !strings.HasPrefix(string(body), "FAIL earlier/package: actual diagnostic\n") || !strings.HasSuffix(string(body), "##[error]go test exit 1\n") {
				t.Fatal("retention lost the earlier diagnostic or terminal verdict")
			}

			for _, secret := range []string{management, release, base64.StdEncoding.EncodeToString([]byte(release)), "inert-private-signature"} {
				if strings.Contains(string(body), secret) {
					t.Fatal("private value reached evidence")
				}
			}

			info, err := os.Stat(files[0])
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("evidence permissions are not private")
			}
		})

		tb := &recordingTB{}
		trial := GitHubTrial{tb: tb, api: api, org: "fixture", token: management}
		trial.SetSecret("fc-app", "RELEASE_TOKEN", release)
		trial.ReportRun("fc-app", &github.WorkflowRun{ID: new(int64(99)), RunAttempt: new(2)})

		if logReads != 1 || len(tb.errors) != 0 || tb.fatal != "" {
			t.Fatalf("unexpected retention result: reads=%d errors=%v fatal=%s", logReads, tb.errors, tb.fatal)
		}

		console := strings.Join(tb.logs, "\n")
		if strings.Contains(console, management) || strings.Contains(console, base64.StdEncoding.EncodeToString([]byte(release))) || !strings.Contains(console, "go test exit 1") {
			t.Fatal("console lost the verdict or exposed a private value")
		}
	})
}

func TestGitHubFailureEvidenceBoundsAndOptIn(t *testing.T) {
	for _, tc := range []struct {
		name         string
		body         string
		status       int
		optIn        bool
		wantError    bool
		brokenRead   bool
		redirectLoop bool
		wantClass    string
	}{
		{name: "disabled", body: "##[error]fixture\n", status: http.StatusOK},
		{name: "at limit", body: strings.Repeat("x", maxJobLog), status: http.StatusOK, optIn: true},
		{name: "over limit", body: strings.Repeat("x", maxJobLog+1), status: http.StatusOK, optIn: true, wantError: true, wantClass: "retention-limit"},
		{name: "forbidden", body: "private response body", status: http.StatusForbidden, optIn: true, wantError: true, wantClass: "HTTP 403 class=http-status"},
		{name: "not found", body: "private response body", status: http.StatusNotFound, optIn: true, wantError: true, wantClass: "HTTP 404 class=http-status"},
		{name: "incomplete read", body: "private partial log and credential", status: http.StatusOK, optIn: true, wantError: true, brokenRead: true, wantClass: "unexpected-eof"},
		{name: "redirect loop", optIn: true, wantError: true, redirectLoop: true, wantClass: "io-or-transport"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "evidence")
			t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", "")
			t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE", "")

			if tc.optIn {
				t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", root)
			}

			var requests atomic.Int32

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)

				if tc.redirectLoop {
					http.Redirect(w, r, "/loop?private-signature", http.StatusFound)

					return
				}

				if tc.brokenRead {
					w.Header().Set("Content-Length", strconv.Itoa(len(tc.body)+100))
				}

				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()

			tb := &recordingTB{}
			trial := GitHubTrial{tb: tb, org: "fixture"}

			excerpt := trial.failureEvidence(t.Context(), "fc-app", &github.WorkflowRun{ID: new(int64(99))}, &github.WorkflowJob{ID: new(int64(42))}, server.URL+"?private-signature")
			if (len(tb.errors) != 0) != tc.wantError {
				t.Fatalf("unexpected evidence errors: %v", tb.errors)
			}

			if tc.wantClass != "" && (!strings.Contains(excerpt, tc.wantClass) || !strings.Contains(strings.Join(tb.errors, "\n"), tc.wantClass)) {
				t.Fatalf("safe failure class missing: excerpt=%s errors=%v", excerpt, tb.errors)
			}

			if requests.Load() > 10 {
				t.Fatal("log download exceeded the native redirect bound")
			}

			files, err := filepath.Glob(filepath.Join(root, "*.log"))
			if err != nil || (len(files) == 1) != (tc.optIn && !tc.wantError) {
				t.Fatalf("unexpected retained files: %v %v", files, err)
			}

			if strings.Contains(excerpt+strings.Join(tb.logs, "\n")+strings.Join(tb.errors, "\n"), "private") {
				t.Fatal("private transport detail exposed")
			}
		})
	}
}

func TestGitHubFailureEvidenceDisabledKeepsLegacyBudget(t *testing.T) {
	t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", "")
	t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE", "")

	for _, size := range []int{9 << 20, 31 << 20} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			log := strings.Repeat("progress\n", size/len("progress\n")) + "original failure\n##[error]go test exit 1\ncleanup\n"

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, log)
			}))
			defer server.Close()

			tb := &recordingTB{}
			trial := GitHubTrial{tb: tb}
			got := trial.failureEvidence(t.Context(), "fc-app", &github.WorkflowRun{}, &github.WorkflowJob{}, server.URL)

			want := readFailureExcerpt(strings.NewReader(log), 40)
			if got != want || !strings.Contains(got, "original failure") || len(tb.errors) != 0 || len(tb.logs) != 0 {
				t.Fatalf("disabled retention changed legacy diagnostics: excerpt bytes=%d errors=%v logs=%v", len(got), tb.errors, tb.logs)
			}
		})
	}
}

func TestGitHubFailureEvidenceDiscoveryErrorBeforeCleanup(t *testing.T) {
	t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", filepath.Join(t.TempDir(), "evidence"))

	const management = "inert-management-credential"

	var annotations atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+management {
			t.Error("SDK request lacks its configured credential")
		}

		switch request.URL.Path {
		case "/repos/fixture/fc-app/actions/runs/99/jobs":
			_, _ = io.WriteString(response, `{"jobs":[{"id":42,"name":"test","conclusion":"failure","check_run_url":"https://api.github.com/repos/fixture/fc-app/check-runs/7"}]}`)
		case "/repos/fixture/fc-app/actions/jobs/42/logs":
			response.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(response).Encode(map[string]string{"message": "unavailable " + management})
		case "/repos/fixture/fc-app/check-runs/7/annotations":
			annotations.Add(1)

			_, _ = io.WriteString(response, `[{"message":"fixture annotation fallback"}]`)
		default:
			t.Errorf("unexpected diagnostic request: %s", request.URL.Path)
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	api, err := github.NewClient(github.WithHTTPClient(server.Client()), github.WithURLs(new(server.URL+"/"), nil), github.WithAuthToken(management))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("disposable", func(t *testing.T) {
		tb := &recordingTB{}

		t.Cleanup(func() {
			if len(tb.errors) != 1 || !strings.Contains(tb.errors[0], "job 42 log URL unavailable; no full evidence retained") {
				t.Fatalf("retention failure not reported before cleanup: %v", tb.errors)
			}

			logs := strings.Join(tb.logs, "\n")
			if annotations.Load() != 1 || !strings.Contains(logs, "fixture annotation fallback") || strings.Contains(logs, management) {
				t.Fatal("annotation fallback missing or credential exposed")
			}
		})

		trial := GitHubTrial{tb: tb, api: api, org: "fixture", token: management}
		trial.ReportRun("fc-app", &github.WorkflowRun{ID: new(int64(99))})
	})
}

func TestGitHubFailureEvidenceLargeGoJSONKeepsEarlierFailure(t *testing.T) {
	for _, size := range []int{9 << 20, 33 << 20} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "evidence")
			t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", root)

			const secret = "inert-signing-password-for-large-log"

			failure := `2026-11-07T12:00:00Z {"Time":"2026-11-07T12:00:00Z","Action":"output","Package":"fixture/earlier","Test":"TestBuild","Output":"expected signed payload: ` + secret + `\n"}` + "\n" +
				`2026-11-07T12:00:00Z {"Time":"2026-11-07T12:00:00Z","Action":"fail","Package":"fixture/earlier","Test":"TestBuild","Elapsed":0.1}` + "\n"
			progress := `2026-11-07T12:01:00Z {"Time":"2026-11-07T12:01:00Z","Action":"output","Package":"fixture/test/policy","Output":"--- PASS: TestPolicy (0.00s)\n"}` + "\n"
			log := failure + strings.Repeat(progress, size/len(progress)) +
				`2026-11-07T12:02:00Z {"Time":"2026-11-07T12:02:00Z","Action":"pass","Package":"fixture/test/policy","Elapsed":23.1}` + "\n##[error]go test exit status 1\n"

			var requests atomic.Int32

			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				requests.Add(1)

				if request.Header.Get("Authorization") != "" {
					t.Error("credential forwarded to signed download")
				}

				_, _ = io.WriteString(response, log)
			}))
			defer server.Close()

			tb := &recordingTB{}

			t.Cleanup(func() {
				if len(tb.errors) != 1 || !strings.Contains(tb.errors[0], "no full evidence retained") {
					t.Fatalf("incomplete evidence not reported before cleanup: %v", tb.errors)
				}

				files, err := filepath.Glob(filepath.Join(root, "*.log"))
				if err != nil || len(files) != 0 {
					t.Fatalf("partial log represented as full evidence: %v %v", files, err)
				}
			})

			trial := GitHubTrial{tb: tb, org: "fixture", token: secret}

			excerpt := trial.failureEvidence(t.Context(), "fc-app", &github.WorkflowRun{ID: new(int64(99))}, &github.WorkflowJob{ID: new(int64(42))}, server.URL+"?private-signature")
			if !strings.Contains(excerpt, "expected signed payload") || !strings.Contains(excerpt, `"Package":"fixture/earlier"`) || strings.Contains(excerpt, secret) || strings.Contains(excerpt, "private-signature") {
				t.Fatalf("earlier failure missing or unredacted: %s", excerpt)
			}

			class, observed := "retention-limit", len(log)
			if size > maxFailureLogBytes {
				class, observed = "diagnostic-limit", maxFailureLogBytes+1
			}

			for _, want := range []string{"HTTP 200", "class=" + class, fmt.Sprintf("observed_bytes=%d", observed), fmt.Sprintf("retention_limit_bytes=%d", maxJobLog), fmt.Sprintf("diagnostic_limit_bytes=%d", maxFailureLogBytes)} {
				if !strings.Contains(strings.Join(tb.errors, "\n"), want) {
					t.Errorf("safe capture metadata missing %q: %v", want, tb.errors)
				}
			}

			if requests.Load() != 1 || len(excerpt) > 4096 || !strings.Contains(excerpt, "no full evidence retained") {
				t.Fatal("capture retried, unbounded excerpt, or incomplete evidence misrepresented")
			}
		})
	}
}

func TestGitHubFailureEvidenceReadFailureKeepsRedactedCompleteLines(t *testing.T) {
	root := filepath.Join(t.TempDir(), "evidence")
	t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", root)

	const secret = "inert-signing-password-read-failure"

	log := "assertion failed: " + secret + "\ntest failed\npartial " + secret[:15]

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Length", strconv.Itoa(len(log)+100))
		_, _ = io.WriteString(response, log)
	}))
	defer server.Close()

	tb := &recordingTB{}
	trial := GitHubTrial{tb: tb, org: "fixture", secrets: map[string]map[string]string{"fc-app": {"signer": secret}}}

	excerpt := trial.failureEvidence(t.Context(), "fc-app", &github.WorkflowRun{}, &github.WorkflowJob{ID: new(int64(42))}, server.URL+"?private-signature")
	if !strings.Contains(excerpt, "assertion failed") || !strings.Contains(excerpt, "test failed") || !strings.Contains(excerpt, "class=unexpected-eof") {
		t.Fatalf("partial read lost useful diagnostic or safe class: %s", excerpt)
	}

	if len(tb.errors) != 1 || strings.Contains(excerpt+strings.Join(tb.errors, "\n"), secret[:15]) || strings.Contains(excerpt, "private-signature") {
		t.Fatal("partial secret exposed or incomplete evidence not reported")
	}

	files, err := filepath.Glob(filepath.Join(root, "*.log"))
	if err != nil || len(files) != 0 {
		t.Fatal("incomplete read retained as full evidence")
	}
}

func TestGitHubFailureEvidenceInvalidSignedLocationIsPrivate(t *testing.T) {
	t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", filepath.Join(t.TempDir(), "evidence"))

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/repos/fixture/fc-app/actions/runs/99/jobs":
			_, _ = io.WriteString(response, `{"jobs":[{"id":42,"name":"test","conclusion":"failure"}]}`)
		case "/repos/fixture/fc-app/actions/jobs/42/logs":
			response.Header().Set("Location", "https://download.invalid/%zz?signature=private-signature")
			response.WriteHeader(http.StatusFound)
		default:
			t.Error("unexpected request; malformed signed URL must never be followed")
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	api, err := github.NewClient(github.WithHTTPClient(server.Client()), github.WithURLs(new(server.URL+"/"), nil), github.WithAuthToken("inert-api-token"))
	if err != nil {
		t.Fatal(err)
	}

	tb := &recordingTB{}
	trial := GitHubTrial{tb: tb, api: api, org: "fixture"}
	trial.ReportRun("fc-app", &github.WorkflowRun{ID: new(int64(99))})

	output := strings.Join(append(tb.logs, tb.errors...), "\n")
	if len(tb.errors) != 1 || !strings.Contains(output, "status=302 class=redirect-location") || strings.Contains(output, "private-signature") || strings.Contains(output, "download.invalid") {
		t.Fatalf("discovery lost safe class or exposed signed location: %s", output)
	}
}

func TestGitHubFailureEvidenceExactDiagnosticBoundary(t *testing.T) {
	for _, scenario := range []struct {
		name         string
		newline      bool
		extra        string
		brokenRead   bool
		singleRecord bool
		wantFinal    bool
		wantClass    string
	}{
		{name: "complete final record without newline", wantFinal: true, wantClass: "retention-limit"},
		{name: "complete final record with newline", newline: true, wantFinal: true, wantClass: "retention-limit"},
		{name: "complete single record without newline", singleRecord: true, wantFinal: true, wantClass: "retention-limit"},
		{name: "one byte over without newline", extra: "x", wantClass: "diagnostic-limit"},
		{name: "boundary EOF unknown", brokenRead: true, wantClass: "unexpected-eof"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "evidence")
			t.Setenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR", root)

			const secret = "inert-boundary-signing-credential"

			final := `{"Time":"2026-11-07T12:00:00Z","Action":"fail","Package":"fixture/boundary","Test":"TestBoundary"}`
			if scenario.newline {
				final += "\n"
			}

			header := "credential: " + secret + "\n"

			log := header + strings.Repeat("p", maxFailureLogBytes-len(header)-len(final)-1) + "\n" + final + scenario.extra
			if scenario.singleRecord {
				start := strings.TrimSuffix(final, "}") + `,"Output":"`
				log = start + strings.Repeat("p", maxFailureLogBytes-len(start)-2) + `"}`
			}

			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				if scenario.brokenRead {
					response.Header().Set("Content-Length", strconv.Itoa(len(log)+100))
				}

				_, _ = io.WriteString(response, log)
			}))
			defer server.Close()

			tb := &recordingTB{}
			trial := GitHubTrial{tb: tb, token: secret}

			excerpt := trial.failureEvidence(t.Context(), "fc-app", &github.WorkflowRun{}, &github.WorkflowJob{ID: new(int64(42))}, server.URL+"?private-signature")
			if strings.Contains(excerpt, "TestBoundary") != scenario.wantFinal {
				t.Fatalf("boundary record preservation=%t; excerpt=%s", scenario.wantFinal, excerpt)
			}

			if scenario.wantFinal {
				legacy := readFailureExcerpt(strings.NewReader(log), 40)
				if !strings.Contains(legacy, "TestBoundary") || strings.Contains(legacy, "may be truncated") {
					t.Fatal("native bounded excerpt did not confirm exact-bound EOF")
				}
			}

			if len(tb.errors) != 1 || !strings.Contains(tb.errors[0], "class="+scenario.wantClass) || !strings.Contains(tb.errors[0], fmt.Sprintf("observed_bytes=%d", len(log))) {
				t.Fatalf("boundary capture metadata is not truthful: %v", tb.errors)
			}

			if strings.Contains(excerpt+strings.Join(tb.errors, "\n"), secret) || strings.Contains(excerpt, "private-signature") || strings.Contains(excerpt, "may be truncated") {
				t.Fatal("boundary diagnostic exposed credentials or claimed uncertain truncation")
			}

			files, err := filepath.Glob(filepath.Join(root, "*.log"))
			if err != nil || len(files) != 0 {
				t.Fatal("boundary diagnostic incorrectly retained as a full log")
			}
		})
	}
}
