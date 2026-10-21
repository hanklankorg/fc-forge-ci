// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package ci

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// The signer is an inert stub. No product entry point, OIDC or network runs.
func TestReleaseSigningRequiresMatchingRebuild(t *testing.T) {
	script, err := filepath.Abs("sign-reproducible-release.sh")
	require.NoError(t, err)
	for _, scenario := range []string{"equal", "with-sbom", "unlisted-sbom", "different", "missing", "no-reference", "pre-release-no-reference", "same-directory", "bad-checksum", "missing-checksum", "incomplete-checksum", "duplicate-checksum", "malformed-checksum"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			first, second := filepath.Join(root, "reference"), filepath.Join(root, "release")
			bin := filepath.Join(root, "bin")
			for _, dir := range []string{first, second, bin} {
				require.NoError(t, os.Mkdir(dir, 0o700))
			}
			var manifest strings.Builder
			for _, target := range []string{"linux_amd64", "linux_arm64", "darwin_amd64", "darwin_arm64"} {
				name := "forge-ci_1.2.3_" + target + ".tar.gz"
				payload := []byte("fixture " + target)
				require.NoError(t, os.WriteFile(filepath.Join(first, name), payload, 0o600))
				if target == "linux_amd64" && scenario == "missing" {
					continue
				}
				if target == "linux_amd64" && scenario == "different" {
					payload = []byte("changed payload")
				}
				require.NoError(t, os.WriteFile(filepath.Join(second, name), payload, 0o600))
				if scenario == "incomplete-checksum" && target != "linux_amd64" {
					continue
				}
				if scenario == "duplicate-checksum" && target == "linux_amd64" {
					fmt.Fprintf(&manifest, "%x *%s\n", sha256.Sum256(payload), name)
				}
				if scenario == "malformed-checksum" && target == "linux_amd64" {
					fmt.Fprintf(&manifest, "invalid  %s\n", name)
					continue
				}
				if scenario == "bad-checksum" && target == "linux_amd64" {
					fmt.Fprintf(&manifest, "%s  %s\n", strings.Repeat("0", 64), name)
					continue
				}
				fmt.Fprintf(&manifest, "%x  %s\n", sha256.Sum256(payload), name)
			}
			if scenario == "with-sbom" || scenario == "unlisted-sbom" {
				const name = "forge-ci_1.2.3_linux_amd64.tar.gz.cdx.sbom.json"
				body := []byte("fixture sbom\n")
				require.NoError(t, os.WriteFile(filepath.Join(second, name), body, 0o600))
				if scenario == "with-sbom" {
					fmt.Fprintf(&manifest, "%x  %s\n", sha256.Sum256(body), name)
				}
			}
			if scenario != "missing-checksum" {
				require.NoError(t, os.WriteFile(filepath.Join(second, "checksums.txt"), []byte(manifest.String()), 0o600))
			}
			require.NoError(t, os.WriteFile(filepath.Join(bin, "forge-ci"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >\"$SIGN_CALL\"\n"), 0o700)) //nolint:gosec // isolated executable fixture
			call := filepath.Join(root, "sign-called")
			cmd := exec.CommandContext(t.Context(), "bash", script, "release", "sign", "--method=sigstore", "--no-checksums-file", "--no-release-artifacts-dir", "--file", filepath.Join(second, "checksums.txt"))
			cmd.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "SIGN_CALL=" + call, "LC_ALL=C"}
			if scenario == "same-directory" {
				first = second
			}
			if scenario != "no-reference" && scenario != "pre-release-no-reference" {
				cmd.Env = append(cmd.Env, "FORGE_CI_RELEASE_REFERENCE="+first)
			}
			if scenario == "pre-release-no-reference" {
				cmd.Env = append(cmd.Env, "GORELEASER_CURRENT_TAG=v0.90.0-pre")
			}
			output, runErr := cmd.CombinedOutput()
			if scenario == "equal" || scenario == "with-sbom" || scenario == "pre-release-no-reference" {
				require.NoError(t, runErr, "%s", output)
				args, readErr := os.ReadFile(call)
				require.NoError(t, readErr)
				require.Equal(t, "release\nsign\n--method=sigstore\n--no-checksums-file\n--no-release-artifacts-dir\n--file\n"+filepath.Join(second, "checksums.txt")+"\n", string(args))
			} else {
				require.Error(t, runErr, "%s", output)
				require.NoFileExists(t, call, "failed verification must never call the signer")
			}
		})
	}
}

func TestReleaseWorkflowPreparesReferenceBeforeSigning(t *testing.T) {
	body, err := os.ReadFile("../../.github/workflows/release-binary.yml")
	require.NoError(t, err)
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
				Run  string            `yaml:"run"`
				With map[string]string `yaml:"with"`
				Env  map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(body, &workflow))
	prepared, signed, provenance := false, false, false
	for _, step := range workflow.Jobs["goreleaser"].Steps {
		if strings.Contains(step.Run, "verify-reproducible.sh --release-reference") {
			require.False(t, signed)
			require.Contains(t, step.Run, `"$RELEASE_TAG" "$RUNNER_TEMP/forge-ci-release-reference"`)
			prepared = true
		}
		if step.Name == "Run goreleaser" {
			require.True(t, prepared, "a final-tag rebuild must precede publication")
			require.Equal(t, "release --clean", step.With["args"], "signing must not be skipped")
			require.Equal(t, "${{ runner.temp }}/forge-ci-release-reference", step.Env["FORGE_CI_RELEASE_REFERENCE"])
			require.Equal(t, "${{ runner.temp }}/forge-ci-release-build-cache", step.Env["GOCACHE"])
			signed = true
		}
		if strings.Contains(step.Run, "release provenance") {
			require.True(t, signed)
			require.Equal(t, "${{ steps.release-context.outputs.release-tag }}", step.Env["RELEASE_TAG"])
			require.Contains(t, step.Run, `source_sha="$(git rev-parse HEAD)"`)
			require.Contains(t, step.Run, `--ref-name "$RELEASE_TAG" --source-sha "$source_sha"`)
			provenance = true
		}
	}
	require.True(t, prepared && signed && provenance)
}

// Exercise the checkout/cache/tag plumbing with inert packager output. Git
// history belongs only to the disposable fixture; the project is never changed.
func TestFinalTagReferenceBuild(t *testing.T) {
	script, err := filepath.Abs("verify-reproducible.sh")
	require.NoError(t, err)
	root := t.TempDir()
	source, bin := filepath.Join(root, "source"), filepath.Join(root, "bin")
	require.NoError(t, os.Mkdir(source, 0o700))
	require.NoError(t, os.Mkdir(bin, 0o700))
	env := []string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + root, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"SOURCE_CHECKOUT=" + source, "TMPDIR=" + root,
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir, cmd.Env = source, env
		out, runErr := cmd.CombinedOutput()
		require.NoError(t, runErr, "%s", out)
	}
	git("init", "--quiet")
	require.NoError(t, os.WriteFile(filepath.Join(source, "fixture.txt"), []byte("source\n"), 0o600))
	git("add", "fixture.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "fixture")
	git("-c", "tag.gpgsign=false", "tag", "v1.2.3")
	packager := `#!/bin/bash
set -euo pipefail
[[ $1 == --version ]] && exit 0
[[ "$*" == 'release --clean --skip=publish,sign,sbom,announce --parallelism=2' ]]
[[ $GORELEASER_CURRENT_TAG == v1.2.3 && $GOPROXY == off && $GOSUMDB == off ]]
[[ $PWD != "$SOURCE_CHECKOUT" && $GOCACHE == */reference-cache && ! -e $GOCACHE ]]
[[ $(git rev-parse HEAD) == $(git rev-parse 'refs/tags/v1.2.3^{commit}') ]]
mkdir dist
for target in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64; do
  printf 'fixture\n' >"dist/forge-ci_1.2.3_${target}.tar.gz"
done
`
	require.NoError(t, os.WriteFile(filepath.Join(bin, "goreleaser"), []byte(packager), 0o700))                    //nolint:gosec // isolated executable fixture
	require.NoError(t, os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\n[ \"$1\" = version ]\n"), 0o700)) //nolint:gosec // isolated executable fixture
	for _, tag := range []string{"v1.2.3", "v9.9.9"} {
		output := filepath.Join(root, "output-"+tag)
		cmd := exec.CommandContext(t.Context(), "bash", script, "--release-reference", tag, output)
		cmd.Dir, cmd.Env = source, env
		out, runErr := cmd.CombinedOutput()
		if tag == "v1.2.3" {
			require.NoError(t, runErr, "%s", out)
			files, readErr := os.ReadDir(output)
			require.NoError(t, readErr)
			require.Len(t, files, 4)
		} else {
			require.Error(t, runErr, "%s", out)
			require.NoDirExists(t, output)
		}
	}
}
