// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// migrationPRWorkflow preserves the consumer's unit checks and tool selections,
// while adapting only their checkout and tool installation to Forgejo.
func migrationPRWorkflow(runner, action, digest string) string {
	return fmt.Sprintf(`name: Migration shell PR unit pilot
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
          - distro: ubuntu
            image: docker.io/library/ubuntu:26.04
            just-version: 1.58.0
          - distro: fedora
            image: docker.io/library/fedora:46@sha256:a9bab18d01cf2c2cf62f3e79c72623405bce14ac062995cc6651a3073c802e41
            just-version: 1.59.0
    container:
      image: ${{ matrix.image }}
    steps:
      - name: Original distro prerequisites
        shell: bash
        run: |
          set -euo pipefail
          if command -v apt-get >/dev/null; then
            apt-get update
            apt-get install -y git curl ca-certificates pipx fish jq gettext diffutils unzip xz-utils nodejs
          else
            dnf install -y git curl ca-certificates just pipx fish gettext jq diffutils unzip xz nodejs
          fi
          node --version
      - uses: %q
        with:
          expected-sha256: %q
      - name: Actual pull request checkout through current engine
        shell: bash
        run: forge-ci platform checkout --depth 1
      - uses: https://github.com/jdx/mise-action@08ae2a091c038e1e05ec6f5115dc5c69d5a2a6b8
      - name: Install original CI Just selection
        shell: bash
        env:
          JUST_VERSION: ${{ matrix.just-version }}
        run: |
          set -euo pipefail
          mise install "aqua:casey/just@${JUST_VERSION}"
          mise bin-paths "aqua:casey/just@${JUST_VERSION}" >>"${FORGEJO_PATH:-${GITHUB_PATH:?}}"
      - name: Original Bats setup and tests
        shell: bash
        run: |
          set -euo pipefail
          test "$(uname -m)" = x86_64
          just test-setup
          just test
`, runner, action, digest)
}

func TestMigrationPRWorkflow(t *testing.T) {
	for _, variant := range []struct{ runner, origin, namespace, digest string }{
		{"linux", "https://forge.example", "fixtures/engine", strings.Repeat("a", 64)},
		{"runner: isolated", "https://forge.other.example", "another/engine", strings.Repeat("b", 64)},
	} {
		t.Run(variant.namespace, func(t *testing.T) {
			action := variant.origin + "/" + variant.namespace + "/.forgejo/actions/setup-forge-ci@" + strings.Repeat("c", 40)
			workflow := migrationPRWorkflow(variant.runner, action, variant.digest)

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
