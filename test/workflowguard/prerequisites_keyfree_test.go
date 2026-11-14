// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

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

type keyfreePrerequisiteStep struct {
	ID   string            `yaml:"id"`
	If   string            `yaml:"if"`
	Env  map[string]string `yaml:"env"`
	With map[string]string `yaml:"with"`
	Run  string            `yaml:"run"`
}

func keyfreePrerequisiteSteps(t *testing.T) []keyfreePrerequisiteStep {
	t.Helper()

	var workflow struct {
		Env  map[string]string `yaml:"env"`
		Jobs map[string]struct {
			Env   map[string]string         `yaml:"env"`
			Steps []keyfreePrerequisiteStep `yaml:"steps"`
		} `yaml:"jobs"`
	}

	require.NoError(t, yaml.Unmarshal(reporoot.ReadFile(t, ".github/workflows/validate-release-prerequisites.yml"), &workflow))
	require.Empty(t, workflow.Env, "no workflow-wide secret or signing environment")

	job, found := workflow.Jobs["validate"]
	require.True(t, found)
	require.Empty(t, job.Env, "no job-wide secret or signing environment")

	return job.Steps
}

func TestPrerequisitesValidationNeverMapsPrivateGPGMaterial(t *testing.T) {
	t.Parallel()

	var validation, summary bool

	for _, step := range keyfreePrerequisiteSteps(t) {
		for _, text := range []string{step.Run, step.If} {
			for _, forbidden := range []string{"release gpg", "release ssh", "GPG_PRIVATE_KEY", "GPG_PASSPHRASE", "GIT_USER_SIGNINGKEY", "GIT_COMMIT_GPGSIGN", "FORGE_CI_GIT_CONFIG_GLOBAL", "steps.import-gpg"} {
				require.NotContains(t, text, forbidden)
			}
		}

		for name, value := range step.Env {
			switch name {
			case "HAS_RELEASE_GPG_PRIVATE_KEY":
				require.Equal(t, "${{ secrets.RELEASE_GPG_PRIVATE_KEY != '' }}", value)
			case "HAS_RELEASE_GPG_PASSPHRASE":
				require.Equal(t, "${{ secrets.RELEASE_GPG_PASSPHRASE != '' }}", value)
			default:
				require.NotContains(t, name, "PRIVATE_KEY")
				require.NotContains(t, name, "PASSPHRASE")
				require.NotContains(t, name, "GIT_COMMIT_GPGSIGN")
				require.NotContains(t, name, "GIT_USER_SIGNINGKEY")
				require.NotContains(t, name, "FORGE_CI_GIT_CONFIG_GLOBAL")
				require.NotContains(t, value, "RELEASE_GPG_PRIVATE_KEY")
				require.NotContains(t, value, "RELEASE_GPG_PASSPHRASE")
			}
		}

		for _, value := range step.With {
			require.NotContains(t, value, "RELEASE_GPG_PRIVATE_KEY")
			require.NotContains(t, value, "RELEASE_GPG_PASSPHRASE")
		}

		if step.ID == "prerequisites" {
			validation = true

			require.Equal(t, "${{ secrets.RELEASE_GPG_PUBLIC_KEY }}", step.Env["RELEASE_GPG_PUBLIC_KEY"])
			require.Equal(t, "${{ inputs['require-allowlisted-signer'] }}", step.Env["REQUIRE_ALLOWLISTED_SIGNER"])
			require.Contains(t, step.Env["REQUIRES_GPG_SIGNING"], ".git_signing.imports_gpg_key")
			require.Contains(t, step.Run, "forge-ci validate prerequisites")
			require.Empty(t, step.If, "GPG selection must not skip public request validation")
		}

		if strings.Contains(step.Run, "forge-ci report status prerequisites") {
			summary = true

			require.Contains(t, step.If, "always()")
		}
	}

	require.True(t, validation)
	require.True(t, summary)
}

func TestPrerequisitesGPGRequestRefusalHasNoPrivateEffects(t *testing.T) {
	// Execute the actual native job's commands with an inert CLI, not bootstrap,
	// checkout, GPG or provider implementations. Model the GPG-selected branch;
	// public-validator behavior itself is tested in internal/app/validate.
	for _, refusal := range []string{"signature", "allowlist"} {
		t.Run(refusal, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			require.NoError(t, os.Mkdir(bin, 0o700))

			privateFile := filepath.Join(root, "unread-private")
			passphraseFile := filepath.Join(root, "unread-passphrase")

			require.NoError(t, os.WriteFile(privateFile, []byte("inert, not a key\n"), 0o600))
			require.NoError(t, os.WriteFile(passphraseFile, []byte("inert, not a passphrase\n"), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(bin, "forge-ci"), []byte(keyfreePrerequisiteCLI), 0o700))

			started, refused := false, false

			for _, step := range keyfreePrerequisiteSteps(t) {
				if step.Run == "forge-ci validate event-context" {
					started = true
				}

				if !started || step.Run == "" || (refused && !strings.Contains(step.If, "always()")) {
					continue
				}

				require.True(t, strings.HasPrefix(step.Run, "forge-ci "), "native command fixture must be reviewed when shell logic changes")

				env := []string{"PATH=" + bin, "EFFECTS=" + root, "REFUSAL=" + refusal}

				for name, value := range step.Env {
					if strings.Contains(value, "${{") {
						value = keyfreePrerequisiteValue(name, value, privateFile, passphraseFile)
					}

					env = append(env, name+"="+value)
				}

				cmd := exec.CommandContext(t.Context(), "/bin/sh", "-eu", "-c", step.Run)
				cmd.Dir, cmd.Env = root, env

				if err := cmd.Run(); err != nil {
					require.Equal(t, "prerequisites", step.ID)

					refused = true
				}
			}

			require.True(t, refused)

			body, err := os.ReadFile(filepath.Join(root, "public-validation"))
			require.NoError(t, err)
			require.Equal(t, refusal, string(body))
			require.FileExists(t, filepath.Join(root, "summary"))

			for _, effect := range []string{"private-reads", "passphrase-reads", "imports", "signing-config", "cleanup"} {
				require.NoFileExists(t, filepath.Join(root, effect), "zero private effects even when GPG is selected")
			}
		})
	}
}

func keyfreePrerequisiteValue(name, value, privateFile, passphraseFile string) string {
	switch {
	case name == "HAS_RELEASE_GPG_PRIVATE_KEY" || name == "HAS_RELEASE_GPG_PASSPHRASE" || name == "REQUIRE_ALLOWLISTED_SIGNER" || name == "REQUIRES_GPG_SIGNING":
		return "true"
	case name == "CONFIG_PLAN_JSON":
		return `{"git_signing":{"imports_gpg_key":true},"sign":{"imports_gpg_key":true}}`
	case name == "RELEASE_GPG_PUBLIC_KEY":
		return "inert public verification material"
	case strings.Contains(value, "secrets.RELEASE_GPG_PRIVATE_KEY"):
		return privateFile
	case strings.Contains(value, "secrets.RELEASE_GPG_PASSPHRASE"):
		return passphraseFile
	default:
		return "inert"
	}
}

const keyfreePrerequisiteCLI = `#!/bin/sh
set -eu
if [ -n "${GPG_PRIVATE_KEY:-}" ]; then
  read -r _material < "$GPG_PRIVATE_KEY"
  printf read >> "$EFFECTS/private-reads"
fi
if [ -n "${GPG_PASSPHRASE:-}" ]; then
  read -r _material < "$GPG_PASSPHRASE"
  printf read >> "$EFFECTS/passphrase-reads"
fi
if [ -n "${GIT_USER_SIGNINGKEY:-}${GIT_COMMIT_GPGSIGN:-}${FORGE_CI_GIT_CONFIG_GLOBAL:-}" ]; then
  printf config >> "$EFFECTS/signing-config"
fi
case "$*" in
  'release gpg import') printf import >> "$EFFECTS/imports" ;;
  'release gpg cleanup') printf cleanup >> "$EFFECTS/cleanup" ;;
  validate\ prerequisites*)
    test "$REQUIRES_GPG_SIGNING" = true
    test "$REQUIRE_ALLOWLISTED_SIGNER" = true
    test "$RELEASE_GPG_PUBLIC_KEY" = 'inert public verification material'
    printf %s "$REFUSAL" > "$EFFECTS/public-validation"
    exit 77 ;;
  'report status prerequisites') printf summary > "$EFFECTS/summary" ;;
esac
`
