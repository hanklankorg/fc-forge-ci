// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package buildah_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/evidence"
	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
)

func runBuildah(t *testing.T, global []string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "buildah", append(append([]string{}, global...), args...)...) //nolint:gosec // test-controlled args.
	out, stderr, err := buildahCommandOutput(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Logf("buildah %v stderr:\n%s", args, stderr)
	}
	return out
}

// Parsed results are stdout only. Warnings remain available to the caller for
// logging, and a failure carries both streams through bounded diagnostics.
func buildahCommandOutput(cmd *exec.Cmd) (string, string, error) {
	var stdout bytes.Buffer
	var stderr evidence.Diagnostic
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	diagnostic := stderr.Text(env)
	if err != nil {
		var output evidence.Diagnostic
		_, _ = output.Write(stdout.Bytes())
		err = fmt.Errorf("%v: %w\nstdout:\n%s\nstderr:\n%s", cmd.Args, err, output.Text(env), diagnostic)
	}
	return stdout.String(), diagnostic, err
}

func TestBuildahCommandOutputSeparatesWarningsFromParsedResults(t *testing.T) {
	t.Parallel()
	for _, stdout := range []string{"", "2023-11-14T22:13:20Z 1", "scratch-container"} {
		t.Run("stdout="+stdout, func(t *testing.T) {
			t.Parallel()
			bins := mockbinary.New(t)
			bins.Add("buildah", `printf '%s' "$1"; printf 'WARN: no subuid ranges\nWARN: no subgid ranges\n' >&2`)
			cmd := exec.CommandContext(t.Context(), bins.Path("buildah"), stdout)
			out, stderr, err := buildahCommandOutput(cmd)
			if err != nil || out != stdout || stderr != "WARN: no subuid ranges\nWARN: no subgid ranges\n" {
				t.Fatalf("stdout=%q stderr=%q err=%v", out, stderr, err)
			}
		})
	}
}

func TestBuildahCommandOutputFailureRetainsBothStreams(t *testing.T) {
	t.Parallel()
	bins := mockbinary.New(t)
	bins.Add("buildah", `printf 'partial stdout\n'; printf 'WARN: no subuid ranges\nError: storage refused\n' >&2; exit 23`)
	out, stderr, err := buildahCommandOutput(exec.CommandContext(t.Context(), bins.Path("buildah")))
	if err == nil || out != "partial stdout\n" || stderr != "WARN: no subuid ranges\nError: storage refused\n" {
		t.Fatalf("stdout=%q stderr=%q err=%v", out, stderr, err)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Fatalf("failure lost its exit status: %v", err)
	}
	for _, want := range []string{"exit status 23", "stdout:\npartial stdout", "stderr:\nWARN: no subuid ranges\nError: storage refused"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("failure lacks %q: %v", want, err)
		}
	}
}

func TestRunBuildahReturnsOnlyStdout(t *testing.T) {
	bins := mockbinary.New(t)
	bins.Add("buildah", `if [ "$1" = inspect ]; then printf '2023-11-14T22:13:20Z 1\n'; fi
printf 'WARN: no subuid ranges\nWARN: no subgid ranges\n' >&2`)
	bins.OnPath()
	if got := runBuildah(t, nil, "images", "--quiet"); got != "" {
		t.Fatalf("warnings polluted empty image listing: %q", got)
	}
	if got := runBuildah(t, nil, "inspect"); got != "2023-11-14T22:13:20Z 1\n" {
		t.Fatalf("warnings polluted image metadata: %q", got)
	}
}
