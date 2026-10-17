// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

// Exercise the actual launcher with inert build/preflight collaborators and
// the signal fixture in this test executable. No provider or container is used.
func TestLiveLauncherWaitsForCleanupAndReleasesLock(t *testing.T) {
	for _, mode := range []string{"launcher signal", "process-group signal", "just recipe signal", "just release recipe signal"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			script, packageDir := newLauncherFixture(t, root)

			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()

			gnu := testenv.GNUPath(t, "bash", "sed", "sha256sum", "install", "find")
			command := liveLauncherCommand(t, ctx, root, script, mode, gnu)
			command.Env = []string{"PATH=" + filepath.Join(root, "tools") + ":" + testenv.ToolPath(t, "flock", "jq") + ":" + gnu + ":/usr/bin:/bin", "HOME=" + root,
				"XDG_STATE_HOME=" + filepath.Join(root, "state"), "TMPDIR=" + root,
				"FORGE_CI_LIVE_SIGNAL_FIXTURE=" + root, "FORGE_CI_LIVE_FIXTURE_BINARY=" + os.Args[0], "FORGE_CI_LIVE_PACKAGE_DIR=" + packageDir,
				"FORGE_CI_LIVE_CLEANUP_HANDSHAKE=1"}
			command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

			var output bytes.Buffer

			command.Stdout, command.Stderr = &output, &output
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}

			defer func() { _ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }()

			done := make(chan error, 1)
			go func() { done <- command.Wait() }()

			waitFixtureFile(t, ctx, root, "ready", done)
			lock := filepath.Join(root, "state/forge-ci/live.lock")
			requireLiveLock(t, ctx, lock, true)

			if mode == "process-group signal" {
				if err := syscall.Kill(-command.Process.Pid, syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			} else if err := command.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}

			waitFixtureFile(t, ctx, root, "cleaning", done)
			requireLiveLock(t, ctx, lock, true)

			if err := os.WriteFile(filepath.Join(root, "finish-cleanup"), nil, 0o600); err != nil {
				t.Fatal(err)
			}

			err := <-done

			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 130 {
				t.Fatalf("launcher status: %v\n%s", err, output.String())
			}

			if _, err := os.Stat(filepath.Join(root, "cleaned")); err != nil {
				t.Fatalf("launcher returned before test cleanup: %v\n%s", err, output.String())
			}

			if !strings.Contains(output.String(), "Live log:") || !strings.Contains(output.String(), "fixture interrupted") {
				t.Fatalf("interruption lost the log: %s", output.String())
			}

			requireLiveLock(t, ctx, lock, false)
		})
	}
}

func liveLauncherCommand(t *testing.T, ctx context.Context, root, script, mode, gnu string) *exec.Cmd {
	t.Helper()

	if !strings.HasPrefix(mode, "just ") {
		return exec.CommandContext(ctx, filepath.Join(gnu, "bash"), script, "focused", "^TestSuiteSignalFixture$") //nolint:gosec // Actual launcher under inert tools and an owned fake repository.
	}

	just, err := exec.LookPath("just")
	if err != nil {
		t.Skip("the recipe signal check requires just")
	}

	args := []string{"--justfile", filepath.Join(root, "repo/justfile"), "--working-directory", filepath.Join(root, "repo")}
	if mode == "just release recipe signal" {
		args = append(args, "test-live-gitlab-release", filepath.Join(root, "job.tar"), filepath.Join(root, "signer.tar"))
	} else {
		args = append(args, "test-live", "^TestSuiteSignalFixture$")
	}

	return exec.CommandContext(ctx, just, args...) //nolint:gosec // Actual Just recipe in the inert launcher fixture.
}

func requireLiveLock(t *testing.T, ctx context.Context, path string, held bool) {
	t.Helper()

	err := exec.CommandContext(ctx, "flock", "-n", path, "true").Run() //nolint:gosec // The launcher's exact owned account lock.
	if held {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			t.Fatalf("account lock was not held: %v", err)
		}
	} else if err != nil {
		t.Fatalf("a child retained the account lock: %v", err)
	}
}

func newLauncherFixture(t *testing.T, root string) (string, string) {
	t.Helper()

	write := func(name, body string) string {
		t.Helper()

		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(path, []byte(body), 0o700); err != nil { //nolint:gosec // Executable inert collaborators in the test's private directory.
			t.Fatal(err)
		}

		return path
	}
	script := write("repo/scripts/ci/run-live-tests.sh", string(reporoot.ReadFile(t, "scripts/ci/run-live-tests.sh")))
	write("repo/justfile", string(reporoot.ReadFile(t, "justfile")))
	write("repo/scripts/ci/forgejo-binary.sh", "#!/bin/sh\nprintf '#!/bin/sh\\nexit 0\\n' > \"$3\"\n")
	write("tools/go", inertLiveGo)

	for _, name := range []string{"git", "cosign", "syft", "buildah"} {
		write("tools/"+name, "#!/bin/sh\nexit 0\n")
	}

	packageDir := filepath.Join(root, "repo/internal/livetest")
	if err := os.MkdirAll(packageDir, 0o700); err != nil {
		t.Fatal(err)
	}

	return script, packageDir
}

const inertLiveGo = `#!/bin/bash
set -eu
mode=$1
shift
case "$mode" in
  run) printf 'inert preflight\n' ;;
  list) printf '%s\n' "$FORGE_CI_LIVE_PACKAGE_DIR" ;;
  build|test)
    while [[ "$1" != -o ]]; do shift; done
    output=$2
    if [[ "$mode" = test ]]; then
      output="${output%/}/livetest.test"
      printf '#!/bin/bash\nexec "$FORGE_CI_LIVE_FIXTURE_BINARY" -test.run=^TestSuiteSignalFixture$\n' > "$output"
    else
      printf '#!/bin/sh\nexit 0\n' > "$output"
    fi
    chmod 700 "$output"
    ;;
  *) exit 90 ;;
esac
`
