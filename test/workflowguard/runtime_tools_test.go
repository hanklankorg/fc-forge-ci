// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestTrustedRuntimeToolOwners(t *testing.T) {
	t.Parallel()
	helper := string(reporoot.ReadFile(t, "scripts/ci/trusted-runtime-tools.sh"))
	// The three profiles are the whole finite set, each one a literal directory
	// list. The host profiles carry no toolchain homes, and no profile reads a
	// caller PATH, workspace or tool-directory variable. host-toolchain ends
	// with the runner user's Cargo directory: a hosted runner has cargo there
	// alone, and the key-free bump runs it to refresh Cargo.lock.
	require.Contains(t, helper, "  export PATH=/usr/local/go/bin:/usr/local/cargo/bin:/usr/local/bin:/usr/bin:/bin\n")
	require.Contains(t, helper, "  export RUSTUP_HOME=/usr/local/rustup")
	require.Contains(t, helper, "  export CARGO_HOME=${HOME:?}/.cargo")
	require.Contains(t, helper, "host) export PATH=/usr/bin:/bin ;;")
	require.Contains(t, helper, "  [[ ${HOME:?} == /* && $HOME != *:* ]] || refuse 'HOME must be an absolute path without a colon'\n  export PATH=/usr/bin:/bin:/usr/local/bin:$HOME/.cargo/bin\n")
	require.Contains(t, helper, `*) refuse "unknown tool profile: $1" ;;`)
	require.Equal(t, 3, strings.Count(helper, "export PATH="), "a fourth profile needs its own review")
	require.NotContains(t, helper, "${PATH")
	require.NotContains(t, helper, "GITHUB_WORKSPACE")
	require.NotContains(t, helper, "TRUSTED_TOOL_PATH")
	require.NotContains(t, helper, "RUNNER_TEMP")

	bootstrap := string(reporoot.ReadFile(t, "scripts/ci/trusted-bootstrap.sh"))
	require.Less(t, strings.Index(bootstrap, "verify_forge_ci || exit $?"), strings.Index(bootstrap, "exec /bin/bash -p"))
	require.Contains(t, bootstrap, `"$checkout/scripts/ci/trusted-runtime-tools.sh" "$profile" "$HELPERS_ENGINE_VERIFIED" "$@"`)
	require.Contains(t, bootstrap, ".engine/bin/forge-ci-linux-amd64.sha256 .engine/bin/forge-ci-linux-arm64.sha256 scripts/ci/trusted-runtime-tools.sh; do")

	consumer := string(reporoot.ReadFile(t, "scripts/ci/canonical-consumer.sh"))
	require.Contains(t, consumer, `[[ "${TOOL_PROFILE:-}" =~ ^[a-z][a-z-]*$ ]]`)
	require.Contains(t, consumer, "\n  \"$TOOL_PROFILE\" >\"$directory/forge-ci\"", "the launcher bakes the admitted literal")

	var workflow struct {
		Jobs map[string]struct {
			Steps []canonicalConsumerStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(reporoot.ReadFile(t, ".github/workflows/canonical-cli-bootstrap.yml"), &workflow))

	prepared := false
	found := false

	for _, step := range workflow.Jobs["canonical-receive"].Steps {
		if strings.Contains(step.Run, `canonical-cli-delivery.sh" prepare`) {
			prepared = true
		}

		if strings.Contains(step.Run, "gh api") {
			require.True(t, prepared, "the actual helper file must be admitted before metadata")
			require.Contains(t, step.Run, `/bin/bash -p "$ENGINE_CHECKOUT/scripts/ci/trusted-runtime-tools.sh" runtime gh api`)
			require.Equal(t, "${{ github.token }}", step.Env["GH_TOKEN"])

			found = true
		}
	}

	require.True(t, found)

	var action struct {
		Runs struct {
			Steps []canonicalConsumerStep `yaml:"steps"`
		} `yaml:"runs"`
	}
	require.NoError(t, yaml.Unmarshal(reporoot.ReadFile(t, ".github/actions/consume-canonical-cli/action.yml"), &action))
	require.Contains(t, action.Runs.Steps[0].Run, "prepare")
	require.Contains(t, action.Runs.Steps[1].Run, `/bin/bash -p "$RUNNER_TEMP/forge-ci-approved-engine/scripts/ci/trusted-runtime-tools.sh" "$TOOL_PROFILE" gh api`)

	for index, step := range action.Runs.Steps {
		if step.Run != "" {
			require.Equal(t, "${{ inputs.tool-profile }}", step.Env["TOOL_PROFILE"], "action step %d", index)
		}
	}

	// Every receiver states its profile as a literal in admitted YAML: runtime
	// inside the approved image, host on a hosted runner, host-toolchain only for
	// the key-free version-file preparation.
	profiles := map[string]string{}

	for _, file := range []string{"release-orchestrator.yml", "release-prepare-stage.yml", "release-create-github.yml", "publish-container.yml", "snapshot-images.yml", "release-snapshot-orchestrator.yml", "pullrequest-orchestrator.yml", "lint-nanolinter.yml", "lint-megalinter.yml"} {
		var receivers struct {
			Jobs map[string]struct {
				Container any `yaml:"container"`
				Steps     []struct {
					Uses string         `yaml:"uses"`
					With map[string]any `yaml:"with"`
				} `yaml:"steps"`
			} `yaml:"jobs"`
		}
		require.NoError(t, yaml.Unmarshal(reporoot.ReadFile(t, ".github/workflows/"+file), &receivers))

		for job, spec := range receivers.Jobs {
			for _, step := range spec.Steps {
				if step.Uses != "./_forge_ci_bootstrap/.github/actions/consume-canonical-cli" {
					continue
				}

				profile, _ := step.With["tool-profile"].(string)
				profiles[file+"/"+job] = profile

				if spec.Container != nil {
					require.Equal(t, "runtime", profile, "%s/%s", file, job)
				} else {
					require.Contains(t, []string{"host", "host-toolchain"}, profile, "%s/%s", file, job)
				}
			}
		}
	}

	require.Equal(t, "host-toolchain", profiles["release-prepare-stage.yml/prepare-version-files"])
	require.Equal(t, "host", profiles["release-prepare-stage.yml/version-bump"])
	require.Equal(t, 1, countValues(profiles, "host-toolchain"), "only key-free version-file preparation reaches runner toolchains")
	require.GreaterOrEqual(t, len(profiles), 12)
}

func countValues(values map[string]string, want string) int {
	count := 0

	for _, value := range values {
		if value == want {
			count++
		}
	}

	return count
}
