// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestCanonicalBuilderProfileGraph(t *testing.T) {
	t.Parallel()
	root := readCanonicalConsumerWorkflow(t, "release-orchestrator.yml")
	call := root.Jobs["canonical-cli"]
	require.Equal(t, "${{ inputs['canonical-builder-image'] }}", call.With["runtime-image"], "never fall back to a possibly Rust-only product runtime")
	require.Equal(t, "${{ inputs['runtime-image'] }}", call.With["receive-runtime-image"])
	require.Empty(t, call.If, "canonical delivery runs on every release route")
	require.Equal(t, "${{ inputs['forge-ci-binary-ref'] }}", call.With["engine-commit"])
	require.NotContains(t, root.Jobs["validate-prerequisites"].With["runtime-image"], "canonical-builder-image")

	var workflow struct {
		Jobs map[string]struct {
			Needs     string `yaml:"needs"`
			Container struct {
				Image string `yaml:"image"`
			} `yaml:"container"`
			Env map[string]string `yaml:"env"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(reporoot.ReadFile(t, ".github/workflows/canonical-cli-bootstrap.yml"), &workflow))

	for name, image := range map[string]string{
		"canonical-build":   "${{ inputs.runtime-image }}",
		"canonical-receive": "${{ inputs.receive-runtime-image || inputs.runtime-image }}",
	} {
		job := workflow.Jobs[name]
		require.Equal(t, image, job.Container.Image)
		require.Equal(t, image, job.Env["RUNTIME_IMAGE"], "admitted helper profile must match its actual container, not the other job")
	}

	require.Equal(t, "canonical-admit", workflow.Jobs["canonical-build"].Needs)
	require.Equal(t, "canonical-build", workflow.Jobs["canonical-receive"].Needs)
	require.Empty(t, workflow.Jobs["canonical-admit"].Container.Image)
}

func TestCanonicalBuilderAdmissionBeforeExecution(t *testing.T) {
	t.Parallel()

	var workflow struct {
		Jobs map[string]struct {
			Steps []canonicalConsumerStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(reporoot.ReadFile(t, ".github/workflows/canonical-cli-bootstrap.yml"), &workflow))
	admit := workflow.Jobs["canonical-admit"].Steps[0]
	require.Equal(t, "${{ inputs.runtime-image }}", admit.Env["RUNTIME_IMAGE"])
	require.Equal(t, "${{ inputs.receive-runtime-image || inputs.runtime-image }}", admit.Env["RECEIVE_RUNTIME_IMAGE"])

	goImage := "registry.invalid/go@sha256:" + strings.Repeat("a", 64)
	rustImage := "registry.invalid/rust@sha256:" + strings.Repeat("b", 64)

	for _, scenario := range []struct {
		name, builder, receiver string
		omitBuilder, valid      bool
	}{
		{name: "root-go", builder: goImage, receiver: goImage, valid: true},
		{name: "cargo-separate-go-builder", builder: goImage, receiver: rustImage, valid: true},
		{name: "direct-go-qualified-default", builder: goImage, receiver: goImage, valid: true},
		{name: "missing-builder", receiver: rustImage, omitBuilder: true},
		{name: "empty-builder", receiver: rustImage},
		{name: "tag-builder", builder: "registry.invalid/go:latest", receiver: rustImage},
		{name: "short-digest-builder", builder: "registry.invalid/go@sha256:abc", receiver: rustImage},
		{name: "conflicting-builder-selections", builder: goImage + " " + rustImage, receiver: rustImage},
		{name: "shell-builder", builder: "$(touch forbidden)", receiver: rustImage},
		{name: "tag-receiver", builder: goImage, receiver: "registry.invalid/rust:latest"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			cmd := exec.CommandContext(t.Context(), testBash(t), "-c", admit.Run+"\nprintf private > effect\n")
			cmd.Dir = dir
			cmd.Env = []string{"ENGINE_REPOSITORY=approved/engine", "ENGINE_COMMIT=" + strings.Repeat("c", 40), "RECEIVE_RUNTIME_IMAGE=" + scenario.receiver}

			if !scenario.omitBuilder {
				cmd.Env = append(cmd.Env, "RUNTIME_IMAGE="+scenario.builder)
			}

			out, err := cmd.CombinedOutput()
			if scenario.valid {
				require.NoError(t, err, string(out))
				require.FileExists(t, filepath.Join(dir, "effect"))
			} else {
				require.Error(t, err)
				require.NoFileExists(t, filepath.Join(dir, "effect"))
			}

			require.NoFileExists(t, filepath.Join(dir, "forbidden"))
		})
	}
}
