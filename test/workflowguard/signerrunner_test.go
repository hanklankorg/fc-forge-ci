// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/runners"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// signingSecrets are the secrets that sign: release, package and app signing
// keys and their passphrases.
var signingSecrets = []string{
	"RELEASE_GPG_PRIVATE_KEY", "RELEASE_GPG_PASSPHRASE",
	"RELEASE_COSIGN_PRIVATE_KEY", "RELEASE_COSIGN_PASSPHRASE",
	"RELEASE_SSH_SIGNING_KEY",
	"ANDROID_KEYSTORE", "ANDROID_KEYSTORE_PASSWORD", "ANDROID_KEY_ALIAS", "ANDROID_KEY_PASSWORD",
	"IOS_SIGNING_CERTIFICATE_BASE64", "IOS_SIGNING_CERTIFICATE_PASSPHRASE", "PROVISIONING_PROFILE_BASE64",
}

// The GitHub signer role is a hosted literal: a job of a reusable workflow
// that receives a signing secret, or a GitHub OIDC token to sign keylessly with,
// names its runner as a literal label, never an input or any other
// expression, so no caller chooses the runner that holds the key.
func TestGitHubSigningJobsRunOnALiteralRunner(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(reporoot.Path(t), ".github", "workflows")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	signing := 0

	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".yml" {
			continue
		}

		var workflow struct {
			On   map[string]yaml.Node `yaml:"on"`
			Jobs map[string]yaml.Node `yaml:"jobs"`
		}

		require.NoError(t, yaml.Unmarshal(reporoot.ReadFile(t, ".github/workflows/"+entry.Name()), &workflow), entry.Name())

		// Only a reusable workflow has a caller who could choose its runner.
		if _, reusable := workflow.On["workflow_call"]; !reusable {
			continue
		}

		for name, node := range workflow.Jobs {
			var job struct {
				RunsOn      yaml.Node         `yaml:"runs-on"`
				Uses        string            `yaml:"uses"`
				Permissions map[string]string `yaml:"permissions"`
			}

			require.NoError(t, node.Decode(&job), "%s/%s", entry.Name(), name)

			// A reusable call has no runner; the callee's jobs are checked.
			if job.Uses != "" {
				continue
			}

			text, err := yaml.Marshal(&node)
			require.NoError(t, err)

			if !holdsSigningMaterial(string(text), job.Permissions) {
				continue
			}

			signing++

			if want, ok := callerRunnerSigners[entry.Name()+"/"+name]; ok {
				require.Equal(t, want, job.RunsOn.Value, "%s/%s signs on the runner of exactly this input", entry.Name(), name)

				continue
			}

			require.True(t, githubSigningRunnerAllowed(job.RunsOn), "%s/%s holds signing material; its runs-on must be an approved hosted literal", entry.Name(), name)
		}
	}

	require.Positive(t, signing, "no signing job found: the guard checks nothing")
}

// callerRunnerSigners are the signing jobs that run where their caller says,
// each with the one expression it may use. The signed Xcode build is one: the
// Xcode version a project builds with decides the macOS image, so the build
// takes the caller's macOS runner whether it signs or not.
var callerRunnerSigners = map[string]string{
	"build-xcode-ios.yml/build-variants-signed": "${{ fromJSON(inputs['runs-on-darwin-arm64']) }}",
}

// A signing job that runs where its caller says is held to the administrator's
// allowlist: every job that calls its workflow needs a job on an approved
// hosted literal, which no caller names, that admits the same runner input
// against enforce-signer-runs-on-darwin-arm64, for the plan the call takes its builds
// from. Every caller of a workflow that takes the list forwards its own, so
// the value the administrator gives the entry point is the one compared.
func TestCallerRunnerSignersAreHeldToTheAllowlist(t *testing.T) {
	t.Parallel()

	const allowlist = "${{ inputs['" + runners.SignerAllowlistInput + "'] }}"

	type job struct {
		If     string            `yaml:"if"`
		Needs  yaml.Node         `yaml:"needs"`
		Uses   string            `yaml:"uses"`
		With   map[string]string `yaml:"with"`
		RunsOn yaml.Node         `yaml:"runs-on"`
		Steps  []struct {
			Run string            `yaml:"run"`
			Env map[string]string `yaml:"env"`
		} `yaml:"steps"`
	}

	workflows := map[string]map[string]job{}

	for _, path := range yamlFiles(t, ".github/workflows") {
		var workflow struct {
			Jobs map[string]job `yaml:"jobs"`
		}

		require.NoError(t, yaml.Unmarshal(reporoot.ReadFile(t, path), &workflow), path)

		workflows[path] = workflow.Jobs
	}

	admitted, forwarded := 0, 0

	for signer, runsOn := range callerRunnerSigners {
		file, _, _ := strings.Cut(signer, "/")
		// ${{ fromJSON(inputs['name']) }} is the runner of the input name.
		input := strings.TrimSuffix(strings.TrimPrefix(runsOn, "${{ fromJSON(inputs['"), "']) }}")
		runner := "${{ inputs['" + input + "'] }}"

		for path, jobs := range workflows {
			for name, call := range jobs {
				if call.Uses != "./.github/workflows/"+file {
					continue
				}

				require.Equal(t, runner, call.With[input], "%s/%s passes %s the runner that was admitted", path, name, file)

				admits := false

				// A job that needs one job may write it as a scalar; an
				// admission is one of several, in a list.
				for _, node := range call.Needs.Content {
					needed := node.Value

					admission := jobs[needed]
					if len(admission.Steps) != 1 || !strings.Contains(admission.Steps[0].Run, "forge-ci plan admit-signer-runner") {
						continue
					}

					step := admission.Steps[0]

					require.True(t, githubSigningRunnerAllowed(admission.RunsOn), "%s/%s admits the signer runner; its runs-on must be an approved hosted literal", path, needed)
					require.Contains(t, step.Run, `--`+input+`="$RUNNER_DARWIN_ARM64"`)
					require.Equal(t, runner, step.Env["RUNNER_DARWIN_ARM64"], "%s/%s admits the runner the build runs on", path, needed)
					require.Contains(t, step.Run, `--`+runners.SignerAllowlistInput+`="$SIGNER_RUNS_ON_ALLOWED"`)
					require.Equal(t, allowlist, step.Env["SIGNER_RUNS_ON_ALLOWED"], "%s/%s admits against the administrator's list", path, needed)
					require.Contains(t, step.Run, `--build-stage-plan-json="$BUILD_STAGE_PLAN_JSON"`)

					plan := strings.TrimSuffix(strings.TrimPrefix(step.Env["BUILD_STAGE_PLAN_JSON"], "${{ "), " }}")
					require.True(t, strings.HasPrefix(plan, "inputs['"), "%s/%s reads its plan from an input", path, needed)
					require.Contains(t, call.If, "fromJson("+plan+").targets.", "%s/%s takes its builds from the plan that was admitted", path, name)

					admits = true
				}

				require.True(t, admits, "%s/%s calls %s, whose %s signs on the caller's runner, without needing a job that admits that runner against %s",
					path, name, file, signer, runners.SignerAllowlistInput)

				admitted++
			}
		}
	}

	require.Positive(t, admitted, "no caller of a caller-runner signer found: the guard checks nothing")

	for path, jobs := range workflows {
		// This repository's own pipelines are entry points: they have no
		// caller to take the list from, and keep the default.
		if strings.HasPrefix(filepath.Base(path), "self-") {
			continue
		}

		for name, call := range jobs {
			callee := strings.TrimPrefix(call.Uses, "./")
			if workflows[callee] == nil || !slices.Contains(declaredInputs(t, callee), runners.SignerAllowlistInput) {
				continue
			}

			require.Equal(t, allowlist, call.With[runners.SignerAllowlistInput], "%s/%s forwards its own %s to %s", path, name, runners.SignerAllowlistInput, callee)
			require.Contains(t, declaredInputs(t, path), runners.SignerAllowlistInput, "%s takes the list it forwards", path)

			forwarded++
		}
	}

	require.Positive(t, forwarded, "no forwarded allowlist found: the guard checks nothing")
}

// githubSigningRunnerAllowed is the approved GitHub signer runner policy.
// Literal syntax alone does not rule out a persistent/self-hosted pool. Keep
// this exact policy separate from configurable ordinary/build runner inputs.
func githubSigningRunnerAllowed(runsOn yaml.Node) bool {
	if runsOn.Kind != yaml.ScalarNode || runsOn.Tag != "!!str" {
		return false
	}

	switch runsOn.Value {
	case "ubuntu-26.04", "macos-26":
		return true
	default:
		return false
	}
}

func TestGitHubSigningRunnerPolicy(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, selector string
		allowed        bool
	}{
		{name: "approved Linux", selector: "ubuntu-26.04", allowed: true},
		{name: "approved macOS", selector: "macos-26", allowed: true},
		{name: "quoted Linux", selector: `'ubuntu-26.04'`, allowed: true},
		{name: "quoted macOS", selector: `"macos-26"`, allowed: true},
		{name: "self-hosted", selector: "self-hosted"},
		{name: "arbitrary literal", selector: "persistent-signing-pool"},
		{name: "unapproved hosted Linux", selector: "ubuntu-26.04-arm"},
		{name: "unapproved hosted macOS", selector: "macos-26-intel"},
		{name: "Linux prefix spoof", selector: "ubuntu-26.04-private"},
		{name: "empty", selector: `''`},
		{name: "unset"},
		{name: "null", selector: "null"},
		{name: "array", selector: `["ubuntu-26.04"]`},
		{name: "self-hosted array", selector: `["self-hosted", "ubuntu-26.04"]`},
		{name: "group", selector: `{group: persistent, labels: ubuntu-26.04}`},
		{name: "input expression", selector: `"${{ inputs['runs-on'] }}"`},
		{name: "variable expression", selector: `"${{ vars.SIGNING_RUNNER }}"`},
		{name: "matrix expression", selector: `"${{ matrix.runner }}"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var job struct {
				RunsOn yaml.Node `yaml:"runs-on"`
			}

			require.NoError(t, yaml.Unmarshal([]byte("runs-on: "+tc.selector+"\n"), &job))
			require.Equal(t, tc.allowed, githubSigningRunnerAllowed(job.RunsOn), "selector %q", tc.selector)
		})
	}
}

func holdsSigningMaterial(text string, permissions map[string]string) bool {
	if permissions["id-token"] == "write" {
		return true
	}

	for _, secret := range signingSecrets {
		if strings.Contains(text, "secrets."+secret+" ") || strings.Contains(text, "secrets."+secret+"}") ||
			strings.Contains(text, "secrets['"+secret+"']") {
			return true
		}
	}

	return false
}
