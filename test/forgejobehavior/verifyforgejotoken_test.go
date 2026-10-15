// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

//nolint:gosec // G101: a marker the test searches the output for, not a credential.
const verifyTokenSecret = "token-value-DO-NOT-LOG"

// verifyTokenFakeEngine records its argv, stdin and environment, then fails the
// way FAKE_MODE asks. The token is split around a CR or control byte in the
// split modes so a sanitizer that redacts before removing controls rebuilds it.
//
//nolint:gosec // G101: a fixture script, no credential.
const verifyTokenFakeEngine = `#!/usr/bin/env bash
set -euo pipefail
printf '%s\0' "$@" >"${FAKE_ARGV_LOG:?}"
token=$(cat)
printf '%s' "${token}" >"${FAKE_STDIN_LOG:?}"
env >"${FAKE_ENV_LOG:?}"
case "${FAKE_MODE:?}" in
  success) exit 0 ;;
  permission) printf 'Authorization: token %s\n' "${token}" >&2; exit 77 ;;
  network) echo 'dial tcp: connection refused' >&2; exit 69 ;;
  validation) echo 'unexpected validation response' >&2; exit 1 ;;
  invocation) echo 'invalid server URL' >&2; exit 2 ;;
  internal)
    printf 'Authorization: token %s\n' "${token}" >&2
    echo 'Set-Cookie: session=another-secret' >&2
    echo 'request to https://user:password@forgejo.invalid failed' >&2
    printf '\001server unavailable for token %s\n' "${token}" >&2
    exit 70 ;;
  silent) exit 78 ;;
  split-cr) printf 'server unavailable for token %s\r%s\n' "${token:0:15}" "${token:15}" >&2; exit 70 ;;
  split-control) printf 'server unavailable for token %s\001%s\n' "${token:0:15}" "${token:15}" >&2; exit 70 ;;
esac
exit 64
`

// The action hands the token to forge-ci only over stdin, maps EX_NOPERM to
// the credential message and every other status to a bounded, redacted
// operational diagnostic, and never prints the token.
func TestVerifyForgejoToken(t *testing.T) {
	t.Parallel()

	const (
		repository  = "itiquette/example"
		operational = "ERROR: forgejo API token verification could not be completed "
	)

	split := func(sep string) string { return "itiquette/" + verifyTokenSecret[:15] + sep + verifyTokenSecret[15:] }

	for _, test := range []verifyTokenCase{
		{name: "success is concise and names the repository", mode: "success",
			stdout: "Forgejo API token verified for itiquette/example\n"},
		{name: "EX_NOPERM gets the credential message", mode: "permission",
			stderr: "ERROR: forgejo API token was rejected or lacks read:repository scope for itiquette/example\n"},
		{name: "network failure keeps its diagnostic", mode: "network",
			contains: []string{operational + "(forge-ci exit 69): dial tcp: connection refused"}},
		{name: "invocation failure keeps its diagnostic", mode: "invocation",
			contains: []string{operational + "(forge-ci exit 2): invalid server URL"}},
		{name: "generic validation failure keeps its diagnostic", mode: "validation",
			contains: []string{operational + "(forge-ci exit 1): unexpected validation response"}},
		{name: "internal failure keeps the first safe redacted line", mode: "internal",
			contains:  []string{operational + "(forge-ci exit 70): server unavailable for token [REDACTED]"},
			forbidden: []string{"Authorization", "another-secret", "user:password"}},
		{name: "token split by CR is redacted", mode: "split-cr",
			contains: []string{operational + "(forge-ci exit 70): server unavailable for token [REDACTED]"}},
		{name: "token split by a control byte is redacted", mode: "split-control",
			contains: []string{operational + "(forge-ci exit 70): server unavailable for token [REDACTED]"}},
		{name: "repository with a CR-split token is redacted", mode: "success", repository: split("\r"),
			stdout: "Forgejo API token verified for itiquette/[REDACTED]\n"},
		{name: "repository with a control-split token is redacted", mode: "success", repository: split("\x01"),
			stdout: "Forgejo API token verified for itiquette/[REDACTED]\n"},
		{name: "silent failure has a bounded fallback", mode: "silent",
			contains: []string{"forge-ci exit 78; no safe diagnostic was emitted"}},
		{name: "missing binary keeps the resolver diagnostic", mode: "success", realResolver: true,
			contains: []string{operational + "(forge-ci exit 69): ERROR: forge-ci binary is missing"}},
		{name: "incompatible binary keeps its exec status", mode: "success", engine: "\x7fELF-invalid\n",
			contains: []string{operational + "(forge-ci exit 126)"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if test.repository == "" {
				test.repository = repository
			}

			box := verifyTokenSandbox(t)
			if test.engine != "" {
				box.write(".forgejo/bin/forge-ci", test.engine, 0o700)
			}

			env := []string{
				"FORGEJO_API_TOKEN=" + verifyTokenSecret, "VERIFY_REPO=" + test.repository,
				"VERIFY_SERVER=https://forgejo.invalid", "FAKE_MODE=" + test.mode,
				"FAKE_STDIN_LOG=" + filepath.Join(box.root, "stdin"), "FAKE_ENV_LOG=" + filepath.Join(box.root, "env"),
			}
			if test.realResolver {
				box.write(".forgejo/bin/lib-forge-ci.sh", string(reporoot.ReadFile(t, ".forgejo/bin/lib-forge-ci.sh")), 0o600)
				env = append(env, "FORGE_CI_BIN="+filepath.Join(box.root, "missing-forge-ci"))
			}

			got := box.run(verifyTokenStep, env...)
			assertVerifyTokenOutput(t, test, got)

			if test.engine == "" && !test.realResolver {
				verifyTokenCheckTransport(t, box, test.repository)
			}
		})
	}

	t.Run("refuses an empty token before the engine", func(t *testing.T) {
		t.Parallel()
		s := verifyTokenSandbox(t)

		got := s.run(verifyTokenStep, "FORGEJO_API_TOKEN=", "VERIFY_REPO="+repository)
		if got.status != 1 || got.stderr != "ERROR: forgejo API token is empty\n" || got.stdout != "" || s.argv() != nil {
			t.Fatalf("status %d stdout %q stderr %q argv %q", got.status, got.stdout, got.stderr, s.argv())
		}
	})
}

// verifyTokenStep is the action's single step; its script lives next to
// action.yml rather than inline.
const verifyTokenStep = `bash "${GITHUB_ACTION_PATH}/verify-forgejo-token.sh"` + "\n"

// verifyTokenCase is one verify-forgejo-token run and the output it must
// produce.
type verifyTokenCase struct {
	name, mode, repository string
	engine                 string // bin/forge-ci content; "" keeps the recording fake
	realResolver           bool   // resolve a missing FORGE_CI_BIN with the real library
	stdout, stderr         string // exact when set
	contains               []string
	forbidden              []string
}

// assertVerifyTokenOutput checks the status and the redacted, bounded output
// of one verify-forgejo-token run.
func assertVerifyTokenOutput(t *testing.T, test verifyTokenCase, got result) {
	t.Helper()

	if strings.Contains(got.stdout+got.stderr, verifyTokenSecret) {
		t.Fatalf("output exposed the token: %q %q", got.stdout, got.stderr)
	}

	wantStatus := 1
	if test.mode == "success" && test.engine == "" && !test.realResolver {
		wantStatus = 0
	}

	if got.status != wantStatus {
		t.Fatalf("status %d, want %d; stderr %q", got.status, wantStatus, got.stderr)
	}

	if test.stdout != "" && (got.stdout != test.stdout || got.stderr != "") {
		t.Fatalf("stdout %q stderr %q, want stdout %q", got.stdout, got.stderr, test.stdout)
	}

	if test.stderr != "" && (got.stderr != test.stderr || got.stdout != "") {
		t.Fatalf("stdout %q stderr %q, want stderr %q", got.stdout, got.stderr, test.stderr)
	}

	for _, want := range test.contains {
		if !strings.Contains(got.stderr, want) {
			t.Fatalf("stderr %q lacks %q", got.stderr, want)
		}
	}

	for _, bad := range append(test.forbidden, "was rejected") {
		if test.mode != "permission" && strings.Contains(got.stderr, bad) {
			t.Fatalf("stderr %q contains %q", got.stderr, bad)
		}
	}
}

func verifyTokenSandbox(t *testing.T) *sandbox {
	t.Helper()
	s := newSandbox(t, "verify-forgejo-token")
	s.write(".forgejo/actions/verify-forgejo-token/verify-forgejo-token.sh",
		string(reporoot.ReadFile(t, ".forgejo/actions/verify-forgejo-token/verify-forgejo-token.sh")), 0o600)
	s.write(".forgejo/bin/forge-ci", verifyTokenFakeEngine, 0o700)

	return s
}

// verifyTokenCheckTransport asserts the token reached the engine on stdin only:
// not in argv and not in its environment.
func verifyTokenCheckTransport(t *testing.T, box *sandbox, repository string) {
	t.Helper()

	if want := []string{"validate", "auth", "token", "--repository", repository, "--token-file", "-"}; !slices.Equal(box.argv(), want) {
		t.Fatalf("argv %q, want %q", box.argv(), want)
	}

	stdin, err := os.ReadFile(filepath.Join(box.root, "stdin"))
	if err != nil || string(stdin) != verifyTokenSecret {
		t.Fatalf("engine stdin %q, %v", stdin, err)
	}

	env, err := os.ReadFile(filepath.Join(box.root, "env"))
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(string(env), "\n")
	if strings.Contains(string(env), verifyTokenSecret) ||
		!slices.Contains(lines, "FORGE_CI_PROVIDER=forgejo") ||
		!slices.Contains(lines, "FORGEJO_SERVER_URL=https://forgejo.invalid") {
		t.Fatalf("engine environment leaks the token or lacks the provider settings:\n%s", env)
	}
}
