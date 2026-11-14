// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package githubconsumer_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"codefloe.com/itiquette/forge-lab-contract/githubcontext"
	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

// Signing admission runs before the trial creates a repository: a refused
// input ends runTrial with no create, an admitted one reaches it. What each
// input admits is releaseSigning's own tests' (signing_test.go).
// Subprocesses let the real runTrial and native SDK terminate via testing.Fatal
// without weakening production error handling or introducing a trial interface.
func TestRunTrialSigningAdmission(t *testing.T) {
	check := func(name string, creates int, failure string) {
		t.Run(name, func(t *testing.T) {
			command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRunTrialSigningBoundaryChild$", "-test.v")

			command.Env = append(os.Environ(), "FORGE_CI_SIGNING_BOUNDARY_CASE="+name)
			body, err := command.CombinedOutput()

			output := string(body)
			if err == nil || !strings.Contains(output, failure) ||
				!strings.Contains(output, fmt.Sprintf("BOUNDARY creates=%d unexpected=0", creates)) {
				t.Fatalf("runTrial admission did not reach the expected boundary: %v\n%s", err, output)
			}
		})
	}
	check("missing", 0, "read the fixture's release workflow")
	check("wildcard", 0, "literal bot-email")
	check("valid", 1, "livetest: create")
	check("pull-request", 1, "livetest: create")
}

func TestRunTrialSigningBoundaryChild(t *testing.T) {
	name := os.Getenv("FORGE_CI_SIGNING_BOUNDARY_CASE")
	if name == "" {
		t.Skip("inert subprocess only")
	}

	var creates, unexpected atomic.Int32

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")

		switch request.Method + " " + request.URL.Path {
		case "GET /orgs/test-org":
			_, _ = writer.Write([]byte(`{"id":42,"login":"test-org"}`))
		case "GET /user":
			_, _ = writer.Write([]byte(`{"id":7,"login":"actor"}`))
		case "POST /orgs/test-org/repos":
			creates.Add(1)
			// Structured refusal stops the native client before any remote allocation,
			// SSH key generation, source push or artifact/runner activity.
			writer.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = writer.Write([]byte(`{"message":"inert-create-stop"}`))
		default:
			unexpected.Add(1)
			writer.WriteHeader(http.StatusForbidden)
		}
	}))
	defer server.Close()

	t.Cleanup(func() { fmt.Printf("BOUNDARY creates=%d unexpected=%d\n", creates.Load(), unexpected.Load()) })

	// OpenGitHubTrial clones the default *http.Transport. Confine that clone
	// to this TLS fixture, verifying its test certificate rather than disabling TLS.
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12}
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	original := http.DefaultTransport

	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = original; transport.CloseIdleConnections() }()

	root := t.TempDir()

	fixtures := filepath.Join(root, "fixtures/workflows")
	if err := os.MkdirAll(fixtures, 0o700); err != nil {
		t.Fatal(err)
	}

	event, workflow, email := livetest.TrialRelease, "release.yml", "release@example.org"
	if name == "wildcard" {
		email = "*"
	}

	if name == "pull-request" {
		event, workflow, email = name, "pullrequest.yml", "*"
	}

	if name != "missing" {
		if err := os.WriteFile(filepath.Join(fixtures, "release.yml"), signingWorkflow(t, email, true, false), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if event != livetest.TrialRelease {
		if err := os.WriteFile(filepath.Join(fixtures, workflow), []byte("jobs: {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	settings, err := livetest.ParseGitHubTrialSettings(event, []string{"workflow=" + workflow})
	if err != nil {
		t.Fatal(err)
	}

	// The release signs with GPG, so the trial has no key of its own to make
	// before the create this boundary stops at.
	if err := os.MkdirAll(filepath.Join(root, ".forge-ci"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, ".forge-ci/artifacts.yml"), []byte("sign:\n  method: gpg\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	document := githubcontext.Context{Version: githubcontext.Version, Session: strings.Repeat("a", 32),
		Organization: githubcontext.Identity{ID: 42, Login: "test-org"}, Actor: githubcontext.Identity{ID: 7, Login: "actor"},
		Prefix: "ex-", Token: "INERT-test-token", FixtureManifestFile: filepath.Join(root, "manifest.json"), Capabilities: []string{"repositories"}}

	body, err := json.Marshal(struct {
		githubcontext.Context

		Repositories []githubcontext.Repository `json:"repositories"`
	}{document, []githubcontext.Repository{}})
	if err != nil {
		t.Fatal(err)
	}

	contextFile := filepath.Join(root, "context.json")
	if err := os.WriteFile(contextFile, body, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv(livetest.GitHubContextEnv, contextFile)
	t.Setenv("FORGE_CI_LIVE_DEMO", "")
	trial := livetest.OpenGitHubTrial(t, 42)
	runTrial(t, trial, livetest.GitHubEngine{}, root, "fixtures", settings, livetest.ConsumerVisibility{}, "")
	t.Fatal("inert create refusal or invalid signing input must stop runTrial")
}
