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

const (
	releaseContextStep = "Derive release context"
	requestCommit      = "1111111111111111111111111111111111111111"
	otherCommit        = "2222222222222222222222222222222222222222"
)

// releaseContextFakeGit answers the two commit lookups of the step from the
// environment, and the tagger queries the engine makes for
// release-request/v1.2.3.
const releaseContextFakeGit = `#!/usr/bin/env bash
set -euo pipefail
case "$*" in
  'rev-parse --verify HEAD^{commit}') printf '%s\n' "${FAKE_HEAD_COMMIT:?}" ;;
  'rev-parse --verify refs/tags/'*'^{commit}') printf '%s\n' "${FAKE_TAG_COMMIT:?}" ;;
  'for-each-ref refs/tags/release-request/v1.2.3 --format=%(taggername) <%(taggeremail)>')
    printf 'Ada Lovelace <ada@example.test>\n' ;;
  'for-each-ref refs/tags/release-request/v1.2.3 --format=%(taggerdate:iso8601)')
    printf '1970-01-01 00:00:00 +0000\n' ;;
  *) exit 1 ;;
esac
`

// releaseContextSandbox is a prepare-release sandbox whose git is the fake.
func releaseContextSandbox(t *testing.T) (*sandbox, string) {
	t.Helper()

	box := newSandbox(t, "prepare-release")
	box.write("fakebin/git", releaseContextFakeGit, 0o700)

	return box, "PATH=" + filepath.Join(box.root, "fakebin") + ":" + os.Getenv("PATH")
}

// releaseContextEnv is a request whose checkout, tag and event commit agree.
func releaseContextEnv(path, ref string) []string {
	return []string{
		path, "RELEASE_CONTEXT_REF=" + ref, "VERIFY_RELEASE_REQUEST_SIGNATURE=false",
		"RELEASE_REQUEST_EVENT_SHA=" + requestCommit, "FAKE_HEAD_COMMIT=" + requestCommit, "FAKE_TAG_COMMIT=" + requestCommit,
	}
}

// The first step of the prepare action forwards the request ref and the fixed
// release policy flags to the engine, exactly, and stops on the engine's
// status. What the engine derives is checked against the built binary in
// TestPrepareReleaseContextEngine.
func TestPrepareReleaseContextCallsTheEngine(t *testing.T) {
	t.Parallel()
	script := actionStep(t, "prepare-release", releaseContextStep)

	argv := func(ref string) []string {
		return []string{
			"version", "derive-release", "--ref-name", ref,
			"--require-release-request", "--require-stable", "--trailer-mode", "coauthor-only",
		}
	}

	for _, test := range []struct {
		name, ref, status string
		want              int
	}{
		{"forwards the ref and the policy flags", "release-request/v1.2.3", "0", 0},
		{"keeps an option-shaped ref one value", "release-request/v1.2.3 --require-stable=false", "0", 0},
		{"propagates the engine status", "release-request/v1.2.3", "5", 5},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			box, path := releaseContextSandbox(t)

			got := box.run(script, append(releaseContextEnv(path, test.ref), "FAKE_ENGINE_STATUS="+test.status)...)
			if got.status != test.want || !slices.Equal(box.argv(), argv(test.ref)) {
				t.Fatalf("status %d argv %q; stderr %s", got.status, box.argv(), got.stderr)
			}
		})
	}

	for name, env := range map[string]string{
		"an empty ref":                       "RELEASE_CONTEXT_REF=",
		"a signature switch that is no bool": "VERIFY_RELEASE_REQUEST_SIGNATURE=yes",
	} {
		t.Run("refuses "+name+" before the engine", func(t *testing.T) {
			t.Parallel()

			box, path := releaseContextSandbox(t)

			got := box.run(script, append(releaseContextEnv(path, "release-request/v1.2.3"), env)...)
			if got.status == 0 || box.argv() != nil {
				t.Fatalf("status %d argv %q stderr %q", got.status, box.argv(), got.stderr)
			}
		})
	}
}

// After the engine has accepted the request, the step holds the checkout and
// the request tag to the commit the event authorized: the caller's input when
// it passes one, else the event's own commit. Either one on another commit
// stops the action before a renderer is installed or a key is loaded.
func TestPrepareReleaseContextBindsTheRequestToTheEventCommit(t *testing.T) {
	t.Parallel()
	script := actionStep(t, "prepare-release", releaseContextStep)

	for _, test := range []struct {
		name    string
		env     []string
		refused string
	}{
		{name: "checkout and tag are the event commit"},
		{name: "the caller's commit takes the place of the event's", env: []string{
			"RELEASE_REQUEST_SHA_INPUT=" + otherCommit, "FAKE_HEAD_COMMIT=" + otherCommit, "FAKE_TAG_COMMIT=" + otherCommit,
		}},
		{name: "the checkout is another commit", env: []string{"FAKE_HEAD_COMMIT=" + otherCommit}, refused: "does not match the event commit"},
		{name: "the tag names another commit", env: []string{"FAKE_TAG_COMMIT=" + otherCommit}, refused: "does not match the event commit"},
		{name: "the caller's commit is not the checkout", env: []string{"RELEASE_REQUEST_SHA_INPUT=" + otherCommit}, refused: "does not match the event commit"},
		{name: "an abbreviated commit", env: []string{"RELEASE_REQUEST_SHA_INPUT=1111111"}, refused: "release-request-sha must be"},
		{name: "an uppercase commit", env: []string{"RELEASE_REQUEST_SHA_INPUT=" + strings.ToUpper(strings.Repeat("a", 40))}, refused: "release-request-sha must be"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			box, path := releaseContextSandbox(t)

			got := box.run(script, append(releaseContextEnv(path, "release-request/v1.2.3"), test.env...)...)
			if (got.status == 0) != (test.refused == "") || !strings.Contains(got.stderr, test.refused) {
				t.Fatalf("status %d, want refusal %q; stderr %s", got.status, test.refused, got.stderr)
			}
		})
	}
}

// The step runs the engine built from this tree with its fixed policy flags:
// a stable release request yields the outputs the action's later steps read,
// and the two policy flags refuse a plain tag and a prerelease.
func TestPrepareReleaseContextEngine(t *testing.T) {
	t.Parallel()
	script := actionStep(t, "prepare-release", releaseContextStep)
	engine := forgeCIEngine(t)
	lib := string(reporoot.ReadFile(t, ".forgejo/bin/lib-forge-ci.sh"))

	run := func(t *testing.T, ref string) (*sandbox, result) {
		t.Helper()

		box, path := releaseContextSandbox(t)
		// The real resolver runs the built engine as a non-strict override.
		box.write(".forgejo/bin/lib-forge-ci.sh", lib, 0o600)

		return box, box.run(script, append(releaseContextEnv(path, ref), "FORGE_CI_BIN="+engine, "FORGE_CI_RUNNER=forgejo")...)
	}

	t.Run("derives the outputs from a stable release request", func(t *testing.T) {
		t.Parallel()

		box, got := run(t, "release-request/v1.2.3")
		if got.status != 0 {
			t.Fatalf("status %d; stderr %s", got.status, got.stderr)
		}

		lines := strings.Split(box.output(), "\n")
		for _, want := range []string{
			"release-tag=v1.2.3", "version=1.2.3", "release-request=release-request/v1.2.3",
			"Release-Request: release-request/v1.2.3", "Co-authored-by: Ada Lovelace <ada@example.test>",
		} {
			if !slices.Contains(lines, want) {
				t.Errorf("outputs lack line %q:\n%s", want, box.output())
			}
		}

		if !slices.ContainsFunc(lines, func(line string) bool { return strings.HasPrefix(line, "commit-trailers") }) {
			t.Errorf("outputs lack commit-trailers:\n%s", box.output())
		}
	})

	for _, ref := range []string{"v1.2.3", "release-request/v1.2.3-rc1"} {
		t.Run("refuses "+ref, func(t *testing.T) {
			t.Parallel()

			box, got := run(t, ref)
			if got.status == 0 || !strings.Contains(got.stdout+got.stderr, "vMAJOR.MINOR.PATCH") || box.output() != "" {
				t.Fatalf("status %d outputs %q stdout %q stderr %q", got.status, box.output(), got.stdout, got.stderr)
			}
		})
	}
}
