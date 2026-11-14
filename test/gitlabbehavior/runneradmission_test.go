// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/domain/runners"
	"github.com/diggsweden/forge-ci/internal/testutil/gitlabrender"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// The policy's literal inputs, not a higher-precedence project/job variable,
// reach the image plan unchanged. Discover the inputs from the component and
// shared naming owner so a newly declared runner input cannot escape this gate.
func TestGitLabImagePlansPassEveryDeclaredRunnerInput(t *testing.T) {
	t.Parallel()

	const path = "templates/release-jobs.yml"

	source := string(reporoot.ReadFile(t, path))
	header, _, _ := strings.Cut(source, "\n---\n")

	var spec struct {
		Spec struct {
			Inputs map[string]any `yaml:"inputs"`
		} `yaml:"spec"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(header), &spec))

	known, err := runners.Names(provider.ForgeGitLab)
	require.NoError(t, err)

	given := map[string]any{}
	want := map[string]bool{}
	env := []string{"RELEASE_TAG=v1.2.3", "RC_REGISTRY_IMAGE=registry.invalid/team/app", "CI_PROJECT_PATH=team/app"}
	block := releasePolicyReadonlyBlock(t, source)

	for name := range spec.Spec.Inputs {
		if !strings.Contains(name, "runner") {
			continue
		}

		require.Contains(t, known, name)
		binding := "RC_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		require.Contains(t, block, binding+"='$[[ inputs."+name+" ]]'")
		value := []any{name, "native=pool"}
		given[name] = value
		encoded, err := json.Marshal(value)
		require.NoError(t, err)

		want[name+"="+string(encoded)] = true

		env = append(env, binding+`=["untrusted-variable"]`)
	}

	require.NotEmpty(t, want)
	job := gitlabrender.Jobs(t, path, given)["Check: release request"]
	assignments := releasePolicyReadonlyBlock(t, fmt.Sprint(job["before_script"]))
	body := assignments + "\n" + `rc_engine() { printf '%s\0' "$@"; }; rc_plan_release_images .forge-ci/artifacts.yml '' plan.json`
	out, err := runReleaseFunction(t, t.TempDir(), body, env...)
	require.NoError(t, err, out)
	args := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	require.NotContains(t, args, "--runner-archs", "runner admission replaces the inferred architecture list")

	for i := 0; i < len(args); i++ {
		if args[i] != "--runner-input" {
			continue
		}

		i++
		require.Less(t, i, len(args))
		require.True(t, want[args[i]], "unexpected, rewritten or duplicate input %q", args[i])
		delete(want, args[i])
	}

	require.Empty(t, want, "declared runner inputs missing from image admission")
}

func releasePolicyReadonlyBlock(t *testing.T, source string) string {
	t.Helper()

	_, rest, found := strings.Cut(source, "readonly RC_RELEASE_POLICY=")
	require.True(t, found, "policy inputs must be bound readonly")

	lines := strings.Split("readonly RC_RELEASE_POLICY="+rest, "\n")
	for i, line := range lines {
		if !strings.HasSuffix(strings.TrimSpace(line), "\\") {
			return strings.Join(lines[:i+1], "\n")
		}
	}

	t.Fatal("unterminated readonly input binding")

	return ""
}

// The real Go owner, invoked through the shell glue, refuses missing runners
// before writing a frozen image plan. Runner names never select platforms,
// and neither ordinary nor another architecture's runner is a fallback.
func TestGitLabImagePlanAdmissionRefusesMissingNativeRunner(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, declared, selected, amd64, arm64, refusal string
		omitAMD64                                       bool
	}{
		{name: "amd64", declared: "linux/amd64", amd64: `["build","amd64"]`, arm64: "[]"},
		{name: "missing amd64", declared: "linux/amd64", amd64: "[]", arm64: `["arm64"]`, refusal: "build-runner-tags-linux-amd64"},
		{name: "missing binding", declared: "linux/amd64", omitAMD64: true, arm64: "[]", refusal: "RC_BUILD_RUNNER_TAGS_LINUX_AMD64"},
		{name: "missing arm64", declared: "linux/amd64,linux/arm64", amd64: `["amd64"]`, arm64: "[]", refusal: "build-runner-tags-linux-arm64"},
		{name: "empty arm64", declared: "linux/arm64", amd64: `["amd64"]`, refusal: "build-runner-tags-linux-arm64"},
		{name: "both", declared: "linux/amd64,linux/arm64", amd64: `["amd64"]`, arm64: `["build","arm64"]`},
		{name: "narrowed", declared: "linux/amd64,linux/arm64", selected: "linux/amd64", amd64: `["amd64"]`, arm64: "[]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			work := t.TempDir()
			declaration := "containers:\n  - name: app\n    platforms: " + tc.declared + "\n    enable-slsa: false\n"
			require.NoError(t, os.WriteFile(filepath.Join(work, "artifacts.yml"), []byte(declaration), 0600))

			env := []string{"OWNER_CLI_BINARY=" + os.Args[0], "FORGE_CI_PROVIDER=gitlab", "FORGE_CI_RUNNER=local",
				"RELEASE_TAG=v1.2.3", "RC_REGISTRY_IMAGE=registry.invalid/team/app", "RC_SERVER_URL=https://gitlab.invalid", "CI_PROJECT_PATH=team/app", "RC_IMAGE_LABELS=",
				`RC_RUNNER_TAGS=["ordinary","amd64","arm64"]`, "RC_SIGNER_RUNNER_TAGS=[]", "RC_BUILD_RUNNER_TAGS_LINUX_ARM64=" + tc.arm64}
			if !tc.omitAMD64 {
				env = append(env, "RC_BUILD_RUNNER_TAGS_LINUX_AMD64="+tc.amd64)
			}

			out, err := runReleaseFunction(t, work, ownerCLI+"rc_plan_release_images artifacts.yml '"+tc.selected+"' plan.json", env...)
			if tc.refusal != "" {
				require.Error(t, err, out)
				require.Contains(t, out, tc.refusal)
				require.NoFileExists(t, filepath.Join(work, "plan.json"))

				return
			}

			require.NoError(t, err, out)

			var plan struct {
				Builds []struct {
					Arch string `json:"arch"`
				} `json:"builds"`
			}

			encoded, err := os.ReadFile(filepath.Join(work, "plan.json"))
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(encoded, &plan))

			selection := tc.declared
			if tc.selected != "" {
				selection = tc.selected
			}

			var got []string
			for _, build := range plan.Builds {
				got = append(got, "linux/"+build.Arch)
			}

			require.ElementsMatch(t, strings.Split(selection, ","), got)
		})
	}
}
