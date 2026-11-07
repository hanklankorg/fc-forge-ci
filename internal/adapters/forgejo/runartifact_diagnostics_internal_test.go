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

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestFinalizeArtifact_TransportDiagnosticsAndNoReplay(t *testing.T) {
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

	for _, status := range []int{http.StatusOK, http.StatusInternalServerError} {
		t.Run(fmt.Sprintf("response-%d", status), func(t *testing.T) {
			logs.Reset()

			calls := 0
			client := &Provider{HTTPClient: &http.Client{Transport: responseBoundaryTransport(func(request *http.Request) (*http.Response, error) {
				calls++

				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(private)), Request: request}, nil
			})}}

			err := client.finalizeArtifact(t.Context(), runtimeUploadCreds{url: "https://" + private + ".invalid", token: private, runID: "7"}, "runtime-cli", 1234)
			if status == http.StatusOK && err != nil || status == http.StatusInternalServerError && !errors.Is(err, errs.ErrDependencyUnavailable) {
				t.Fatalf("unexpected finalization status: %v", err)
			}

			if calls != 1 || !strings.Contains(logs.String(), fmt.Sprintf("status=%d", status)) || !strings.Contains(logs.String(), "elapsed=") {
				t.Fatalf("missing response diagnostics or replayed PATCH: calls=%d", calls)
			}

			if strings.Contains(fmt.Sprint(err)+logs.String(), private) {
				t.Fatal("response diagnostics exposed private data")
			}
		})
	}
}
