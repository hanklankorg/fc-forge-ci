// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/adapters/httpretry"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestFinalizeArtifact_DiagnosticsAndReplay(t *testing.T) {
	// Capture debug logs too: credentials must not become visible with -v.
	var logs bytes.Buffer

	previous := slog.Default()

	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	const private = "PRIVATE-artifact-canary"
	for _, test := range []struct {
		name, category string
		cause          error
	}{
		{"deadline", "request deadline exceeded", context.DeadlineExceeded},
		{"canceled", "request canceled", context.Canceled},
		{"reset", "connection reset", syscall.ECONNRESET},
		{"closed", "connection closed before response completed", io.EOF},
		{"unknown", "unclassified network failure", errors.New(private)},
	} {
		t.Run(test.name, func(t *testing.T) {
			logs.Reset()

			calls := 0
			client := &Provider{HTTPClient: &http.Client{Transport: responseBoundaryTransport(func(request *http.Request) (*http.Response, error) {
				calls++

				if request.Method != http.MethodPatch || request.Header.Get("Authorization") != "Bearer "+private {
					t.Fatal("finalization lost its method or authentication")
				}

				return nil, fmt.Errorf("%s: %w", private, test.cause)
			})}}

			err := client.finalizeArtifact(t.Context(), runtimeUploadCreds{url: "https://" + private + ".invalid", token: private, runID: "7"}, "runtime-cli", 1234)
			if !errors.Is(err, test.cause) || calls != 1 {
				t.Fatalf("cause lost or finalization replayed: calls=%d, error=%v", calls, err)
			}

			for _, want := range []string{`finalize artifact "runtime-cli"`, "PATCH", "1234 bytes", "elapsed", "server outcome unknown, not retried", test.category} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("diagnostic missing %q: %v", want, err)
				}
			}

			if strings.Contains(err.Error()+logs.String(), private) {
				t.Fatal("error or debug log exposed private transport data")
			}

			if !strings.Contains(logs.String(), "Finalizing Forgejo artifact") || !strings.Contains(logs.String(), "artifact_bytes=1234") {
				t.Fatal("missing safe finalization progress log")
			}
		})
	}

	// Only an answered HTTP 500 is replayed, up to the retry bound: Forgejo
	// refuses the whole confirmation when another artifact's chunk vanishes
	// while it lists the run's chunks, and repeating it is idempotent.
	for _, test := range []struct {
		name       string
		statuses   []int
		want       error
		calls      int
		diagnostic string // in the error, when there is one
		logged     string // the logged final status, when a response ends it
	}{
		{"accepted", []int{http.StatusOK}, nil, 1, "", "status=200"},
		{"refused-then-accepted", []int{http.StatusInternalServerError, http.StatusOK}, nil, 2, "", "status=200"},
		{"refused-throughout", []int{http.StatusInternalServerError}, errs.ErrDependencyUnavailable, httpretry.DefaultMaxAttempts, "HTTP 500, retries exhausted", ""},
		{"conflict", []int{http.StatusConflict}, errs.ErrValidation, 1, "HTTP 409", "status=409"},
	} {
		t.Run(test.name, func(t *testing.T) {
			logs.Reset()

			calls := 0
			fake := responseBoundaryTransport(func(request *http.Request) (*http.Response, error) {
				calls++

				if body, err := io.ReadAll(request.Body); err != nil || string(body) != `{"Size":1234}` {
					t.Fatalf("attempt %d sent body %q, %v", calls, body, err)
				}

				status := test.statuses[min(calls, len(test.statuses))-1]

				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(private)), Request: request}, nil
			})
			client := &Provider{HTTPClient: &http.Client{Transport: httpretry.NewTransport(httpretry.Config{
				Inner: fake, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond,
			})}}

			// The retry transport's debug log names the host of a replayed
			// request, as it does for every retried runtime read.
			err := client.finalizeArtifact(t.Context(), runtimeUploadCreds{url: "https://runtime.invalid", token: private, runID: "7"}, "runtime-cli", 1234)
			if test.want == nil && err != nil || test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("finalization error = %v, want %v", err, test.want)
			}

			if calls != test.calls {
				t.Fatalf("PATCH sent %d times, want %d", calls, test.calls)
			}

			if err != nil && (!strings.Contains(err.Error(), test.diagnostic) || strings.Contains(err.Error(), "outcome unknown")) {
				t.Errorf("diagnostic = %v, want %q and an answered outcome", err, test.diagnostic)
			}

			if test.logged != "" && (!strings.Contains(logs.String(), test.logged) || !strings.Contains(logs.String(), "elapsed=")) {
				t.Fatalf("missing response diagnostics %q", test.logged)
			}

			if !strings.Contains(fmt.Sprint(err)+logs.String(), "elapsed") {
				t.Fatal("missing elapsed diagnostics")
			}

			if strings.Contains(fmt.Sprint(err)+logs.String(), private) {
				t.Fatal("response diagnostics exposed private data")
			}
		})
	}
}
