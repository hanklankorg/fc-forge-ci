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

	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Execute the actual signed-build workflow step with an inert signer. Packaging
// is intentionally unsigned: only this explicit boundary can produce the bundle
// required by the rolling publisher. No real OIDC, signing key or provider runs.
func TestBuildCLIWorkflowSignsBeforeHandoff(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../../.github/workflows/build-cli.yml")
	require.NoError(t, err)

	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
				If   string            `yaml:"if"`
				Run  string            `yaml:"run"`
				With map[string]string `yaml:"with"`
				Env  map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(body, &workflow))

	packaged, signed, handedOff := false, false, false

	var signingCommand string

	for _, step := range workflow.Jobs["build"].Steps {
		switch step.Name {
		case "Run goreleaser":
			require.Equal(t, "release --clean --skip=${{ inputs.sign && 'publish,validate,sign' || 'publish,validate,sign,sbom' }}", step.With["args"])

			packaged = true
		case "Verify payload and sign checksum manifest":
			require.True(t, packaged)
			require.Equal(t, "${{ inputs.sign }}", step.If, "PR builds must not attempt OIDC signing")
			require.Equal(t, "${{ inputs.version-tag }}", step.Env["GORELEASER_CURRENT_TAG"])

			signingCommand, signed = step.Run, true
		case "Generate + sign SLSA provenance (cosign sign-blob, keyless)":
			require.True(t, signed, "checksum bundle must precede provenance and the handoff")
		case "Upload dist artifact":
			require.True(t, signed, "uploading unsigned packaging output loses checksums.txt.bundle")

			handedOff = true
		}
	}

	require.True(t, packaged && signed && handedOff)

	script, err := os.ReadFile("sign-reproducible-release.sh")
	require.NoError(t, err)

	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprintf("changed=%t", changed), func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()

			for _, dir := range []string{"bin", "dist", "scripts/ci"} {
				require.NoError(t, os.MkdirAll(filepath.Join(root, dir), 0o700))
			}

			require.NoError(t, os.WriteFile(filepath.Join(root, "scripts/ci/sign-reproducible-release.sh"), script, 0o600))

			archive := "forge-ci_0.90.0-pre_linux_amd64.tar.gz"
			payload := []byte("fixture archive\n")
			manifest := fmt.Sprintf("%x  %s\n", sha256.Sum256(payload), archive)

			if changed {
				payload = []byte("tampered archive\n")
			}

			require.NoError(t, os.WriteFile(filepath.Join(root, "dist", archive), payload, 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(root, "dist/checksums.txt"), []byte(manifest), 0o600))

			releaseOwnerMetadata(t, filepath.Join(root, "dist"))
			releaseOwnerStub(t, filepath.Join(root, "bin"))

			cmd := exec.CommandContext(t.Context(), testBash(t), "-c", signingCommand)
			cmd.Dir = root
			cmd.Env = []string{
				"PATH=" + filepath.Join(root, "bin") + string(os.PathListSeparator) + testenv.GNUPath(t, "sha256sum") + string(os.PathListSeparator) + os.Getenv("PATH"),
				"GORELEASER_CURRENT_TAG=v0.90.0-pre", "LC_ALL=C",
				"RELEASE_OWNER_BINARY=" + os.Args[0],
			}
			output, runErr := cmd.CombinedOutput()
			bundle := filepath.Join(root, "dist/checksums.txt.bundle")

			if changed {
				require.Error(t, runErr, "%s", output)
				require.NoFileExists(t, bundle, "a changed archive must not reach the signer")
			} else {
				require.NoError(t, runErr, "%s", output)
				require.FileExists(t, bundle, strings.TrimSpace(string(output)))
			}
		})
	}
}
