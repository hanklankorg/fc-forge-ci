// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package github_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/adapters/github"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/lender"
	"github.com/diggsweden/forge-ci/internal/runcontext"
)

type dispatchCall struct {
	path, authorization string
	body                map[string]any
}

func lenderServer(t *testing.T, status int, calls *[]dispatchCall) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)

		var body map[string]any

		_ = json.Unmarshal(raw, &body)
		*calls = append(*calls, dispatchCall{path: request.Method + " " + request.URL.Path, authorization: request.Header.Get("Authorization"), body: body})

		if status != http.StatusNoContent {
			response.Header().Set("Content-Type", "application/json")
		}

		response.WriteHeader(status)

		if status != http.StatusNoContent {
			_, _ = io.WriteString(response, `{"message":"refused"}`)
		}
	}))
	t.Cleanup(server.Close)

	return server
}

func TestLender_DispatchesTheLenderWorkflowWithArchitectureAndWait(t *testing.T) {
	t.Parallel()

	var calls []dispatchCall

	server := lenderServer(t, http.StatusNoContent, &calls)
	lend := &github.Lender{Owner: "acme", Repository: "lender", Ref: "main", APIBase: server.URL, Token: runcontext.OperatorCredential("lender-canary")}

	if err := lend.Request(t.Context(), lender.Request{Arch: lender.ArchARM64, TTL: 20 * time.Minute}); err != nil {
		t.Fatal(err)
	}

	if len(calls) != 1 {
		t.Fatalf("calls=%+v, want one dispatch", calls)
	}

	call := calls[0]
	if call.path != "POST /repos/acme/lender/actions/workflows/lend-runner.yml/dispatches" || call.authorization != "Bearer lender-canary" {
		t.Errorf("call=%+v", call)
	}

	inputs, _ := call.body["inputs"].(map[string]any)
	if call.body["ref"] != "main" || inputs["arch"] != "arm64" || inputs["ttl-minutes"] != "20" {
		t.Errorf("body=%v, want ref main, arch arm64, ttl-minutes 20", call.body)
	}

	if got := lend.Name(); got != "github:acme/lender" {
		t.Errorf("name=%q", got)
	}
}

// A runner-issued token is bound to the forge that issued it. The dispatch
// goes to GitHub from a job on another forge, so such a token is withheld
// and nothing is sent.
func TestLender_WithholdsACredentialBoundToAnotherForge(t *testing.T) {
	t.Parallel()

	var calls []dispatchCall

	server := lenderServer(t, http.StatusNoContent, &calls)
	bound := runcontext.Token().Resolve(envMap{"GITHUB_ACTIONS": "true", "GITHUB_SERVER_URL": "https://codefloe.com", "GITHUB_TOKEN": "forgejo-job-canary"}.lookup)
	lend := &github.Lender{Owner: "acme", Repository: "lender", Ref: "main", APIBase: server.URL, Token: bound}

	err := lend.Request(t.Context(), lender.Request{Arch: lender.ArchAMD64, TTL: 20 * time.Minute})
	if !errors.Is(err, errs.ErrUsage) || !strings.Contains(err.Error(), "--token-file") {
		t.Fatalf("err=%v, want a usage error naming --token-file", err)
	}

	if len(calls) != 0 {
		t.Errorf("a bound credential reached the lender: %+v", calls)
	}
}

func TestLender_ClassifiesGitHubsAnswers(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		status int
		want   error
		detail string
	}{
		{http.StatusNotFound, errs.ErrMissingInput, "lend-runner.yml on branch main of acme/lender"},
		{http.StatusForbidden, errs.ErrPermissionDenied, ""},
		{http.StatusUnprocessableEntity, errs.ErrValidation, ""},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			t.Parallel()

			var calls []dispatchCall

			server := lenderServer(t, tc.status, &calls)
			lend := &github.Lender{Owner: "acme", Repository: "lender", Ref: "main", APIBase: server.URL, Token: runcontext.OperatorCredential("lender-canary")}

			err := lend.Request(t.Context(), lender.Request{Arch: lender.ArchAMD64, TTL: 20 * time.Minute})
			if !errors.Is(err, tc.want) {
				t.Fatalf("status %d: err=%v, want %v", tc.status, err, tc.want)
			}

			if !strings.Contains(err.Error(), tc.detail) {
				t.Errorf("err=%q lacks %q", err.Error(), tc.detail)
			}
		})
	}
}

type envMap map[string]string

func (m envMap) lookup(key string) string { return m[key] }
