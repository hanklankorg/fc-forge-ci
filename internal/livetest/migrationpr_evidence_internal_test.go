// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-github/v91/github"
)

const validMigrationJUnit = `<testsuite tests="2" skipped="1" failures="0" errors="0"><testcase name="ran"/><testcase name="skipped"><skipped/></testcase></testsuite>`

func migrationTestZIP(t *testing.T, name, content string, mode fs.FileMode) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	directory := &zip.FileHeader{Name: "reports/"}
	directory.SetMode(fs.ModeDir | 0755)
	if _, err := writer.CreateHeader(directory); err != nil {
		t.Fatal(err)
	}
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetMode(mode)
	file, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestMigrationJUnitReports(t *testing.T) {
	for _, tc := range []struct {
		name, path, content string
		mode                fs.FileMode
		pass                bool
	}{
		{"valid", "reports/TEST-unit.xml", validMigrationJUnit, 0644, true},
		{"nested suites", "TEST-unit.xml", `<testsuites tests="2" skipped="1" failures="0" errors="0">` + validMigrationJUnit + `</testsuites>`, 0644, true},
		{"missing reports", "output.txt", "green", 0644, false},
		{"zero tests", "TEST-unit.xml", `<testsuite tests="0"/>`, 0644, false},
		{"all skipped", "TEST-unit.xml", `<testsuite tests="1" skipped="1"><testcase><skipped/></testcase></testsuite>`, 0644, false},
		{"malformed", "TEST-unit.xml", `<testsuite`, 0644, false},
		{"fabricated count", "TEST-unit.xml", `<testsuite tests="99"><testcase/></testsuite>`, 0644, false},
		{"failure", "TEST-unit.xml", `<testsuite tests="1" failures="1"><testcase><failure/></testcase></testsuite>`, 0644, false},
		{"error", "TEST-unit.xml", `<testsuite tests="1" errors="1"><testcase><error/></testcase></testsuite>`, 0644, false},
		{"multiple roots", "TEST-unit.xml", validMigrationJUnit + validMigrationJUnit, 0644, false},
		{"external entity", "TEST-unit.xml", `<!DOCTYPE testsuite SYSTEM "file:///private-sentinel"><testsuite tests="0"/>`, 0644, false},
		{"traversal", "../TEST-unit.xml", validMigrationJUnit, 0644, false},
		{"reserved", ".git/TEST-unit.xml", validMigrationJUnit, 0644, false},
		{"symlink", "TEST-unit.xml", validMigrationJUnit, fs.ModeSymlink | 0777, false},
		{"pipe", "TEST-unit.xml", validMigrationJUnit, fs.ModeNamedPipe | 0600, false},
		{"file quota", "TEST-unit.xml", strings.Repeat(" ", (2<<20)+1), 0644, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			summary, err := migrationJUnitReports(migrationTestZIP(t, tc.path, tc.content, tc.mode))
			if (err == nil) != tc.pass {
				t.Fatalf("summary=%+v error=%v", summary, err)
			}
			if tc.pass && (summary.Reports != 1 || summary.Tests != 2 || summary.Skipped != 1) {
				t.Fatalf("wrong execution counts: %+v", summary)
			}
		})
	}
	t.Run("entry quota", func(t *testing.T) {
		var buffer bytes.Buffer
		writer := zip.NewWriter(&buffer)
		for range 2049 {
			if _, err := writer.Create("empty"); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := migrationJUnitReports(buffer.Bytes()); err == nil {
			t.Fatal("entry quota accepted")
		}
	})
}

func TestMigrationJavaEvidenceRequiresExecutionAndRetainsReports(t *testing.T) {
	for _, variant := range []string{"valid", "missing artifact", "skipped job", "failed reports", "failed job", "wrong job", "no runner"} {
		t.Run(variant, func(t *testing.T) {
			payload := migrationTestZIP(t, "reports/TEST-unit.xml", validMigrationJUnit, 0644)
			if variant == "failed reports" {
				payload = migrationTestZIP(t, "TEST-unit.xml", `<testsuite tests="1" failures="1"><testcase><failure/></testcase></testsuite>`, 0644)
			}
			scratch := t.TempDir()
			artifactFile := filepath.Join(scratch, "artifact.zip")
			if err := os.WriteFile(artifactFile, payload, 0600); err != nil {
				t.Fatal(err)
			}
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/fixture/fc-app/actions/runs/42/jobs":
					name := "original-java21-unit-check / test"
					conclusion := "success"
					runner := 12
					if variant == "wrong job" {
						name = "unrelated"
					}
					if variant == "skipped job" {
						conclusion = "skipped"
					}
					if variant == "failed job" {
						conclusion = "failure"
					}
					if variant == "no runner" {
						runner = 0
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 1, "jobs": []any{map[string]any{"id": 9, "name": name, "status": "completed", "conclusion": conclusion, "runner_id": runner, "started_at": "2026-01-01T00:00:00Z", "completed_at": "2026-01-01T00:01:00Z"}}})
				case "/repos/fixture/fc-app/actions/runs/42/artifacts":
					artifacts := []any{map[string]any{"id": 7, "name": migrationReportArtifact, "size_in_bytes": len(payload), "expired": false}}
					if variant == "missing artifact" {
						artifacts = nil
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(artifacts), "artifacts": artifacts})
				case "/repos/fixture/fc-app/actions/artifacts/7/zip":
					w.Header().Set("Location", server.URL+"/signed-report")
					w.WriteHeader(http.StatusFound)
				case "/signed-report":
					if r.Header.Get("Authorization") != "" {
						t.Error("credential on report download")
					}
					body, err := os.ReadFile(artifactFile)
					if err != nil {
						t.Error(err)
						http.Error(w, "missing scratch artifact", 500)
						return
					}
					_, _ = w.Write(body)
				default:
					t.Errorf("unexpected API request: %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			api, err := github.NewClient(github.WithHTTPClient(server.Client()), github.WithURLs(new(server.URL+"/"), nil), github.WithAuthToken("synthetic-api-only"))
			if err != nil {
				t.Fatal(err)
			}
			trial := GitHubTrial{tb: t, api: api, org: "fixture"}
			evidence := t.TempDir()
			_, err = trial.migrationJavaEvidence(t.Context(), "fc-app", &github.WorkflowRun{ID: new(int64(42)), Status: new("completed"), Conclusion: new("success")}, "original-java21-unit-check / test", evidence)
			if (err == nil) != (variant == "valid") {
				t.Fatalf("unexpected oracle error: %v", err)
			}
			if err := os.RemoveAll(scratch); err != nil {
				t.Fatal(err)
			}
			if variant == "valid" || variant == "failed reports" || variant == "failed job" {
				got, err := os.ReadFile(filepath.Join(evidence, "github-java-run-42-surefire.zip"))
				if err != nil || !bytes.Equal(got, payload) {
					t.Fatal("evidence did not survive scratch cleanup", err)
				}
				receipt, err := os.ReadFile(filepath.Join(evidence, "github-java-run-42-surefire.json"))
				var metadata struct {
					Accepted bool                  `json:"accepted"`
					JUnit    migrationJUnitSummary `json:"junit"`
					Job      github.WorkflowJob    `json:"job"`
				}
				if err != nil || json.Unmarshal(receipt, &metadata) != nil || metadata.Accepted != (variant == "valid") || metadata.Job.GetRunnerID() != 12 || metadata.JUnit.Reports != 1 {
					t.Fatalf("missing execution receipt: %s: %v", receipt, err)
				}
			}
		})
	}
}

func TestSnapshotEvidenceConfinesWrites(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sentinel, filepath.Join(root, "report.zip")); err != nil {
		t.Fatal(err)
	}
	if err := writeSnapshotEvidence(root, "report.zip", []byte("report")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(sentinel)
	if err != nil || string(got) != "private" {
		t.Fatal("outside sentinel changed")
	}
	if err := os.Symlink(outside, filepath.Join(root, "ancestor")); err != nil {
		t.Fatal(err)
	}
	if err := writeSnapshotEvidence(filepath.Join(root, "ancestor", "new"), "report.zip", []byte("report")); err == nil {
		t.Fatal("symlink evidence ancestor accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "new")); !os.IsNotExist(err) {
		t.Fatal("outside directory created")
	}
}

func TestMigrationEvidenceDestinationIsExternal(t *testing.T) {
	repo := t.TempDir()
	if err := migrationEvidenceDestination(filepath.Join(repo, "evidence"), repo); err == nil {
		t.Fatal("repository-local evidence accepted")
	}
	if err := migrationEvidenceDestination(t.TempDir(), repo); err != nil {
		t.Fatal(err)
	}
}
