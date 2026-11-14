// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type lateStep struct {
	Name             string            `yaml:"name"`
	If               string            `yaml:"if"`
	Shell            string            `yaml:"shell"`
	Run              string            `yaml:"run"`
	Uses             string            `yaml:"uses"`
	WorkingDirectory string            `yaml:"working-directory"`
	Env              map[string]string `yaml:"env"`
}

// Run the actual first workflow step WITHOUT errexit. A failed standalone
// assertion followed by a successful command must not accidentally admit Q.
// The inert downstream counters represent checkout/fetch, validators and private
// operations; neither Git nor a key/provider process is invoked by these tests.
func TestRequiredWorkflowRequestSourceFailsBeforeAnyDownstreamEffect(t *testing.T) {
	t.Parallel()

	for file, jobs := range map[string][]string{
		"release-create-github.yml": {"sign-release-gpg", "sign-release", "create-release"},
		"release-orchestrator.yml":  {"parse-config", "validate-release-tag"},
	} {
		workflow := lateWorkflow(t, file)

		for _, job := range jobs {
			gate := workflow[job].Steps[0]
			require.Equal(t, "Require original immutable request source", gate.Name)
			require.Equal(t, "bash", gate.Shell)
			require.Empty(t, gate.If)
			require.Empty(t, gate.Uses)
			require.Len(t, gate.Env, 1)

			if file == "release-create-github.yml" {
				require.Equal(t, "${{ inputs['authorized-source-sha'] }}", gate.Env["RELEASE_REQUEST_SHA"])
			} else {
				require.Equal(t, "${{ github.sha }}", gate.Env["RELEASE_REQUEST_SHA"])
			}

			for name, source := range map[string]string{
				"sha1": strings.Repeat("a", 40), "sha256": strings.Repeat("b", 64),
				"empty": "", "whitespace": " \t\n", "short": "abcdef", "uppercase": strings.Repeat("A", 40),
				"bad-hex": strings.Repeat("g", 40), "39": strings.Repeat("a", 39), "41": strings.Repeat("a", 41),
				"63": strings.Repeat("b", 63), "65": strings.Repeat("b", 65),
				"leading-space": " " + strings.Repeat("a", 40), "trailing-newline": strings.Repeat("a", 40) + "\n",
				"literal-shell-data": "$(printf unsafe > injected)",
			} {
				t.Run(file+"/"+job+"/"+name, func(t *testing.T) {
					t.Parallel()
					dir := t.TempDir()
					effects := filepath.Join(dir, "effects")

					for _, command := range []string{"git", "forge-ci", "fetch", "checkout", "request-validator", "final-validator", "key-setup", "sign", "publish"} {
						require.NoError(t, os.WriteFile(filepath.Join(dir, command), []byte("#!/bin/bash\nprintf '%s\\n' \"${0##*/}\" >>\"$EFFECTS\"\n"), 0o700))
					}

					script := gate.Run + "\nfetch\ncheckout\nrequest-validator\nfinal-validator\nkey-setup\nsign\npublish\n"
					cmd := exec.CommandContext(t.Context(), "bash", "--noprofile", "--norc", "-u", "-o", "pipefail", "-c", script)
					cmd.Dir = dir
					// Available fallback values must not substitute for absent Q.
					cmd.Env = append(os.Environ(), "PATH="+dir, "RELEASE_REQUEST_SHA="+source, "EFFECTS="+effects,
						"GITHUB_SHA="+strings.Repeat("c", 40), "RELEASE_SHA="+strings.Repeat("d", 40), "ASSEMBLY_REQUEST_SHA="+strings.Repeat("e", 40))

					body, err := cmd.CombinedOutput()
					if name == "sha1" || name == "sha256" {
						require.NoError(t, err, string(body))
						counts, err := os.ReadFile(effects)
						require.NoError(t, err)
						require.Equal(t, "fetch\ncheckout\nrequest-validator\nfinal-validator\nkey-setup\nsign\npublish\n", string(counts))
					} else {
						require.Error(t, err, string(body))
						require.NoFileExists(t, effects, "invalid required Q must exit before any downstream operation")
					}

					require.NoFileExists(t, filepath.Join(dir, "injected"), "Q is data, never evaluated")
				})
			}
		}
	}
}

func lateWorkflow(t *testing.T, name string) map[string]struct {
	Steps []lateStep        `yaml:"steps"`
	With  map[string]string `yaml:"with"`
} {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(reporoot.Path(t), ".github", "workflows", name))
	require.NoError(t, err)

	var workflow struct {
		Jobs map[string]struct {
			Steps []lateStep        `yaml:"steps"`
			With  map[string]string `yaml:"with"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(body, &workflow))

	return workflow.Jobs
}

func TestNativePrivateBoundariesRequireOriginalRequest(t *testing.T) {
	t.Parallel()
	orchestrator := lateWorkflow(t, "release-orchestrator.yml")
	with := orchestrator["create-release"].With
	require.Equal(t, "${{ github.sha }}", with["authorized-source-sha"])
	require.Contains(t, with["request-is-release"], "!fromJson(")
	require.Contains(t, with["request-is-release"], "targets.version_bump.runs")
	require.Contains(t, with["require-allowlisted-signer"], "policy.require_allowlisted_signer")

	var upstream bool

	for _, step := range orchestrator["validate-release-tag"].Steps {
		if strings.Contains(step.Run, "forge-ci release validate-tag") {
			upstream = true

			require.Equal(t, "${{ github.sha }}", step.Env["RELEASE_REQUEST_SHA"])
			require.Contains(t, step.Env["RELEASE_REQUEST_IS_RELEASE"], "targets.version_bump.runs")
		}
	}

	require.True(t, upstream)

	jobs := lateWorkflow(t, "release-create-github.yml")
	// The host jobs run the canonical CLI consumed before any consumer
	// metadata; the container signer runs the CLI baked into the admitted
	// image and consumes nothing.
	for _, job := range []string{"sign-release-gpg", "sign-release", "create-release"} {
		steps := jobs[job].Steps
		host := job != "sign-release"
		consumed, boundary := false, -1

		for index, step := range steps {
			require.NotContains(t, step.Run, "install-forge-ci.sh", "no host installer on the release route")

			if step.Uses == "./_forge_ci_bootstrap/.github/actions/consume-canonical-cli" {
				require.True(t, host, "a job container runs its baked CLI; it does not consume the canonical one")

				consumed = true
			}

			if strings.Contains(step.Run, "forge-ci platform checkout") {
				require.Equal(t, host, consumed, "a host job bootstraps before consumer metadata")
				require.Contains(t, step.Run, "--sparse .forge-ci --fetch-all-refs")
				require.Equal(t, "${{ github.token }}", step.Env["GITHUB_TOKEN"])
			}

			if strings.Contains(step.Run, "forge-ci release validate-tag") {
				boundary = index

				require.Equal(t, "${{ inputs['authorized-source-sha'] }}", step.Env["RELEASE_REQUEST_SHA"])
				require.Equal(t, "${{ inputs['checkout-ref'] }}", step.Env["RELEASE_SHA"])
				require.Equal(t, "${{ inputs['request-is-release'] }}", step.Env["RELEASE_REQUEST_IS_RELEASE"])
				require.Equal(t, "${{ inputs['require-allowlisted-signer'] }}", step.Env["REQUIRE_ALLOWLISTED_SIGNER"])
				require.NotContains(t, step.Env, "REQUIRE_VALID_SIGNATURE", "validity is required of the final tag only")
				require.Contains(t, step.Env["ALLOWED_SIGNERS_FILE"], "/release-request-metadata/.forge-ci/")
				require.Contains(t, step.Env["ALLOWED_GPG_KEYS_FILE"], "/release-request-metadata/.forge-ci/")
				require.Equal(t, "${{ runner.temp }}/release-source-metadata", step.WorkingDirectory)
				require.Contains(t, step.Run, `forge-ci validate tag signature --tag "release-request/$TAG_NAME"`)
				require.Contains(t, step.Run, `forge-ci release validate-tag --expected-parent "$RELEASE_REQUEST_SHA"`)
				require.True(t, strings.HasSuffix(strings.TrimSpace(step.Run), `forge-ci release validate-tag --expected-parent "$RELEASE_REQUEST_SHA"`))

				for key := range step.Env {
					require.NotContains(t, key, "PRIVATE")
					require.NotContains(t, key, "PASSWORD")
					require.NotEqual(t, "GH_TOKEN", key)
				}
			}

			if step.Name == "Import GPG key" || step.Name == "Bind Cosign signing key to trusted public key" || step.Name == "Create GitHub Release" {
				require.Equal(t, index-1, boundary, "late authority check must immediately precede the first private operation")
			}

			if strings.Contains(step.Run, "forge-ci release sign") || strings.Contains(step.Run, "forge-ci validate signing-key") {
				require.GreaterOrEqual(t, boundary, 0)
				require.Less(t, boundary, index)
			}
		}

		require.GreaterOrEqual(t, boundary, 0, job)
	}
}

// Execute the actual workflow boundary script with an inert command port. The
// remote state is changed AFTER a successful prerequisite observation. These
// cases prove step ordering/fail-fast behavior, not cryptographic verification
// (the native signature and Git validators have their own app/adapter tests).
func TestLateBoundaryRefusalsHaveNoPrivateEffects(t *testing.T) {
	t.Parallel()
	jobs := lateWorkflow(t, "release-create-github.yml")

	for _, job := range []string{"sign-release-gpg", "sign-release", "create-release"} {
		var script string

		for _, step := range jobs[job].Steps {
			if strings.Contains(step.Run, "forge-ci release validate-tag") {
				script = step.Run
			}
		}

		require.NotEmpty(t, script)

		for _, state := range []string{"valid-child", "exact-P-retry", "no-bump", "request-moved", "request-deleted", "annotation-replaced", "unsigned-same-Q", "final-moved", "final-superseded"} {
			t.Run(job+"/"+state, func(t *testing.T) {
				t.Parallel()
				dir := t.TempDir()
				port := `#!/bin/bash
set -euo pipefail
printf '%s\n' "$*" >>"$CALLS"
case "$*" in
  'validate tag signature --tag release-request/v1.2.3')
    test "$REMOTE_STATE" != unsigned-same-Q ;;
  'validate tag signature --tag v1.2.3 --require-valid-signature --require-allowlisted-signer=false --allowed-gpg-keys-file /dev/null')
    : ;;
  'release validate-tag --expected-parent original-Q')
    case "$REMOTE_STATE" in
      valid-child|exact-P-retry|no-bump) : ;;
      *) exit 1 ;;
    esac ;;
  *) exit 99 ;;
esac
`
				require.NoError(t, os.WriteFile(filepath.Join(dir, "forge-ci"), []byte(port), 0o700))
				// Only a fully successful check reaches these inert effect counters.
				cmd := exec.CommandContext(t.Context(), "bash", "-c", script+"\nprintf 'key-setup sign publish\\n' >\"$EFFECTS\"\n")

				cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "REMOTE_STATE="+state,
					"TAG_NAME=v1.2.3", "RELEASE_REQUEST_SHA=original-Q", "CALLS="+filepath.Join(dir, "calls"), "EFFECTS="+filepath.Join(dir, "effects"))

				body, err := cmd.CombinedOutput()
				if state == "valid-child" || state == "exact-P-retry" || state == "no-bump" {
					require.NoError(t, err, string(body))
					require.FileExists(t, filepath.Join(dir, "effects"))
				} else {
					require.Error(t, err, string(body))
					require.NoFileExists(t, filepath.Join(dir, "effects"))
				}
			})
		}
	}
}
