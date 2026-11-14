// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

func TestTailWriter_KeepsTheEndFromAWholeLine(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
		keep           int
		dropped        int64
	}{
		{name: "fits", in: "a\nb\n", keep: 8, want: "a\nb\n"},
		{name: "cut mid line", in: "first\nsecond\nthird\n", keep: 9, want: "third\n", dropped: 13},
		{name: "cut at a line start", in: "1234\n678\n", keep: 4, want: "678\n", dropped: 5},
		{name: "only part of one line left", in: "first\nthird\n", keep: 5, want: "hird\n", dropped: 7},
		{name: "no line start left", in: "abcdefghij", keep: 5, want: "fghij", dropped: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tail := tailWriter{keep: tc.keep}

			// Written a byte at a time, as a response body may arrive.
			for _, piece := range strings.SplitAfter(tc.in, "") {
				if _, err := tail.Write([]byte(piece)); err != nil {
					t.Fatal(err)
				}
			}

			got, dropped, err := tail.bytes()
			if err != nil || string(got) != tc.want || dropped != tc.dropped {
				t.Fatalf("got %q dropped %d err %v, want %q dropped %d", got, dropped, err, tc.want, tc.dropped)
			}
		})
	}
}

// A release build's log over the bound was refused whole, so the trial kept
// nothing and said "the forge served no log" for the job that failed.
func TestJobLog_KeepsTheEndOfALogOverTheBoundAndCancelAnswers5xxAsServerSide(t *testing.T) {
	over := strings.Repeat("progress line\n", maxJobLog/14+1000) + "error: the build failed here\n"

	server, ca := labTLSServer(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.Method + " " + request.URL.Path {
		case "GET /api/v1/repos/fixture/fci-owned/actions/jobs/5287/logs":
			_, _ = io.WriteString(response, over)
		case "POST /api/v1/repos/fixture/fci-owned/actions/runs/436/cancel":
			response.WriteHeader(http.StatusInternalServerError)
		default:
			t.Errorf("unexpected request: %s %s", request.Method, request.URL.Path)
			response.WriteHeader(http.StatusNotFound)
		}
	}))

	original := http.DefaultTransport
	transport := original.(*http.Transport).Clone()
	transport.DialContext = (&dialRecorder{to: server.Listener.Addr().String()}).dial

	http.DefaultTransport = transport

	defer func() { http.DefaultTransport = original }()
	defer transport.CloseIdleConnections()

	target := Target{Forge: provider.ForgeForgejo, Host: labHost, Owner: "fixture", CAFile: "captured", caPEM: ca, accepted: true}

	log, err := jobLog(t, target, "fci-owned", RunJob{ID: 5287, Name: "Build: release files", Attempt: 1})
	if err != nil {
		t.Fatalf("an oversized log was refused: %v", err)
	}

	if len(log) > maxJobLog || !strings.HasPrefix(log, "[livetest: the first ") || !strings.HasSuffix(log, "error: the build failed here\n") {
		t.Fatalf("kept %d bytes, opening %q, ending %q", len(log), log[:60], log[len(log)-40:])
	}

	if _, err := retainJobLog(t.TempDir(), "fixture/fci-owned", RunJob{ID: 5287}, log); err != nil {
		t.Fatalf("the kept log cannot be retained as evidence: %v", err)
	}

	err = cancelObservedRunOnce(t, target)
	if !errors.Is(err, errServerSide) {
		t.Fatalf("a cancel the forge failed to answer is not server-side: %v", err)
	}
}

func cancelObservedRunOnce(t *testing.T, target Target) error {
	t.Helper()

	return discard(t.Context(), target, http.MethodPost, actionsURL(target, "fci-owned", "/actions/runs/436/cancel"), nil, http.StatusOK, http.StatusNoContent)
}
