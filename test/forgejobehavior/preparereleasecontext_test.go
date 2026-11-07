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
)

// releaseContextFakeGit answers only the tagger queries used in derivation.
// Source identity admission belongs to release validate-request, not this step.
const releaseContextFakeGit = `#!/usr/bin/env bash
set -euo pipefail
case "$*" in
  'for-each-ref refs/tags/release-request/v1.2.3 --format=%(taggername) <%(taggeremail)>'|'for-each-ref refs/tags/release-request/v1.2.3-beta.2 --format=%(taggername) <%(taggeremail)>')
    printf 'Release Administrator <admin@example.test>\n' ;;
  'for-each-ref refs/tags/release-request/v1.2.3 --format=%(taggerdate:iso8601)'|'for-each-ref refs/tags/release-request/v1.2.3-beta.2 --format=%(taggerdate:iso8601)')
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

// releaseContextEnv supplies the key-free derivation inputs.
func releaseContextEnv(path, ref string) []string {
	return []string{
		path, "RELEASE_CONTEXT_REF=" + ref, "VERIFY_RELEASE_REQUEST_SIGNATURE=false",
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
			"version", "derive-release", "--release-policy", "stable", "--ref-name", ref,
			"--require-release-request", "--trailer-mode", "coauthor-only",
		}
	}

	for _, test := range []struct {
		name, ref, status string
		want              int
	}{
		{"forwards the ref and the policy flags", "release-request/v1.2.3", "0", 0},
		{"keeps an option-shaped ref one value", "release-request/v1.2.3 --release-policy stable-prerelease", "0", 0},
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

// The existing Go request owner checks the full OID, request tag and HEAD.
// Keep that admission in the key-free pre-signing step, not shell derivation.
func TestPrepareReleaseSourceBindingIsOwnedByRequestValidation(t *testing.T) {
	t.Parallel()

	derive := actionStep(t, "prepare-release", releaseContextStep)
	if strings.Contains(derive, "git rev-parse") || strings.Contains(derive, "expected_release_request_sha") {
		t.Fatal("derivation reintroduced source identity decisions")
	}

	validate := actionStep(t, "prepare-release", "Revalidate release request before loading signing key")
	for _, fragment := range []string{"release validate-request", `--expected-commit "${expected_release_request_sha}"`, "unset SSH_SIGNING_KEY"} {
		if !strings.Contains(validate, fragment) {
			t.Fatalf("pre-key validation lacks %s", fragment)
		}
	}
}

// The step runs this tree's engine: stable is the default, prereleases require
// explicit opt-in. Source binding is separately admitted before signing.
func TestPrepareReleaseContextEngine(t *testing.T) {
	t.Parallel()
	script := actionStep(t, "prepare-release", releaseContextStep)
	engine := forgeCIEngine(t)
	lib := string(reporoot.ReadFile(t, ".engine/bin/lib-forge-ci.sh"))

	run := func(t *testing.T, ref string, extra ...string) (*sandbox, result) {
		t.Helper()

		box, path := releaseContextSandbox(t)
		// The real resolver runs the built engine as a non-strict override.
		box.write(".engine/bin/lib-forge-ci.sh", lib, 0o600)

		env := append(releaseContextEnv(path, ref), "FORGE_CI_BIN="+engine, "FORGE_CI_RUNNER=forgejo")

		return box, box.run(script, append(env, extra...)...)
	}
	t.Run("explicit policy admits a numbered beta", func(t *testing.T) {
		box, got := run(t, "release-request/v1.2.3-beta.2", "RELEASE_POLICY=stable-prerelease")
		if got.status != 0 || !strings.Contains(box.output(), "release-tag=v1.2.3-beta.2") {
			t.Fatalf("beta policy was not carried to engine: status=%d outputs=%s stderr=%s", got.status, box.output(), got.stderr)
		}
	})

	t.Run("derives the outputs from a stable release request", func(t *testing.T) {
		t.Parallel()

		box, got := run(t, "release-request/v1.2.3")
		if got.status != 0 {
			t.Fatalf("status %d; stderr %s", got.status, got.stderr)
		}

		lines := strings.Split(box.output(), "\n")
		for _, want := range []string{
			"release-tag=v1.2.3", "version=1.2.3", "release-request=release-request/v1.2.3",
			"Release-Request: release-request/v1.2.3", "Co-authored-by: Release Administrator <admin@example.test>",
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
