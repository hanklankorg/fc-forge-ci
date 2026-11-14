// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func githubActionFixture(t *testing.T) appcontainer.GitHubKanikoAction {
	t.Helper()
	workspace := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(workspace, "source"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "source", "original"), []byte("unchanged source"), 0o600))
	tools := t.TempDir()
	engine, scanner := filepath.Join(tools, "engine"), filepath.Join(tools, "scanner")
	require.NoError(t, os.WriteFile(engine, []byte("native engine"), 0o700))
	require.NoError(t, os.WriteFile(scanner, []byte("native scanner"), 0o700))

	return appcontainer.GitHubKanikoAction{Workspace: workspace, JobImage: "registry.invalid/engine/kaniko@sha256:" + strings.Repeat("a", 64), Engine: engine, Scanner: scanner, Plan: []byte(`{"container build":{"context":".","mode":"oci"}}`)}
}

func TestGitHubKanikoActionSeparatesSourceToolsAndOutput(t *testing.T) {
	t.Parallel()
	in := githubActionFixture(t)
	output, err := appcontainer.PrepareGitHubKanikoAction(in)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(in.Workspace, appcontainer.GitHubKanikoOutputDirectory), output)
	action := filepath.Join(in.Workspace, appcontainer.GitHubKanikoActionDirectory)
	body, err := os.ReadFile(filepath.Join(action, "action.yml"))
	require.NoError(t, err)

	var metadata struct {
		Runs struct {
			Using      string `yaml:"using"`
			Image      string `yaml:"image"`
			Entrypoint string `yaml:"entrypoint"`
		} `yaml:"runs"`
	}
	require.NoError(t, yaml.Unmarshal(body, &metadata))
	require.Equal(t, "docker", metadata.Runs.Using)
	require.Equal(t, "Dockerfile", metadata.Runs.Image)
	require.Equal(t, "/kaniko/tools/entrypoint.sh", metadata.Runs.Entrypoint)

	dockerfile, err := os.ReadFile(filepath.Join(action, "Dockerfile"))
	require.NoError(t, err)
	require.Contains(t, string(dockerfile), "FROM "+in.JobImage+"\n")
	require.NotContains(t, string(dockerfile), "ARG ")

	engine, err := os.ReadFile(filepath.Join(action, "forge-ci"))
	require.NoError(t, err)
	require.Equal(t, "native engine", string(engine))

	entries, err := os.ReadDir(filepath.Join(in.Workspace, "source"))
	require.NoError(t, err)
	require.Len(t, entries, 1, "no control payload or credential may enter the consumer context")
	require.Equal(t, "original", entries[0].Name())
}

func TestGitHubKanikoActionRefusesUnsafeOrExistingCoordinates(t *testing.T) {
	t.Parallel()

	for _, failure := range []string{"mutable image", "Dockerfile injection", "existing action", "existing output", "source symlink", "missing engine"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			in := githubActionFixture(t)

			switch failure {
			case "mutable image":
				in.JobImage = "registry.invalid/kaniko:latest"
			case "Dockerfile injection":
				in.JobImage += "\nRUN false"
			case "existing action":
				require.NoError(t, os.WriteFile(filepath.Join(in.Workspace, appcontainer.GitHubKanikoActionDirectory), []byte("user-owned"), 0o600))
			case "existing output":
				require.NoError(t, os.WriteFile(filepath.Join(in.Workspace, appcontainer.GitHubKanikoOutputDirectory), []byte("user-owned"), 0o600))
			case "source symlink":
				require.NoError(t, os.Rename(filepath.Join(in.Workspace, "source"), filepath.Join(in.Workspace, "original-source")))
				require.NoError(t, os.Symlink(in.Workspace, filepath.Join(in.Workspace, "source")))
			case "missing engine":
				in.Engine = filepath.Join(in.Workspace, "absent")
			}

			_, err := appcontainer.PrepareGitHubKanikoAction(in)
			require.Error(t, err)

			if failure == "existing action" {
				body, readErr := os.ReadFile(filepath.Join(in.Workspace, appcontainer.GitHubKanikoActionDirectory))
				require.NoError(t, readErr)
				require.Equal(t, "user-owned", string(body))
			} else {
				_, statErr := os.Lstat(filepath.Join(in.Workspace, appcontainer.GitHubKanikoActionDirectory))
				require.ErrorIs(t, statErr, os.ErrNotExist, "failed preparation must leave no partial action")
			}

			if failure == "mutable image" || failure == "Dockerfile injection" {
				require.ErrorIs(t, err, errs.ErrValidation)
			}
		})
	}
}

func TestGitHubKanikoEntrypointPreservesGatesAndExitStatus(t *testing.T) {
	t.Parallel()

	for _, operation := range []string{"image", "extract", "snapshot"} {
		for _, status := range []int{0, 7, 130} {
			t.Run(fmt.Sprintf("%s/%d", operation, status), func(t *testing.T) {
				t.Parallel()
				in := githubActionFixture(t)
				in.Snapshot = operation == "snapshot"
				stagedOutput, err := appcontainer.PrepareGitHubKanikoAction(in)
				require.NoError(t, err)
				// The fake engine was written before any test could fork.
				tools := githubActionFakeTools
				entrypoint, err := os.ReadFile(filepath.Join(in.Workspace, appcontainer.GitHubKanikoActionDirectory, "entrypoint.sh"))
				require.NoError(t, err)
				require.Contains(t, string(entrypoint), "exec forge-ci container", "the CLI must receive container signals directly")
				// Relocate only the image's tool prefix so an installed /kaniko
				// executable can never escape this inert command fixture.
				// bash reads the script and does not execute the file.
				script := filepath.Join(t.TempDir(), "entrypoint.sh")
				require.NoError(t, os.WriteFile(script, []byte(strings.ReplaceAll(string(entrypoint), "/kaniko/tools", tools)), 0o600))
				argsFile := filepath.Join(t.TempDir(), "args")
				command := exec.CommandContext(t.Context(), "bash", script, operation)

				command.Env = []string{
					"PATH=" + tools + ":" + os.Getenv("PATH"), "HOME=" + t.TempDir(),
					"GITHUB_WORKSPACE=" + in.Workspace, "GITHUB_SERVER_URL=https://github.invalid", "GITHUB_REPOSITORY=owner/source",
					"REGISTRY_PASSWORD=fixture-password", "REGISTRY_TOKEN=unrelated-registry-token", "REGISTRY_USER=fixture", "CONTAINER_REGISTRY=registry.invalid",
					"GITHUB_TOKEN=unrelated-token", "ACTIONS_RUNTIME_TOKEN=runtime-token", "ACTIONS_RESULTS_URL=https://runtime.invalid",
					"ADMIN_VULNERABILITIES=false", "IMAGE_NAME=service", "IMAGE_ARCH=amd64",
					"FIXTURE_ARGS=" + argsFile, fmt.Sprintf("FIXTURE_STATUS=%d", status),
				}
				if operation == "snapshot" {
					command.Env = append(command.Env, "FIXTURE_NO_LOGIN=true")
				}

				// An image build without the administrator's setting stops
				// before the engine builds anything; it never means false.
				if operation != "snapshot" && status == 0 {
					unset := exec.CommandContext(t.Context(), "bash", script, operation)
					unset.Env = slices.DeleteFunc(slices.Clone(command.Env), func(entry string) bool { return strings.HasPrefix(entry, "ADMIN_VULNERABILITIES=") })
					out, unsetErr := unset.CombinedOutput()
					require.Error(t, unsetErr, "%s", out)
					require.Contains(t, string(out), "enforce-vulnerabilities setting")
					require.NoFileExists(t, argsFile, "the build ran without the setting")
				}

				out, runErr := command.CombinedOutput()
				if status == 0 {
					require.NoError(t, runErr, "%s", out)
				} else {
					var exit *exec.ExitError
					require.ErrorAs(t, runErr, &exit, "%s", out)
					require.Equal(t, status, exit.ExitCode(), "%s", out)
				}

				args, readErr := os.ReadFile(argsFile)
				require.NoError(t, readErr)
				require.Contains(t, string(args), "--image-builder\nkaniko\n")
				require.FileExists(t, filepath.Join(stagedOutput, "result"), "action and host must agree on the output coordinate")

				if operation != "snapshot" {
					require.Contains(t, string(args), "--enforce-vulnerabilities=false")
				}

				info, statErr := os.Stat(filepath.Join(in.Workspace, "source", "trivy-results.sarif"))
				require.NoError(t, statErr)
				require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "permission repair belongs to the host, not a signal-handling shell wrapper")
			})
		}
	}
}

const githubActionFakeEngine = `#!/bin/bash
set -euo pipefail
if [[ "$2" == login ]]; then
  test "${FIXTURE_NO_LOGIN:-false}" != true
  password=
  IFS= read -r password || test -n "$password"
  test "$password" = fixture-password
  exit 0
fi
test -z "${REGISTRY_PASSWORD:-}${REGISTRY_TOKEN:-}${GITHUB_TOKEN:-}${ACTIONS_RUNTIME_TOKEN:-}${ACTIONS_RESULTS_URL:-}"
printf '%s\n' "$@" > "$FIXTURE_ARGS"
while (($#)); do
  case "$1" in
    --output-dir|--metadata-dir) mkdir -p "$2"; printf 'layout\n' > "$2/result"; shift ;;
  esac
  shift
done
printf 'scan evidence\n' > trivy-results.sarif
chmod 600 trivy-results.sarif
exit "$FIXTURE_STATUS"
`
