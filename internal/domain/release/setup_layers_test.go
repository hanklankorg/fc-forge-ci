// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/release"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"gopkg.in/yaml.v3"
)

func TestSetupGitHubNamesAndSigningConditionsMatchWorkflow(t *testing.T) {
	t.Parallel()
	root := reporoot.Path(t)

	body, err := os.ReadFile(filepath.Join(root, ".github/workflows/release-orchestrator.yml"))
	if err != nil {
		t.Fatal(err)
	}

	var workflow struct {
		On struct {
			Call struct {
				Secrets map[string]yaml.Node `yaml:"secrets"`
			} `yaml:"workflow_call"`
		} `yaml:"on"`
	}
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		t.Fatal(err)
	}

	for _, item := range release.GitHubReleaseSetup(release.SetupRequired, release.SetupRequired, release.SetupRequired, release.SetupRequired, release.SetupRequired, release.SetupRequired) {
		if item.State == release.SetupAutomatic {
			continue
		}

		if _, ok := workflow.On.Call.Secrets[item.Name]; !ok {
			t.Fatalf("modeled input no longer declared: %s", item.Name)
		}
	}

	preflight, err := os.ReadFile(filepath.Join(root, ".github/workflows/validate-release-prerequisites.yml"))
	if err != nil {
		t.Fatal(err)
	}

	for _, gate := range []string{
		"inputs['sign-artifacts'] && fromJSON(inputs['config-plan-json']).sign.imports_gpg_key",
		"fromJSON(inputs['config-plan-json']).git_signing.imports_gpg_key",
	} {
		if !strings.Contains(string(preflight), gate) {
			t.Fatalf("review setup signing rule after preflight change: %s", gate)
		}
	}
}

func TestSetupGitLabEnvironmentScopesAndRegistryPreflight(t *testing.T) {
	t.Parallel()
	root := reporoot.Path(t)

	body, err := os.ReadFile(filepath.Join(root, "templates/release-jobs.yml"))
	if err != nil {
		t.Fatal(err)
	}

	decoder := yaml.NewDecoder(strings.NewReader(string(body)))

	var spec yaml.Node

	type job struct {
		Environment struct {
			Name string `yaml:"name"`
		} `yaml:"environment"`
		Script []string `yaml:"script"`
	}

	var nodes map[string]yaml.Node

	jobs := map[string]job{}

	if err := decoder.Decode(&spec); err != nil {
		t.Fatal(err)
	}

	if err := decoder.Decode(&nodes); err != nil {
		t.Fatal(err)
	}

	for name, node := range nodes {
		if node.Kind != yaml.MappingNode {
			continue
		}

		var parsed job
		if err := node.Decode(&parsed); err != nil {
			t.Fatal(err)
		}

		jobs[name] = parsed
	}

	for job, scope := range map[string]string{
		"Check: signing access":    "release/registry/signing",
		"Check: publishing access": "release/publish",
		"Check: registry access":   "release/registry/preflight",
	} {
		if jobs[job].Environment.Name != scope {
			t.Fatalf("review modeled scope for %s: %q", job, jobs[job].Environment.Name)
		}
	}

	if !strings.Contains(strings.Join(jobs["Check: registry access"].Script, "\n"), "${RELEASE_REGISTRY_TOKEN:?}") {
		t.Fatal("review unconditional registry-token requirement")
	}

	shell, err := os.ReadFile(filepath.Join(root, ".gitlab/bin/release.sh"))
	if err != nil {
		t.Fatal(err)
	}

	for _, item := range release.KeyReleaseSetup("gitlab", true, release.SetupRequired, release.SetupUnused, release.SetupRequired) {
		if !strings.Contains(string(shell), item.Name) && !strings.Contains(string(body), item.Name) {
			t.Fatalf("modeled GitLab credential disappeared: %s", item.Name)
		}
	}

	if !strings.Contains(string(shell), `if [ "${RC_SIGN_DISTRO_PACKAGES}" = true ]; then`) {
		t.Fatal("review package-signing requirement gate")
	}
}
