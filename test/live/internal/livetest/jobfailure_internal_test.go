// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

func TestClassifyGitLabJob_PutsOnlyTheLabsReasonsOnTheLab(t *testing.T) {
	t.Parallel()

	systemFailureLog := "Preparing environment\nERROR: Job failed (system failure): prepare environment: exit code 1\n"

	for _, tc := range []struct {
		name, reason, log string
		infrastructure    bool
	}{
		{name: "runner system", reason: "runner_system_failure", infrastructure: true},
		{name: "stuck or timeout", reason: "stuck_or_timeout_failure", infrastructure: true},
		{name: "scheduler", reason: "scheduler_failure", infrastructure: true},
		{name: "api", reason: "api_failure", infrastructure: true},
		{name: "data integrity", reason: "data_integrity_failure", infrastructure: true},
		{name: "runner unsupported", reason: "runner_unsupported", infrastructure: true},
		{name: "script", reason: "script_failure"},
		{name: "unknown", reason: "unknown_failure"},
		{name: "no matching runner", reason: "no_matching_runner"},
		{name: "execution timeout", reason: "job_execution_timeout"},
		{name: "missing dependency", reason: "missing_dependency_failure"},
		{name: "unheard of", reason: "a_reason_gitlab_adds_later"},
		{name: "the reason outranks the log", reason: "script_failure", log: systemFailureLog},
		{name: "no reason, log marker", log: systemFailureLog, infrastructure: true},
		{name: "no reason, no marker", log: "exit status 1\n"},
		{name: "no reason, no log"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := ClassifyGitLabJob("Build: release files", tc.reason, tc.log)
			if got.Infrastructure != tc.infrastructure || got.Job != "Build: release files" || got.Reason == "" {
				t.Fatalf("classified %+v, want infrastructure=%t", got, tc.infrastructure)
			}
		})
	}
}

func TestClassifyForgejoJob_PutsOnlyAnUntakenRunnerJobOnTheLab(t *testing.T) {
	t.Parallel()

	none, task := int64(0), int64(3883)

	for _, tc := range []struct {
		name           string
		job            RunJob
		infrastructure bool
	}{
		{name: "no task reported", job: RunJob{Name: "Build", RunsOn: []string{"lab"}}},
		{name: "no runner took it", job: RunJob{Name: "Build", RunsOn: []string{"lab"}, TaskID: &none}, infrastructure: true},
		{name: "reusable-workflow caller", job: RunJob{Name: "Release", TaskID: &none}},
		{name: "ran as a task", job: RunJob{Name: "Build", RunsOn: []string{"lab"}, TaskID: &task}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := ClassifyForgejoJob(tc.job); got.Infrastructure != tc.infrastructure || got.Job != tc.job.Name {
				t.Fatalf("classified %+v, want infrastructure=%t", got, tc.infrastructure)
			}
		})
	}

	jobs := []RunJob{
		{Name: "ok", Status: "success", RunsOn: []string{"lab"}, TaskID: &none},
		{Name: "untaken", Status: "failure", RunsOn: []string{"lab"}, TaskID: &none},
	}
	if got := ForgejoJobFailures(jobs); len(got) != 1 || got[0].Job != "untaken" || !got[0].Infrastructure {
		t.Fatalf("failures of a run: %+v", got)
	}
}

func TestStopOnInfrastructure_StopsOnlyWhenTheLabCausedEveryFailure(t *testing.T) {
	t.Parallel()

	lab := JobFailure{Job: "Build", Infrastructure: true, Reason: "runner_system_failure"}
	product := JobFailure{Job: "Test", Reason: "script_failure"}

	for _, tc := range []struct {
		name     string
		failures []JobFailure
		stop     bool
	}{
		{name: "none"},
		{name: "product", failures: []JobFailure{product}},
		{name: "lab and product", failures: []JobFailure{lab, product}},
		{name: "lab", failures: []JobFailure{lab}, stop: true},
		{name: "lab twice", failures: []JobFailure{lab, lab}, stop: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var recorder fatalRecorder

			StopOnInfrastructure(&recorder, tc.failures)

			if recorder.fatal != tc.stop || OnlyInfrastructure(tc.failures) != tc.stop {
				t.Fatalf("stopped=%t, want %t", recorder.fatal, tc.stop)
			}
		})
	}
}

// The GitLab runner writes its system-failure line last. The log was read
// from its start and cut at 1 MiB, so on a long log that line was never seen.
func TestRunLogTail_ReadsTheRealEndOfALongLog(t *testing.T) {
	const marker = "ERROR: Job failed (system failure): prepare environment\n"

	long := strings.Repeat("progress line\n", (2<<20)/14) + marker

	var archive bytes.Buffer

	writer := zip.NewWriter(&archive)

	entry, err := writer.Create("1-build.log")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := io.WriteString(entry, long); err != nil {
		t.Fatal(err)
	}

	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	server, ca := labTLSServer(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/trace":
			_, _ = io.WriteString(response, long)
		case "/logs":
			response.Header().Set("Content-Type", "application/zip")
			_, _ = response.Write(archive.Bytes())
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))

	original := http.DefaultTransport
	transport := original.(*http.Transport).Clone()
	transport.DialContext = (&dialRecorder{to: server.Listener.Addr().String()}).dial

	http.DefaultTransport = transport

	defer func() { http.DefaultTransport = original }()
	defer transport.CloseIdleConnections()

	target := Target{Forge: provider.ForgeGitLab, Host: labHost, Owner: "fixture", CAFile: "captured", caPEM: ca, accepted: true}

	for _, route := range []string{"/trace", "/logs"} {
		tail := runLogTail(t.Context(), target, target.BaseURL()+route)
		if !strings.HasSuffix(tail, marker) || len(tail) > jobLogTail || !strings.HasPrefix(tail, "progress line\n") {
			t.Errorf("%s: kept %d bytes ending %q", route, len(tail), tail[max(0, len(tail)-80):])
		}

		if got := ClassifyGitLabJob("Build", "", tail); !got.Infrastructure {
			t.Errorf("%s: the tail's marker was not read: %+v", route, got)
		}
	}
}
