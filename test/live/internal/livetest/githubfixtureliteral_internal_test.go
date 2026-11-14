// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func literalFixtureRefusal(t *testing.T, root, fixtures string, overrides map[string]string) {
	t.Helper()

	// Providerless sentinels at the two actual admission return boundaries. The
	// entry's existing source-order test separately proves token/session/engine
	// effects are after this public check; these counters do not read credentials.
	precredentialEffects := 0
	if err := ValidateGitHubGPGFixtureProfile(t.Context(), root, fixtures, overrides); err == nil {
		precredentialEffects++
	}

	if precredentialEffects != 0 {
		t.Error("unsafe profile crossed actual public precredential admission")
	}

	// Always exercise actual staging too, even if the public check failed.
	// Unexpected success inspects the returned execution tree; extra writers
	// must be refused, never silently filtered out to make a test pass.
	stagedEffects := 0
	refused := false

	func() {
		defer func() {
			if caught := recover(); caught != nil {
				failure, ok := caught.(acceptanceFailure)
				if !ok || !strings.Contains(string(failure), "fixture profile") {
					panic(caught)
				}

				refused = true
			}
		}()

		trial := &GitHubTrial{tb: stopOnFatalTB{t}}
		dir, _ := trial.stageConsumer(root, fixtures, trialEngine, overrides)
		stagedEffects++

		t.Errorf("unsafe staging returned actual execution inventory: %q", strings.TrimSpace(trial.git(dir, "ls-tree", "--name-only", "HEAD:.github/workflows")))
	}()

	if !refused || stagedEffects != 0 {
		t.Fatal("unsafe profile crossed actual native staging boundary")
	}
}

func TestOriginalReviewerGPGExpressionCannotBypassRootBinding(t *testing.T) {
	for _, viaOverride := range []bool{false, true} {
		name := "raw-expression"
		if viaOverride {
			name = "effective-override-expression"
		}

		t.Run(name, func(t *testing.T) {
			root, git := gpgProfileTestSource(t)
			profile := "test/live/github/workflows/"

			body, err := os.ReadFile(filepath.Join(root, profile, "release.yml"))
			if err != nil {
				t.Fatal(err)
			}

			expression := "${{ '.forge-ci/artifacts-gpg.yml' }}"

			var overrides map[string]string

			if viaOverride {
				overrides = map[string]string{"artifacts-config": expression}
			} else {
				body = []byte(strings.Replace(string(body), ".forge-ci/artifacts.yml", expression, 1))
			}

			stageTestWrite(t, root, profile+"release.yml", string(body), 0644)
			stageTestWrite(t, root, profile+"second-release.yml", string(body), 0644)
			stageTestCommit(t, git, root)

			if err := git.requireCleanTop(t.Context(), root); err != nil {
				t.Fatal(err)
			}

			if _, err := os.ReadFile(filepath.Join(root, githubGPGConfig)); err != nil {
				t.Fatal(err)
			}

			// Both actual committed source writers still exist with the original
			// request-tag trigger, regardless of whether the expression is raw.
			for _, workflow := range []string{"release.yml", "second-release.yml"} {
				text, err := git.run(t.Context(), root, "show", "HEAD:"+profile+workflow)
				if err != nil || !strings.Contains(text, `"release-request/v*"`) {
					t.Fatal("second-writer native fixture premise was lost")
				}
			}

			literalFixtureRefusal(t, root, "test/live/github", overrides)
		})
	}
}

func TestGitHubGPGProfileLiteralInputsUseDecodedYAML(t *testing.T) {
	for _, scalar := range []string{
		`${{'.forge-ci/artifacts-gpg.yml'}}`,
		`${{ format('{0}/{1}', '.forge-ci', 'artifacts-gpg.yml') }}`,
		`${{ inputs['config'] }}`,
		`.forge-ci/${{ 'artifacts-gpg' }}.yml`,
		`"${{ '.forge-ci/artifacts-gpg.yml' }}" # quoted is still evaluated`,
		`'${{ ''.forge-ci/artifacts-gpg.yml'' }}'`,
		`"\u0024\u007b\u007b '.forge-ci/artifacts-gpg.yml' }}"`,
		"!!str ${{ '.forge-ci/artifacts-gpg.yml' }}",
		"!profile ${{ '.forge-ci/artifacts-gpg.yml' }}",
		"&dynamic ${{ '.forge-ci/artifacts-gpg.yml' }}",
		">-\n        ${{\n        '.forge-ci/artifacts-gpg.yml'\n        }}",
		"|-\n        ${{ '.forge-ci/artifacts-gpg.yml' }}",
		`$(printf .forge-ci/artifacts-gpg.yml)`,
		`${CONFIG_PATH}`,
		`${{ incomplete`,
		`" .forge-ci/artifacts-gpg.yml "`,
	} {
		t.Run(scalar, func(t *testing.T) {
			root, git := gpgProfileTestSource(t)

			body, err := os.ReadFile(filepath.Join(root, "test/live/github/workflows/release.yml"))
			if err != nil {
				t.Fatal(err)
			}

			// Unknown roots/basenames must not gain approval just because a raw
			// value is unequal to the fixed GPG filename. Both writers stay put.
			body = []byte(strings.Replace(string(body), "artifacts-config: .forge-ci/artifacts.yml", "artifacts-config: "+scalar, 1))
			for _, name := range []string{"release.yml", "second-release.yml"} {
				stageTestWrite(t, root, "custom/profile/workflows/"+name, string(body), 0644)
			}

			stageTestCommit(t, git, root)

			var parsed map[string]any

			if err := yaml.Unmarshal(body, &parsed); err != nil {
				t.Fatalf("negative must exercise valid YAML, not a parser error: %v", err)
			}

			literalFixtureRefusal(t, root, "custom/profile", nil)
		})
	}
}

func TestGitHubGPGProfileExpressionAliasRootAndOverridesRefuse(t *testing.T) {
	for _, scenario := range []string{"yaml-alias", "yaml-merge", "hidden-raw-expression", "unknown-root-override", "dynamic-fixture-root", "dynamic-gpg-root", "step-expression", "step-override-expression", "quoted-key-unused-override"} {
		t.Run(scenario, func(t *testing.T) {
			root, git := gpgProfileTestSource(t)
			fixtures := "test/live/github"

			var overrides map[string]string

			body, err := os.ReadFile(filepath.Join(root, fixtures, "workflows/release.yml"))
			if err != nil {
				t.Fatal(err)
			}

			switch scenario {
			case "yaml-alias":
				body = []byte("config: &dynamic ${{ '.forge-ci/artifacts-gpg.yml' }}\n" + strings.Replace(string(body), "artifacts-config: .forge-ci/artifacts.yml", "artifacts-config: *dynamic", 1))
			case "yaml-merge":
				body = []byte("config: &dynamic\n  artifacts-config: ${{ '.forge-ci/artifacts-gpg.yml' }}\n" + strings.Replace(string(body), "artifacts-config: .forge-ci/artifacts.yml", "<<: *dynamic", 1))
			case "hidden-raw-expression":
				body = []byte(strings.Replace(string(body), ".forge-ci/artifacts.yml", "${{ '.forge-ci/artifacts-gpg.yml' }}", 1))
				overrides = map[string]string{"artifacts-config": ".forge-ci/artifacts.yml"}
			case "quoted-key-unused-override":
				// YAML admits this key, but the existing text override contract
				// cannot replace it. Refuse that unsupported override before effects,
				// rather than waiting for staging's generic unused-input check.
				body = []byte(strings.Replace(string(body), "artifacts-config:", `"artifacts-config":`, 1))
				overrides = map[string]string{"artifacts-config": "${{ '.forge-ci/artifacts-gpg.yml' }}"}
			case "unknown-root-override":
				fixtures = "custom/profile"
				overrides = map[string]string{"artifacts-config": `${{ format('{0}', '.forge-ci/artifacts-gpg.yml') }}`}
			case "dynamic-fixture-root":
				fixtures = "${{ 'test/live/github' }}"
			case "dynamic-gpg-root":
				fixtures = githubGPGFixtures

				body, err = os.ReadFile(filepath.Join(root, fixtures, "workflows/"+githubGPGWorkflow))
				if err != nil {
					t.Fatal(err)
				}

				overrides = map[string]string{"artifacts-config": "${{ '.forge-ci/artifacts-gpg.yml' }}"}
			case "step-expression", "step-override-expression":
				body = []byte("on: {push: {tags: ['release-request/v*']}}\njobs:\n  caller:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: ./action\n        with:\n          artifacts-config: .forge-ci/artifacts.yml\n")
				if scenario == "step-expression" {
					body = []byte(strings.Replace(string(body), ".forge-ci/artifacts.yml", "${{ '.forge-ci/artifacts-gpg.yml' }}", 1))
				} else {
					overrides = map[string]string{"artifacts-config": "${{ '.forge-ci/artifacts-gpg.yml' }}"}
				}
			}

			name := "release.yml"
			if fixtures == githubGPGFixtures {
				name = githubGPGWorkflow
			}

			stageTestWrite(t, root, fixtures+"/workflows/"+name, string(body), 0644)
			stageTestWrite(t, root, "literal-case.txt", scenario, 0644)
			stageTestCommit(t, git, root)
			literalFixtureRefusal(t, root, fixtures, overrides)
		})
	}
}

func TestGitHubGPGProfileLiteralYAMLFormsStageMatchedInventory(t *testing.T) {
	for _, scalar := range []string{
		`.forge-ci/artifacts-gpg.yml # ${{ expressions in comments are not inputs }}`,
		`".forge-ci/artifacts-gpg.yml"`,
		`'.forge-ci/artifacts-gpg.yml'`,
		`".forge-ci/\u0061rtifacts-gpg.yml"`,
		`!!str .forge-ci/artifacts-gpg.yml`,
		">-\n        .forge-ci/artifacts-gpg.yml",
		"|-\n        .forge-ci/artifacts-gpg.yml",
	} {
		t.Run(scalar, func(t *testing.T) {
			root, git := gpgProfileTestSource(t)
			workflow := githubGPGFixtures + "/workflows/" + githubGPGWorkflow

			body, err := os.ReadFile(filepath.Join(root, workflow))
			if err != nil {
				t.Fatal(err)
			}

			stageTestWrite(t, root, workflow, strings.Replace(string(body), "artifacts-config: "+githubGPGConfig, "artifacts-config: "+scalar, 1), 0644)
			stageTestCommit(t, git, root)

			if err := ValidateGitHubGPGFixtureProfile(t.Context(), root, githubGPGFixtures, nil); err != nil {
				t.Fatal(err)
			}

			trial := &GitHubTrial{tb: t}

			dir, _ := trial.stageConsumer(root, githubGPGFixtures, trialEngine, nil)
			if names := strings.TrimSpace(trial.git(dir, "ls-tree", "--name-only", "HEAD:.github/workflows")); names != githubGPGWorkflow {
				t.Fatalf("actual installed execution inventory: %q", names)
			}

			var installed struct {
				Jobs map[string]struct {
					With map[string]any `yaml:"with"`
				} `yaml:"jobs"`
			}

			if err := yaml.Unmarshal([]byte(trial.git(dir, "show", "HEAD:.github/workflows/"+githubGPGWorkflow)), &installed); err != nil {
				t.Fatal(err)
			}

			if installed.Jobs["release"].With["artifacts-config"] != githubGPGConfig {
				t.Fatal("actual installed decoded config differs from the bound GPG profile")
			}
		})
	}
}

func TestOriginalReviewerGPGIsolationClosure(t *testing.T) {
	for _, alias := range []string{"./.forge-ci/artifacts-gpg.yml", ".forge-ci/./artifacts-gpg.yml"} {
		for _, secondWriter := range []bool{true, false} {
			name := alias + "/single"
			if secondWriter {
				name = alias + "/two-writers"
			}

			t.Run(name, func(t *testing.T) {
				root, git := gpgProfileTestSource(t)
				profile := "test/live/github-gpg/workflows/"

				body, err := os.ReadFile(filepath.Join(root, profile, githubGPGWorkflow))
				if err != nil {
					t.Fatal(err)
				}

				stageTestWrite(t, root, profile+githubGPGWorkflow, strings.Replace(string(body), githubGPGConfig, alias, 1), 0644)

				if secondWriter {
					second, err := os.ReadFile(filepath.Join(root, "test/live/github/workflows/release.yml"))
					if err != nil {
						t.Fatal(err)
					}

					stageTestWrite(t, root, profile+"second-release.yml", string(second), 0644)
				}

				stageTestCommit(t, git, root)

				actual, err := os.ReadFile(filepath.Join(root, alias))
				if err != nil {
					t.Fatal(err)
				}

				canonical, err := os.ReadFile(filepath.Join(root, githubGPGConfig))
				if err != nil || !bytes.Equal(actual, canonical) {
					t.Fatal("independent equivalent-path premise was lost")
				}

				literalFixtureRefusal(t, root, githubGPGFixtures, nil)
			})
		}
	}
}

func TestGitHubGPGProfileNonConfigExpressionsStillStageDefault(t *testing.T) {
	root, _ := gpgProfileTestSource(t)
	overrides := map[string]string{"branch": "${{ github.ref_name }}"}

	if err := ValidateGitHubGPGFixtureProfile(t.Context(), root, "test/live/github", overrides); err != nil {
		t.Fatal(err)
	}

	trial := &GitHubTrial{tb: t}
	dir, _ := trial.stageConsumer(root, "test/live/github", trialEngine, overrides)

	if names := strings.TrimSpace(trial.git(dir, "ls-tree", "--name-only", "HEAD:.github/workflows")); names != "pullrequest.yml\nrelease.yml" {
		t.Fatalf("actual default installed inventory: %q", names)
	}

	if body := trial.git(dir, "show", "HEAD:.github/workflows/release.yml"); !strings.Contains(body, "branch: ${{ github.ref_name }}") {
		t.Fatal("admission must not ban or erase expressions outside config/profile inputs")
	}
}
