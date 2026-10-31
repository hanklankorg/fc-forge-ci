// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func consumerPRTestWorkflow(body string) error {
	var workflow struct {
		Jobs map[string]struct {
			Uses  string `yaml:"uses"`
			Steps []any  `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(body), &workflow); err != nil {
		return err
	}
	if len(workflow.Jobs) != 1 {
		return errors.New("unit baseline requires exactly one original test job")
	}
	for _, job := range workflow.Jobs {
		if job.Uses != "" || len(job.Steps) == 0 {
			return errors.New("unit baseline requires an original test job with steps")
		}
	}
	return nil
}

func TestConsumerPRTestWorkflow(t *testing.T) {
	for _, tc := range []struct {
		body string
		ok   bool
	}{
		{`jobs: {test: {name: 'Test (${{ matrix.java }})', steps: [{run: mvn test}]}}`, true},
		{`jobs: {a: {steps: [{run: mvn test}]}, b: {steps: [{run: mvn test}]}}`, false},
		{`jobs: {test: {uses: ./.github/workflows/test.yml}}`, false},
	} {
		if err := consumerPRTestWorkflow(tc.body); (err == nil) != tc.ok {
			t.Fatalf("workflow admission: ok=%t err=%v", tc.ok, err)
		}
	}
}

// consumerPRFixture is a unit-check overlay, not an engine release-flow plan.
// PublicTools is an explicit admission: private tools require origin-scoped
// authentication and must never silently run through the public installer.
type consumerPRFixture struct {
	Runner, Action, Digest string
	PublicTools            bool
	Distros                []consumerPRDistro
	Prerequisites, Checks  string
}

type consumerPRDistro struct{ Name, Image, JustVersion string }

// Decode only the bounded fixture knobs, keeping the trial's engine pin and
// contract runner outside caller-controlled JSON.
func configureConsumerPRFixture(f consumerPRFixture, input string) (consumerPRFixture, error) {
	var config struct {
		PublicTools           bool
		Distros               []consumerPRDistro
		Prerequisites, Checks string
	}
	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return f, fmt.Errorf("invalid consumer PR fixture configuration")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return f, fmt.Errorf("consumer PR fixture must contain exactly one configuration")
	}
	f.PublicTools, f.Distros = config.PublicTools, config.Distros
	f.Prerequisites, f.Checks = config.Prerequisites, config.Checks
	return f, nil
}

func consumerPRSettings(input string) (TrialSettings, error) {
	var words []string
	for word := range strings.SplitSeq(input, "\n") {
		if word != "" {
			words = append(words, word)
		}
	}
	settings, err := ParseTrialSettings(TrialPullRequest, words)
	if err == nil && len(settings.TokenSecrets) != 0 {
		return settings, fmt.Errorf("unit fixtures do not bind consumer secrets")
	}
	return settings, err
}

func publicShellPRFixture(runner, action, digest string) consumerPRFixture {
	return consumerPRFixture{
		Runner: runner, Action: action, Digest: digest, PublicTools: true,
		Distros: []consumerPRDistro{
			{"ubuntu", "docker.io/library/ubuntu:26.04", "1.58.0"},
			{"fedora", "docker.io/library/fedora:46@sha256:a9bab18d01cf2c2cf62f3e79c72623405bce14ac062995cc6651a3073c802e41", "1.59.0"},
		},
		Prerequisites: `set -euo pipefail
if command -v apt-get >/dev/null; then
  apt-get update
  apt-get install -y git curl ca-certificates pipx fish jq gettext diffutils unzip xz-utils nodejs npm
else
  dnf install -y git curl ca-certificates just pipx fish gettext jq diffutils unzip xz nodejs
fi
node --version`,
		Checks: "set -euo pipefail\ntest \"$(uname -m)\" = x86_64\njust test-setup\njust test",
	}
}

func consumerPRWorkflow(f consumerPRFixture) (string, error) {
	if !f.PublicTools {
		return "", fmt.Errorf("public-only Mise fixture requires explicit public tool admission; private tools need origin-scoped authentication")
	}
	if f.Runner == "" || f.Action == "" || f.Digest == "" || len(f.Distros) == 0 || len(f.Distros) > 2 || f.Prerequisites == "" || f.Checks == "" {
		return "", fmt.Errorf("incomplete consumer PR fixture (one or two distros required)")
	}
	var matrix strings.Builder
	for _, distro := range f.Distros {
		if distro.Name == "" || distro.Image == "" || distro.JustVersion == "" {
			return "", fmt.Errorf("incomplete consumer distro")
		}
		fmt.Fprintf(&matrix, "          - distro: %q\n            image: %q\n            just-version: %q\n", distro.Name, distro.Image, distro.JustVersion)
	}
	indent := func(s string) string {
		return "          " + strings.ReplaceAll(strings.TrimSuffix(s, "\n"), "\n", "\n          ")
	}
	return fmt.Sprintf(`name: Consumer PR unit checks
on:
  pull_request:
    branches: [main]
jobs:
  units:
    runs-on: %q
    strategy:
      fail-fast: false
      matrix:
        include:
%s
    container:
      image: ${{ matrix.image }}
    steps:
      - name: Original distro prerequisites
        shell: bash
        run: |
%s
      - uses: %q
        with:
          expected-sha256: %q
      - name: Actual pull request checkout through current engine
        shell: bash
        run: forge-ci platform checkout --depth 1
      - uses: https://github.com/jdx/mise-action@08ae2a091c038e1e05ec6f5115dc5c69d5a2a6b8
        with:
          install: false
          github_token: ''
      - name: Install all original public tools
        shell: bash
        run: |
          set -euo pipefail
          # Public-only fixture: the local forge job token has no authority at
          # Codeberg or GitHub. Remove it only from these tool child processes.
          env -u FORGEJO_TOKEN -u MISE_FORGEJO_TOKEN -u GITHUB_TOKEN -u MISE_GITHUB_TOKEN mise install
      - name: Install original CI Just selection
        shell: bash
        env:
          JUST_VERSION: ${{ matrix.just-version }}
        run: |
          set -euo pipefail
          env -u FORGEJO_TOKEN -u MISE_FORGEJO_TOKEN -u GITHUB_TOKEN -u MISE_GITHUB_TOKEN mise install "aqua:casey/just@${JUST_VERSION}"
          env -u FORGEJO_TOKEN -u MISE_FORGEJO_TOKEN -u GITHUB_TOKEN -u MISE_GITHUB_TOKEN mise bin-paths "aqua:casey/just@${JUST_VERSION}" >>"${FORGEJO_PATH:-${GITHUB_PATH:?}}"
      - name: Original Bats setup and tests
        shell: bash
        env:
          CONSUMER_PR_JUST_VERSION: ${{ matrix.just-version }}
        run: |
          set -euo pipefail
          # bin-paths is newline-delimited. Use physical binaries for both Just
          # and Bats so child tests outside this checkout need no Mise config.
          just_paths=$(env -u FORGEJO_TOKEN -u MISE_FORGEJO_TOKEN -u GITHUB_TOKEN -u MISE_GITHUB_TOKEN mise bin-paths "aqua:casey/just@${CONSUMER_PR_JUST_VERSION:?}" | paste -sd:)
          tool_paths=$(env -u FORGEJO_TOKEN -u MISE_FORGEJO_TOKEN -u GITHUB_TOKEN -u MISE_GITHUB_TOKEN mise bin-paths | paste -sd:)
          export PATH="${just_paths}:${tool_paths}:${PATH}"
%s
`, f.Runner, strings.TrimSuffix(matrix.String(), "\n"), indent(f.Prerequisites), f.Action, f.Digest, indent(f.Checks)), nil
}

func TestConsumerPRWorkflow(t *testing.T) {
	for _, variant := range []struct{ runner, origin, namespace, digest string }{
		{"linux", "https://forge.example", "fixtures/engine", strings.Repeat("a", 64)},
		{"runner: isolated", "https://forge.other.example", "another/engine", strings.Repeat("b", 64)},
	} {
		t.Run(variant.namespace, func(t *testing.T) {
			action := variant.origin + "/" + variant.namespace + "/.forgejo/actions/setup-forge-ci@" + strings.Repeat("c", 40)
			workflow, err := consumerPRWorkflow(publicShellPRFixture(variant.runner, action, variant.digest))
			if err != nil {
				t.Fatal(err)
			}

			var doc struct {
				Permissions any `yaml:"permissions"`
				Jobs        map[string]struct {
					Runner   string `yaml:"runs-on"`
					Strategy struct {
						Matrix struct {
							Include []struct {
								Distro  string `yaml:"distro"`
								Version string `yaml:"just-version"`
							} `yaml:"include"`
						} `yaml:"matrix"`
					} `yaml:"strategy"`
					Steps []struct {
						Uses string            `yaml:"uses"`
						With map[string]string `yaml:"with"`
						Run  string            `yaml:"run"`
					} `yaml:"steps"`
				} `yaml:"jobs"`
			}
			if err := yaml.Unmarshal([]byte(workflow), &doc); err != nil {
				t.Fatal(err)
			}

			job := doc.Jobs["units"]
			// Unlike GitHub's hosted execution, Forgejo invokes a JavaScript
			// action with the job container's own Node executable. Both distro
			// branches must provide it before the first action is entered.
			if len(job.Steps) == 0 || job.Steps[0].Uses != "" {
				t.Fatal("container prerequisites must precede actions")
			}
			for _, install := range []string{"apt-get install", "dnf install"} {
				found := false
				for line := range strings.SplitSeq(job.Steps[0].Run, "\n") {
					if strings.Contains(line, install) && strings.Contains(" "+line+" ", " nodejs ") {
						found = true
					}
				}
				if !found {
					t.Fatalf("%s must supply Node before JavaScript actions", install)
				}
			}
			if !strings.Contains(job.Steps[0].Run, "node --version") {
				t.Fatal("container must prove Node is executable before actions")
			}
			if doc.Permissions != nil || job.Runner != variant.runner {
				t.Fatal("ignored permissions or altered runner")
			}

			if len(job.Strategy.Matrix.Include) != 2 {
				t.Fatal("expected both original distros")
			}

			for i, want := range []struct{ distro, version string }{{"ubuntu", "1.58.0"}, {"fedora", "1.59.0"}} {
				got := job.Strategy.Matrix.Include[i]
				if got.Distro != want.distro || got.Version != want.version {
					t.Fatalf("original CI selection changed: %+v", got)
				}
			}

			var engineActions, miseActions int

			for _, step := range job.Steps {
				if step.Uses == "" {
					continue
				}

				if step.Uses == action {
					engineActions++

					if step.With["expected-sha256"] != variant.digest {
						t.Fatal("caller integrity pin changed")
					}

					continue
				}

				if step.Uses != "https://github.com/jdx/mise-action@08ae2a091c038e1e05ec6f5115dc5c69d5a2a6b8" {
					t.Fatalf("unexpected remote action origin or dependency: %s", step.Uses)
				}

				miseActions++
				if step.With["install"] != "false" {
					t.Fatal("public tools must install in the credential-isolated child")
				}
				if token, explicit := step.With["github_token"]; !explicit || token != "" {
					t.Fatal("local forge job token must not become a GitHub credential")
				}
			}

			if engineActions != 1 || miseActions != 1 {
				t.Fatal("expected exactly one pinned engine setup and one pinned Mise setup")
			}

			for _, required := range []string{
				"mise install \"aqua:casey/just@${JUST_VERSION}\"",
				"mise bin-paths \"aqua:casey/just@${JUST_VERSION}\" >>\"${FORGEJO_PATH:-${GITHUB_PATH:?}}\"",
				"forge-ci platform checkout --depth 1", "just test-setup\n          just test",
			} {
				if !strings.Contains(workflow, required) {
					t.Errorf("missing original check or pinned tool plumbing: %s", required)
				}
			}
		})
	}
}

func TestConsumerPRFixtureVariation(t *testing.T) {
	f := publicShellPRFixture("runner: \"quoted\"", "https://other.example/scratch/engine@commit", "digest")
	f.Distros = []consumerPRDistro{{"alternate: distro", "registry.example/image:42", "9.8.7"}}
	f.Checks = "set -euo pipefail\nprintf '%s\\n' \"quoted: check\"\njust verify"
	workflow, err := consumerPRWorkflow(f)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Jobs map[string]struct {
			Runner string `yaml:"runs-on"`
			Steps  []struct {
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(workflow), &doc); err != nil {
		t.Fatal(err)
	}
	job := doc.Jobs["units"]
	if job.Runner != f.Runner || strings.TrimSpace(job.Steps[len(job.Steps)-1].Run) != f.Checks || !strings.Contains(workflow, `just-version: "9.8.7"`) {
		t.Fatal("fixture values or shell quoting changed")
	}
	f.PublicTools = false
	if _, err := consumerPRWorkflow(f); err == nil {
		t.Fatal("private toolchain admitted without origin authentication")
	}
	if _, err := configureConsumerPRFixture(f, `{"Action":"untrusted override"}`); err == nil {
		t.Fatal("engine origin must not be caller-overridable")
	}
	configured, err := configureConsumerPRFixture(f, `{"PublicTools":true,"Distros":[{"Name":"custom","Image":"registry.example/custom:7","JustVersion":"2.0.0"}],"Prerequisites":"node --version","Checks":"just verify"}`)
	if err != nil {
		t.Fatal(err)
	}
	if configured.Action != f.Action || configured.Runner != f.Runner || configured.Distros[0].Image != "registry.example/custom:7" || configured.Checks != "just verify" {
		t.Fatal("typed configuration changed trial-owned source or lost fixture settings")
	}
	if _, err := consumerPRWorkflow(configured); err != nil {
		t.Fatal(err)
	}
	if _, err := configureConsumerPRFixture(f, `{} {}`); err == nil {
		t.Fatal("trailing fixture configuration accepted")
	}
	settings, err := consumerPRSettings("\nworkflow=checks.yml\nCHECK_MODE=two words\n")
	if err != nil || settings.Workflow != "checks.yml" || settings.Variables["CHECK_MODE"] != "two words" {
		t.Fatal("consumer settings must preserve literal values and ignore empty lines")
	}
}
