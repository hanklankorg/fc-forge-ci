// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/goccy/go-yaml/ast"
	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestRuntimePublicationBarriersAndSigningBoundary(t *testing.T) {
	t.Parallel()

	runtimeWorkflow := loadWorkflow(t, ".forgejo/workflows/runtime-images.yml")

	for name, prerequisite := range map[string]string{
		"build": "request-runner", "collect": "build", "smoke": "collect",
		"assemble": "smoke", "sign": "assemble", "publish": "sign",
	} {
		require.Contains(t, jobNeeds(runtimeWorkflow.jobs[name]), prerequisite, "%s must wait for the entire preceding matrix", name)
	}

	require.Contains(t, jobNeeds(runtimeWorkflow.jobs["smoke"]), "request-smoke-runner")
	require.Contains(t, jobNeeds(runtimeWorkflow.jobs["request-smoke-runner"]), "collect")

	require.Equal(t, "forgejo.ref == 'refs/heads/main'", scalarString(mappingValue(runtimeWorkflow.jobs["plan"].node, "if")))

	for name, target := range runtimeWorkflow.jobs {
		text := target.node.String()
		if name != "sign" {
			require.NotContains(t, text, "secrets.RELEASE_COSIGN", "signing authority leaked to %s", name)
		}

		if !slices.Contains([]string{"request-runner", "request-smoke-runner"}, name) {
			require.NotContains(t, text, "secrets.CI_RUNNER_LENDER_TOKEN", "runner-lending authority leaked to %s", name)
		}

		for _, step := range target.steps() {
			if strings.Contains(scalarString(mappingValue(step, "run")), "platform checkout") {
				require.Equal(t, "${{ forgejo.token }}", scalarString(mappingValue(mappingValue(step, "env"), "FORGEJO_TOKEN")), "%s must authenticate private source checkout", name)
			}
		}

		if name == "sign" || name == "publish" {
			require.Equal(t, signerImage, scalarString(mappingValue(target.node, "container")), "%s must use the reviewed signer, never a candidate", name)

			for _, forbidden := range []string{"platform checkout", "runtime-smoke.sh", "mise install", "apt-get", "go run", "bash scripts/"} {
				require.NotContains(t, text, forbidden, "%s must not execute source or install tools", name)
			}

			if name == "sign" {
				require.Contains(t, text, "runtime-publication sign", "Go signing must verify the entire index and evidence before signing")
				require.NotContains(t, text, "while read", "native evidence/signing iteration belongs in Go")
			} else {
				require.Contains(t, text, "runtime-publication promote", "Go promotion must verify evidence, freshness and promoted bytes")
			}
		}
	}

	require.Equal(t, "${{ matrix.ref }}", scalarString(mappingValue(mappingValue(runtimeWorkflow.jobs["smoke"].node, "container"), "image")))
	require.Contains(t, runtimeWorkflow.jobs["smoke"].node.String(), "sha256sum --check --strict")
	require.True(t, jobRunsStep(runtimeWorkflow.jobs["plan"], "container platform-plan"))
	require.True(t, jobRunsStep(runtimeWorkflow.jobs["request-runner"], "platform request-runner"))
	require.Equal(t, "${{ needs.coordinates.outputs.builder }}", scalarString(mappingValue(mappingValue(runtimeWorkflow.jobs["build"].node, "env"), "CI_IMAGE_BUILDER")))
	require.Equal(t, runtimeBuildRunner, runtimeWorkflow.jobs["build"].runsOn())
	require.Equal(t, runtimeSmokeRunner, runtimeWorkflow.jobs["smoke"].runsOn())
	require.Contains(t, runtimeWorkflow.jobs["publish"].node.String(), "runtime-publication promote")
	require.NotContains(t, runtimeWorkflow.jobs["publish"].node.String(), "--no-creds")
	require.NotContains(t, runtimeWorkflow.jobs["publish"].node.String(), "git ls-remote")
	require.NotContains(t, runtimeWorkflow.jobs["publish"].node.String(), "curl --config -", "authenticated API effects belong to the provider adapter")
	credentials := mappingValue(mappingValue(runtimeWorkflow.jobs["smoke"].node, "container"), "credentials")
	require.Equal(t, "${{ secrets.RELEASE_REGISTRY_TOKEN }}", scalarString(mappingValue(credentials, "password")))
}

func TestRuntimePublicationChecksToolchainBeforeExposingTheEngine(t *testing.T) {
	t.Parallel()

	workflow := loadWorkflow(t, ".forgejo/workflows/runtime-images.yml")
	for name, target := range workflow.jobs {
		if target.containerImage() != signerImage {
			continue
		}

		steps := target.steps()
		require.GreaterOrEqual(t, len(steps), 5, name)
		require.Equal(t, "Expose the verified engine", scalarString(mappingValue(steps[4], "name")), name)
		require.Contains(t, scalarString(mappingValue(steps[4], "run")), "setup-forge-ci.sh", name)

		for _, step := range steps[:4] {
			require.NotContains(t, scalarString(mappingValue(step, "run")), "setup-forge-ci.sh", name)
			require.NotContains(t, scalarString(mappingValue(step, "run")), "--version", name)
		}
	}

	require.False(t, anyString(workflow.jobs["build-cli"].node, secretReference.MatchString), "native CLI compilation receives no named secret")

	for _, name := range []string{"build-cli", "build", "smoke"} {
		for _, key := range []string{"RELEASE_COSIGN", "RELEASE_GPG", "RELEASE_SSH", "RELEASE_TOKEN"} {
			require.NotContains(t, workflow.jobs[name].node.String(), key, "%s must have no release/signing authority", name)
		}
	}
}

func TestRuntimePublicationPreflightAdmitsOnlyPinnedBuildImages(t *testing.T) {
	t.Parallel()

	script := preflightScript(t, ".forgejo/workflows/runtime-images.yml")
	for _, test := range []struct {
		name     string
		env      []string
		accepted bool
	}{
		{"buildah host", nil, true},
		{"kaniko pinned", []string{"BUILDER_INPUT=kaniko", "BUILDER_JOB_IMAGE_INPUT=" + exampleSigner}, true},
		{"kaniko absent", []string{"BUILDER_INPUT=kaniko"}, false},
		{"mutable builder", []string{"BUILDER_JOB_IMAGE_INPUT=registry.example/acme/builder:latest"}, false},
		{"unknown builder", []string{"BUILDER_INPUT=docker"}, false},
		{"mutable go", []string{"GO_BUILD_IMAGE_INPUT=registry.example/acme/go:latest"}, false},
		{"missing go", []string{"GO_BUILD_IMAGE_INPUT="}, false},
		{"malformed key pin", []string{"PUBLIC_KEY_SHA256=not-a-digest"}, false},
		{"short engine commit", []string{"RUNTIME_ENGINE_SHA=1234567"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			accepted, output, _ := runPreflightWithStderr(t, script, exampleRepository, exampleSigner, test.env...)
			require.Equal(t, test.accepted, accepted)

			if !accepted {
				require.Empty(t, output, "refusal cannot emit partial trusted coordinates")
			}
		})
	}
}

func TestRuntimePublicationPromotionDelegatesCoordinatesAndStatus(t *testing.T) {
	t.Parallel()

	w := loadWorkflow(t, ".forgejo/workflows/runtime-images.yml")
	step, ok := w.findStep("Verify signatures and reject a stale main before promotion")
	require.True(t, ok)
	literal, ok := mappingValue(step, "run").(*ast.LiteralNode)
	require.True(t, ok)

	for _, status := range []int{0, 69} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			record := filepath.Join(root, "arguments")
			require.NoError(t, os.WriteFile(filepath.Join(root, "forge-ci"), []byte("#!/bin/sh\nprintf '%s\\0' \"$@\" >\"$CLI_ARGS\"\nexit \"$CLI_STATUS\"\n"), 0o700))

			cmd := exec.CommandContext(t.Context(), testBash(t), "-c", literal.Value.Value)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PATH="+root+":"+os.Getenv("PATH"), "TARGET=go", "REGISTRY_AUTH_FILE=auth with spaces.json",
				"FORGEJO_SERVER=https://forge.example", "FORGEJO_REPO=acme/widget", "FORGEJO_TOKEN=fixture-forge-token",
				"IMAGE_REPOSITORY=registry.example/acme/runtime", "SOURCE_SHA="+strings.Repeat("a", 40), "CANDIDATE_TAG=staging-123-1",
				`ARCHS_JSON=["amd64","arm64"]`, "TRUSTED_KEY_INPUT=fixture public key", "TRUSTED_KEY_SHA256="+strings.Repeat("b", 64),
				"CLI_ARGS="+record, "CLI_STATUS="+strconv.Itoa(status))

			out, err := cmd.CombinedOutput()
			if status == 0 {
				require.NoError(t, err, "%s", out)
			} else {
				var exit *exec.ExitError
				require.ErrorAs(t, err, &exit)
				require.Equal(t, status, exit.ExitCode())
			}

			body, err := os.ReadFile(record)
			require.NoError(t, err)
			require.Equal(t, []string{"container", "runtime-publication", "promote", "--auth-file", "auth with spaces.json",
				"--source-sha", strings.Repeat("a", 40), "--server-url", "https://forge.example", "--repository", "acme/widget",
				"--image-repository", "registry.example/acme/runtime", "--tag", "staging-123-1", "--name", "runtime-go",
				"--archs", "amd64\narm64", "--cosign-public-key-path", "cosign.pub", "--cosign-public-key-sha256", strings.Repeat("b", 64)}, strings.Split(strings.TrimSuffix(string(body), "\x00"), "\x00"))
			require.NotContains(t, string(body), "fixture-forge-token", "API credentials must not enter argv")
		})
	}
}

func TestRuntimeSmokeReceiptIsWrittenOnlyAfterSuccessfulChecks(t *testing.T) {
	t.Parallel()

	w := loadWorkflow(t, ".forgejo/workflows/runtime-images.yml")
	checked := false
	recorded := false

	for _, step := range w.jobs["smoke"].steps() {
		name := scalarString(mappingValue(step, "name"))
		if name == "Exercise the native runtime toolchain" {
			checked = true

			require.Nil(t, mappingValue(step, "continue-on-error"))
		}

		if name == "Record the digest that passed native smoke checks" {
			recorded = true

			require.True(t, checked, "receipt must follow smoke checks")
			require.Nil(t, mappingValue(step, "if"), "default success gating is required")
			require.Nil(t, mappingValue(step, "continue-on-error"))
			env := mappingValue(step, "env")
			require.Equal(t, "${{ matrix.ref }}", scalarString(mappingValue(env, "TESTED_REF")))
			require.Equal(t, "${{ matrix.arch }}", scalarString(mappingValue(env, "ARCH")))
			require.Equal(t, "${{ matrix.target }}", scalarString(mappingValue(env, "TARGET")))
		}
	}

	require.True(t, recorded)
}

func TestRuntimeSmokeRunnerRequestsOnlyMissingNativeARMCapacity(t *testing.T) {
	t.Parallel()

	w := loadWorkflow(t, ".forgejo/workflows/runtime-images.yml")

	var script string

	for _, step := range w.jobs["request-smoke-runner"].steps() {
		if scalarString(mappingValue(step, "name")) == "Request fresh smoke runners when ARM64 uses the build pool" {
			require.Equal(t, "inputs['runner-label-arm64'] == ''", scalarString(mappingValue(step, "if")))
			literal, ok := mappingValue(step, "run").(*ast.LiteralNode)
			require.True(t, ok)

			script = literal.Value.Value
		}
	}

	require.NotEmpty(t, script)

	for _, tc := range []struct {
		name, matrix string
		count        int
		fails        bool
	}{
		{"amd64_only", `{"include":[{"arch":"amd64"}]}`, 0, false},
		{"arm64_only", `{"include":[{"arch":"arm64"}]}`, 1, false},
		{"both", `{"include":[{"arch":"amd64"},{"arch":"arm64"},{"arch":"arm64"}]}`, 2, false},
		{"malformed", `{`, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			log := filepath.Join(root, "calls")
			tool := filepath.Join(root, "forge-ci")
			body := `#!/bin/sh
set -eu
test "${CI_RUNNER_LENDER_TOKEN+x}" != x
test "${lender_token+x}" != x
test "$(cat)" = fixture-token
printf '%s\n' "$*" >> "$CALL_LOG"
`
			require.NoError(t, os.WriteFile(tool, []byte(body), 0o600))
			require.NoError(t, os.Chmod(tool, 0o700))
			cmd := exec.CommandContext(t.Context(), testBash(t), "-c", script)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PATH="+root+":"+os.Getenv("PATH"), "CALL_LOG="+log, "CI_RUNNER_LENDER_TOKEN=fixture-token", "lender_token=previous-export", "SMOKE_MATRIX_JSON="+tc.matrix)

			out, err := cmd.CombinedOutput()
			if tc.fails {
				require.Error(t, err, "%s", out)
			} else {
				require.NoError(t, err, "%s", out)
			}

			if tc.count == 0 {
				require.NoFileExists(t, log)
			} else {
				calls, readErr := os.ReadFile(log)
				require.NoError(t, readErr)
				require.Equal(t, strings.Repeat("platform request-runner --arch arm64 --token-file -\n", tc.count), string(calls))
			}
		})
	}
}

func TestRuntimeFlavoursMatchTheGitHubPublisherAndContainerfile(t *testing.T) {
	t.Parallel()

	var roster []struct {
		Target string   `json:"target"`
		Name   string   `json:"name"`
		Archs  []string `json:"archs"`
	}
	require.NoError(t, json.Unmarshal(reporoot.ReadFile(t, "containers/runtime/images.json"), &roster))
	require.Len(t, roster, 7)
	containerfile := string(reporoot.ReadFile(t, "containers/runtime/Containerfile"))

	github := loadWorkflow(t, ".github/workflows/self-runtime-container.yml")
	for _, name := range []string{"verify", "publish-arch", "publish-merge"} {
		entries := sequenceValues(mappingValue(mappingValue(mappingValue(github.jobs[name].node, "strategy"), "matrix"), "runtime"))
		require.Len(t, entries, len(roster))

		for _, image := range roster {
			found := false

			for _, entry := range entries {
				if scalarString(mappingValue(entry, "target")) == image.Target && scalarString(mappingValue(entry, "name")) == image.Name {
					found = true
				}
			}

			require.True(t, found, "%s omits %s", name, image.Name)
			require.Contains(t, containerfile, " AS "+image.Target+"\n")

			if image.Target == "android" {
				require.Equal(t, []string{"amd64"}, image.Archs)
			} else {
				require.True(t, slices.Equal(image.Archs, []string{"amd64", "arm64"}))
			}
		}
	}
}

func TestRuntimeExampleRequiresExplicitPublicationPolicy(t *testing.T) {
	t.Parallel()

	for _, file := range []string{"runtime-images.yml"} {
		body := string(reporoot.ReadFile(t, "examples/forgejo/"+file))
		require.Contains(t, body, "vars.RUNTIME_IMAGE_NAMESPACE", file)
		require.Contains(t, body, "cosign-public-key-sha256: PIN_COSIGN_PUBLIC_KEY_SHA256", file)
		require.Contains(t, body, ".forgejo/workflows/runtime-images.yml@FORGE_CI_COMMIT", file)
		require.NotContains(t, body, "runtime-images.sh", file)
		require.NotContains(t, body, "itiquette", file)

		for _, forbidden := range []string{"182c68718a03dc3f085cab23897f5eb5d8c69d29", "localhost/forge-ci-signer", "--object-format sha256", "object-format: sha256"} {
			require.NotContains(t, body, forbidden, file)
		}

		for line := range strings.SplitSeq(body, "\n") {
			if strings.Contains(line, "uses:") {
				require.Contains(t, line, "OWNER/forge-ci/", file)
				require.Contains(t, line, "@FORGE_CI_COMMIT", file)
			}
		}
	}
}
