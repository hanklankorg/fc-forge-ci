// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package ci

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

const runtimeExtractDouble = `#!/bin/sh
set -eu
case "$1 $2" in
"container runtime-publication")
  if [ "$3" = plan ]; then
    jq '{target:"go",name:"forge-ci-runtime-go-1.27",archs:["amd64"],repository:(.ref|split("@")[0]),tag:"staging-123-1-aaaaaaaaaaaa",candidates:[{arch:"amd64",ref:.ref}]}' runtime-go-arch-amd64/runtime-go-amd64.json
    exit 0
  fi
  if [ "$3" = verify-cli ]; then
    test "$4 $5 $6" = '--arch amd64 --image-ref'
    test "$8" = --auth-file
    test "$9" = "$REGISTRY_AUTH_FILE"
    if [ "${CLI_REFUSAL:-}" = true ]; then
      echo 'CLI bytes do not match source checksum' >&2
      exit 78
    fi
    exit 0
  fi
  test "$3" = verify-smoke
  test "$4" = --receipt
  test "$6" = --source-sha
  test "$8" = --target
  test "${10}" = --arch
  test "${12}" = --image-ref
  jq -e --arg source "$7" --arg target "$9" --arg arch "${11}" --arg ref "${13}" \
    '.source == $source and .target == $target and .arch == $arch and .ref == $ref' "$5" >/dev/null || {
    echo 'does not match its successful smoke receipt' >&2
    exit 1
  }
  ;;
"artifact download")
  if [ "$4" = runtime-smoke-go-amd64 ]; then
    mkdir -p "$6"
    jq --arg source "$SOURCE_SHA" --arg ref "${SMOKE_REF_OVERRIDE:-}" \
      '{source:$source,target:"go",arch:"amd64",ref:(if $ref == "" then .ref else $ref end)}' \
      runtime-go-arch-amd64/runtime-go-amd64.json >"$6/receipt.json"
  fi
  exit 0
  ;;
*) exit 99 ;;
esac
`

func TestRuntimePlanDelegatesArgumentsAndExitStatus(t *testing.T) {
	t.Parallel()

	root := reporoot.Path(t)
	sha, err := exec.CommandContext(t.Context(), "git", "-C", root, "rev-parse", "HEAD").Output()
	require.NoError(t, err)

	for _, mode := range []string{"plan", "collect"} {
		for _, status := range []int{0, 69} {
			t.Run(mode+"/"+strconv.Itoa(status), func(t *testing.T) {
				t.Parallel()

				bin := t.TempDir()
				record := filepath.Join(t.TempDir(), "arguments")
				require.NoError(t, os.WriteFile(filepath.Join(bin, "forge-ci"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >\"$ARGUMENT_RECORD\"\nexit \"$CLI_STATUS\"\n"), 0o700))

				cmd := exec.CommandContext(t.Context(), "bash", filepath.Join(root, "scripts/ci/runtime-images.sh"), mode)
				cmd.Dir = root

				const platforms = `[{"platform":"linux/amd64","suffix":"amd64"}]`

				cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "ARGUMENT_RECORD="+record, fmt.Sprintf("CLI_STATUS=%d", status), "RUNTIME_IMAGE_NAMESPACE=registry.example/acme", "SOURCE_SHA="+strings.TrimSpace(string(sha)), "RUNTIME_RUN_ID=123-1", "PUBLICATION_REF=refs/heads/main", "PLATFORM_MATRIX="+platforms)

				body, runErr := cmd.CombinedOutput()
				if status == 0 {
					require.NoError(t, runErr, "%s", body)
				} else {
					var exit *exec.ExitError
					require.ErrorAs(t, runErr, &exit)
					require.Equal(t, status, exit.ExitCode())
				}

				data, readErr := os.ReadFile(record)
				require.NoError(t, readErr)
				require.Equal(t, []string{
					"container", "runtime-publication", mode, "--roster", "containers/runtime/images.json",
					"--platform-matrix", platforms, "--image-namespace", "registry.example/acme",
					"--source-sha", strings.TrimSpace(string(sha)), "--checked-out-sha", strings.TrimSpace(string(sha)),
					"--run-id", "123-1", "--publication-ref", "refs/heads/main",
				}, strings.Split(strings.TrimSpace(string(data)), "\n"))
			})
		}
	}
}

func TestRuntimeBuildAndAssemblyStopAtGoProjectionRefusal(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"build", "assemble"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			for name, body := range map[string]string{
				"git": "#!/bin/sh\nprintf '%s\\n' \"$SOURCE_SHA\"\n",
				"forge-ci": `#!/bin/sh
set -eu
test "$1 $2 $3" = 'container runtime-publication plan' || { touch "$UNEXPECTED_EFFECT"; exit 99; }
printf '%s\n' "$@" >"$ARGUMENT_RECORD"
exit 78
`,
			} {
				require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(body), 0o700))
			}

			record, effect := filepath.Join(root, "arguments"), filepath.Join(root, "unexpected-effect")
			cmd := exec.CommandContext(t.Context(), "bash", filepath.Join(reporoot.Path(t), "scripts/ci/runtime-images.sh"), mode, "go", "amd64")
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PATH="+root+":"+os.Getenv("PATH"), "ARGUMENT_RECORD="+record, "UNEXPECTED_EFFECT="+effect,
				"RUNTIME_IMAGE_NAMESPACE=registry.example/acme", "SOURCE_SHA="+strings.Repeat("a", 40), "RUNTIME_RUN_ID=123-1", "PUBLICATION_REF=refs/heads/main",
				`PLATFORM_MATRIX=[{"platform":"linux/arm64","suffix":"amd64"}]`)
			body, err := cmd.CombinedOutput()
			require.Error(t, err, "%s", body)
			require.Equal(t, 78, cmd.ProcessState.ExitCode())
			require.NoFileExists(t, effect)

			forwarded, err := os.ReadFile(record)
			require.NoError(t, err)
			require.Contains(t, string(forwarded), "--target\ngo\n")
			require.Contains(t, string(forwarded), "--platform-matrix\n[{\"platform\":\"linux/arm64\",\"suffix\":\"amd64\"}]\n")
		})
	}
}

func TestRuntimeStageCLIVerifiesBeforeStaging(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "runtime-cli"), 0o750))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "tools"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, "tools/forge-ci"), []byte(`#!/bin/sh
test "$*" = 'container runtime-publication verify-cli --arch amd64 --file runtime-cli/forge-ci-amd64' || exit 99
exit "$CLI_STATUS"
`), 0o700))

	content := []byte("recorded engine bytes")
	artifact := filepath.Join(root, "runtime-cli/forge-ci-amd64")
	require.NoError(t, os.WriteFile(artifact, content, 0o600))

	run := func(status string) ([]byte, error) {
		cmd := exec.CommandContext(t.Context(), "bash", filepath.Join(reporoot.Path(t), "scripts/ci/runtime-images.sh"), "stage-cli", "amd64")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "tools")+":"+os.Getenv("PATH"), "CLI_STATUS="+status)

		return cmd.CombinedOutput()
	}
	body, err := run("78")
	require.Error(t, err, "%s", body)
	require.NoFileExists(t, filepath.Join(root, "cli-prebuilt/forge-ci"))

	body, err = run("0")
	require.NoError(t, err, "%s", body)
	got, err := os.ReadFile(filepath.Join(root, "cli-prebuilt/forge-ci"))
	require.NoError(t, err)
	require.Equal(t, content, got)
}

func TestRuntimeScannerAuthIsPrivateAndRemovedOnFailure(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for _, dir := range []string{"containers/runtime", "runtime-go-arch-amd64", "tools"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, dir), 0o750))
	}

	require.NoError(t, os.WriteFile(filepath.Join(root, "containers/runtime/images.json"), []byte(`[{"target":"go","name":"forge-ci-runtime-go-1.27","archs":["amd64"]}]`), 0o600))

	repo := "registry.example/acme/forge-ci-runtime-go-1.27"
	data, err := json.Marshal(map[string]string{"arch": "amd64", "tag": repo + ":staging-123-1-aaaaaaaaaaaa-amd64", "digest": "sha256:" + strings.Repeat("b", 64), "ref": repo + "@sha256:" + strings.Repeat("b", 64)})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "runtime-go-arch-amd64/runtime-go-amd64.json"), data, 0o600))
	auth := filepath.Join(root, "registry-auth.json")
	require.NoError(t, os.WriteFile(auth, []byte(`{"auths":{}}`), 0o600))

	for name, body := range map[string]string{
		"forge-ci": runtimeExtractDouble,
		"git":      "#!/bin/sh\nprintf '%s\\n' \"$SOURCE_SHA\"\n",
		"syft": `#!/bin/sh
set -eu
mode() { stat -c '%a' "$1" 2>/dev/null || stat -f '%Lp' "$1"; }
test "$(mode "$DOCKER_CONFIG")" = 700
test "$(mode "$DOCKER_CONFIG/config.json")" = 600
cmp "$REGISTRY_AUTH_FILE" "$DOCKER_CONFIG/config.json"
printf '%s' "$DOCKER_CONFIG" > "$AUTH_DIR_RECORD"
exit 42
`,
	} {
		path := filepath.Join(root, "tools", name)
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
		require.NoError(t, os.Chmod(path, 0o700))
	}

	record := filepath.Join(root, "auth-dir")
	cmd := exec.CommandContext(t.Context(), "bash", filepath.Join(reporoot.Path(t), "scripts/ci/runtime-images.sh"), "assemble", "go")
	cmd.Dir = root

	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "tools")+":"+os.Getenv("PATH"), "RUNTIME_IMAGE_NAMESPACE=registry.example/acme", "SOURCE_SHA="+strings.Repeat("a", 40), "RUNTIME_RUN_ID=123-1", `PLATFORM_MATRIX=[{"platform":"linux/amd64","suffix":"amd64"}]`, "PUBLICATION_REF=refs/heads/main", "RUNNER_TEMP="+root, "REGISTRY_AUTH_FILE="+auth, "AUTH_DIR_RECORD="+record)
	body, err := cmd.CombinedOutput()
	require.Error(t, err, "%s", body)
	require.Equal(t, 42, cmd.ProcessState.ExitCode())

	privateDir, err := os.ReadFile(record)
	require.NoError(t, err)
	require.NoDirExists(t, string(privateDir))
	require.NoFileExists(t, filepath.Join(root, "evidence/config.json"))
	require.FileExists(t, auth, "the login owner, not the scanner bridge, owns the original auth file")

	// A substituted binary must stop assembly before even starting the scanner.
	require.NoError(t, os.Remove(record))
	retry := exec.CommandContext(t.Context(), "bash", filepath.Join(reporoot.Path(t), "scripts/ci/runtime-images.sh"), "assemble", "go")
	retry.Dir = root
	retry.Env = cmd.Env
	retry.Env = append(retry.Env, "CLI_REFUSAL=true")
	body, err = retry.CombinedOutput()
	require.Error(t, err, "%s", body)
	require.Contains(t, string(body), "CLI bytes do not match source checksum")
	require.NoFileExists(t, record, "scanner must not run after the byte-identity refusal")

	// Rebuilt bytes with a green smoke job for the old digest must not reach
	// scanning or index publication, even though the dependency stayed green.
	stale := exec.CommandContext(t.Context(), "bash", filepath.Join(reporoot.Path(t), "scripts/ci/runtime-images.sh"), "assemble", "go")
	stale.Dir = root
	stale.Env = append(stale.Env, cmd.Env...)
	stale.Env = append(stale.Env, "SMOKE_REF_OVERRIDE="+repo+"@sha256:"+strings.Repeat("c", 64))
	body, err = stale.CombinedOutput()
	require.Error(t, err, "%s", body)
	require.Contains(t, string(body), "does not match its successful smoke receipt")
	require.NoFileExists(t, record, "scanner must not run for candidates without matching smoke evidence")

	leftovers, err := filepath.Glob(filepath.Join(root, "runtime-registry.*"))
	require.NoError(t, err)
	require.Empty(t, leftovers)
}

// The assembly scans with the caller's enforcement setting. A refused scan is
// still run for every architecture, its reports are uploaded, and no index is
// assembled.
func TestRuntimeAssemblyStopsOnARefusedScanAfterKeepingItsReports(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for _, dir := range []string{"containers/runtime", "runtime-go-arch-amd64", "runtime-go-arch-arm64", "scripts/ci", "tools"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, dir), 0o750))
	}

	repo := "registry.example/acme/forge-ci-runtime-go-1.27"
	refs := map[string]string{"amd64": repo + "@sha256:" + strings.Repeat("b", 64), "arm64": repo + "@sha256:" + strings.Repeat("c", 64)}
	auth := filepath.Join(root, "registry-auth.json")
	require.NoError(t, os.WriteFile(auth, []byte(`{"auths":{}}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "scripts/ci/runtime-evidence.sh"), reporoot.ReadFile(t, "scripts/ci/runtime-evidence.sh"), 0o600))

	for name, body := range map[string]string{
		"syft":  "#!/bin/sh\nexit 0\n",
		"trivy": "#!/bin/sh\necho 'a scanner call outside the engine policy' >&2\nexit 97\n",
		"git":   "#!/bin/sh\nprintf '%s\\n' \"$SOURCE_SHA\"\n",
		"forge-ci": `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$CALLS"
case "$1 $2" in
"container runtime-publication")
  test "$3" != plan || printf '%s\n' "$PLAN"
  ;;
"security scan")
  test "$4" = --enforce-vulnerabilities=true
  : > "$8"
  exit 7
  ;;
"artifact download")
  test "$4" != runtime-smoke-go-amd64 && test "$4" != runtime-smoke-go-arm64 || { mkdir -p "$6" && : > "$6/receipt.json"; }
  ;;
"artifact upload") ;;
*) exit 99 ;;
esac
`,
	} {
		path := filepath.Join(root, "tools", name)
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
		require.NoError(t, os.Chmod(path, 0o700))
	}

	plan := fmt.Sprintf(`{"repository":%q,"tag":"staging","archs":["amd64","arm64"],"candidates":[{"arch":"amd64","ref":%q},{"arch":"arm64","ref":%q}]}`, repo, refs["amd64"], refs["arm64"])
	calls := filepath.Join(root, "calls")
	cmd := exec.CommandContext(t.Context(), "bash", filepath.Join(reporoot.Path(t), "scripts/ci/runtime-images.sh"), "assemble", "go")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "tools")+":"+os.Getenv("PATH"), "RUNTIME_IMAGE_NAMESPACE=registry.example/acme", "SOURCE_SHA="+strings.Repeat("a", 40), "RUNTIME_RUN_ID=123-1", `PLATFORM_MATRIX=[]`, "PUBLICATION_REF=refs/heads/main", "RUNNER_TEMP="+root, "REGISTRY_AUTH_FILE="+auth, "PLAN="+plan, "CALLS="+calls, "RUNTIME_IMAGES_ADMIN_VULNERABILITIES=true")
	body, err := cmd.CombinedOutput()
	require.Error(t, err, "%s", body)
	require.Equal(t, 7, cmd.ProcessState.ExitCode(), "%s", body)

	recorded, err := os.ReadFile(calls)
	require.NoError(t, err)

	for _, ref := range refs {
		require.Contains(t, string(recorded), "security scan container --enforce-vulnerabilities=true --image-ref "+ref+" ")
	}

	require.Contains(t, string(recorded), "artifact upload --name runtime-scan-go --dir evidence/")
	require.NotContains(t, string(recorded), "multiarch-image assemble")
	require.FileExists(t, filepath.Join(root, "evidence/trivy-arm64.json"))
}

func TestRuntimeBakedCLIDelegatesToGoVerification(t *testing.T) {
	t.Parallel()

	for _, refused := range []bool{false, true} {
		t.Run(strconv.FormatBool(refused), func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(root, "forge-ci"), []byte(runtimeExtractDouble), 0o700))

			cmd := exec.CommandContext(t.Context(), "bash", filepath.Join(reporoot.Path(t), "scripts/ci/runtime-images.sh"), "verify-cli", "amd64", "registry.example/acme/runtime@sha256:"+strings.Repeat("b", 64))
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PATH="+root+":"+os.Getenv("PATH"), "REGISTRY_AUTH_FILE="+filepath.Join(root, "unused-auth"), fmt.Sprintf("CLI_REFUSAL=%t", refused))

			body, err := cmd.CombinedOutput()
			if refused {
				require.Error(t, err, "%s", body)
				require.Contains(t, string(body), "CLI bytes do not match")
			} else {
				require.NoError(t, err, "%s", body)
			}
		})
	}
}
