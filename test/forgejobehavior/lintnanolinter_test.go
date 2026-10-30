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

// fakeNanolinter records its argv, one argument per line, and exits with
// FAKE_NANOLINTER_STATUS.
const fakeNanolinter = `#!/usr/bin/env bash
test "${LANG:-}" = C.UTF-8 && test "${LC_ALL:-}" = C.UTF-8 || exit 97
printf '%s\n' "$@" >"${FAKE_NANOLINTER_LOG:?}"
results='[]'
if [ "${FAKE_NANOLINTER_STATUS:-0}" = 1 ]; then
  results='[{"ruleId":"format","properties":{"source":"curated","check_id":"format","class":"formatter","status":"fail"}}]'
fi
printf '{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"nanolinter"}},"results":%s}]}\n' "$results"
exit "${FAKE_NANOLINTER_STATUS:-0}"
`

// The lint step runs the job's own nanolinter with the mandated floor, the
// caller's exclusions and the offline switch, and its status is nanolinter's.
// It installs nothing: without nanolinter on PATH it stops, and says what the
// job is missing. The real engine applies the immutable policy after execution.
func TestLintNanolinter(t *testing.T) {
	t.Parallel()
	action := loadAction(t, "lint-nanolinter")
	action.requireSteps(t, 1)
	action.requireStepEnv(t, 0, map[string]string{
		"LINT_REQUIRED_LINTS": "${{ inputs.required-lints }}",
		"LINT_DISABLE_CHECKS": "${{ inputs.disable-checks }}",
		"LINT_OFFLINE":        "${{ inputs.offline }}",
	})

	if floor := action.Inputs["required-lints"].Default; floor != "secrets,sast" {
		t.Fatalf("default lint floor = %v, want secrets,sast as on the other forges", floor)
	}

	script := action.Runs.Steps[0].Run
	run := func(t *testing.T, env ...string) (result, []string) {
		t.Helper()

		box := newSandbox(t, "lint-nanolinter")
		box.write("tools/nanolinter", fakeNanolinter, 0o700)
		box.write(".engine/bin/lib-forge-ci.sh", string(reporoot.ReadFile(t, ".engine/bin/lib-forge-ci.sh")), 0o600)
		for _, arch := range []string{"amd64", "arm64"} {
			file := ".engine/bin/forge-ci-linux-" + arch + ".sha256"
			box.write(file, string(reporoot.ReadFile(t, file)), 0o600)
		}
		log := filepath.Join(box.root, "nanolinter-argv")

		got := box.run(script, append([]string{
			"PATH=" + filepath.Join(box.root, "tools") + ":" + os.Getenv("PATH"), "FAKE_NANOLINTER_LOG=" + log,
			"FORGE_CI_BIN=" + forgeCIEngine(t),
		}, env...)...)
		if box.argv() != nil {
			t.Fatalf("the lint step ran the layer's binary: %q", box.argv())
		}

		data, err := os.ReadFile(log)
		if os.IsNotExist(err) {
			return got, nil
		}

		if err != nil {
			t.Fatal(err)
		}

		return got, strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	}

	for _, test := range []struct {
		name string
		env  []string
		argv []string
	}{
		{
			"the default floor",
			[]string{"LINT_REQUIRED_LINTS=secrets,sast", "LINT_DISABLE_CHECKS=", "LINT_OFFLINE=false"},
			[]string{"verify", "--require", "secrets,sast", "--report", "sarif", "--out", "-"},
		},
		{
			"exclusions and offline",
			[]string{"LINT_REQUIRED_LINTS=secrets", "LINT_DISABLE_CHECKS=osv,trivy-fs", "LINT_OFFLINE=true"},
			[]string{"verify", "--require", "secrets", "--disable-check", "osv,trivy-fs", "--offline", "--report", "sarif", "--out", "-"},
		},
		{
			"no floor",
			[]string{"LINT_REQUIRED_LINTS=", "LINT_DISABLE_CHECKS=", "LINT_OFFLINE=false"},
			[]string{"verify", "--report", "sarif", "--out", "-"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, argv := run(t, test.env...)
			if got.status != 0 || !slices.Equal(argv, test.argv) {
				t.Fatalf("status %d argv %q, want %q; stderr %s", got.status, argv, test.argv, got.stderr)
			}
		})
	}

	t.Run("a blocking finding fails the policy gate", func(t *testing.T) {
		t.Parallel()

		got, argv := run(t, "LINT_REQUIRED_LINTS=secrets,sast", "LINT_DISABLE_CHECKS=", "LINT_OFFLINE=false", "FAKE_NANOLINTER_STATUS=1")
		if got.status != 1 || argv == nil || !strings.Contains(got.stdout, "result=findings") {
			t.Fatalf("status %d argv %q", got.status, argv)
		}
	})

	t.Run("an offline value that is not a boolean is refused before nanolinter", func(t *testing.T) {
		t.Parallel()

		got, argv := run(t, "LINT_REQUIRED_LINTS=secrets,sast", "LINT_DISABLE_CHECKS=", "LINT_OFFLINE=yes")
		if got.status == 0 || argv != nil || !strings.Contains(got.stderr, "offline must be true or false") {
			t.Fatalf("status %d argv %q stderr %q", got.status, argv, got.stderr)
		}
	})

	t.Run("a job without nanolinter is refused, not skipped", func(t *testing.T) {
		t.Parallel()

		box := newSandbox(t, "lint-nanolinter")

		// The step uses shell builtins only until it calls nanolinter.
		got := box.run(script, "PATH="+filepath.Join(box.root, "empty"),
			"LINT_REQUIRED_LINTS=secrets,sast", "LINT_DISABLE_CHECKS=", "LINT_OFFLINE=false")
		if got.status != 69 || !strings.Contains(got.stderr, "nanolinter is not on PATH") {
			t.Fatalf("status %d stderr %q", got.status, got.stderr)
		}
	})
}
