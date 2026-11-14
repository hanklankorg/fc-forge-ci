// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package changelog_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/changelog"
	"github.com/diggsweden/forge-ci/internal/testutil/evidence"
	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
	"gopkg.in/yaml.v3"
)

func TestPrereleaseAndFinalNotesUsePreviousStableBoundary(t *testing.T) {
	for _, backend := range []string{"git-cliff", "git-chglog"} {
		t.Run(backend, func(t *testing.T) {
			// git-chglog builds without version metadata can treat --version
			// as a render request before the fixture config exists. The real
			// rendering assertions below still verify backend behavior.
			binary := evidence.RequireWorkingTool(t, backend, "--help")

			root := t.TempDir()
			t.Chdir(root)

			sequence := 0
			git := func(args ...string) {
				t.Helper()

				sequence++
				cmd := exec.CommandContext(t.Context(), "git", args...)

				cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid", "GIT_AUTHOR_DATE="+fmt.Sprintf("2026-01-01T00:00:%02dZ", sequence), "GIT_COMMITTER_DATE="+fmt.Sprintf("2026-01-01T00:00:%02dZ", sequence))
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, output)
				}
			}
			git("init", "-q")
			git("commit", "--allow-empty", "-qm", "feat: BASELINE_CHANGE")
			git("tag", "v0.9.0")
			git("commit", "--allow-empty", "-qm", "feat: FIRST_BETA_CHANGE")
			git("tag", "v1.0.0-beta.1")
			git("commit", "--allow-empty", "-qm", "fix: SECOND_BETA_CHANGE")
			git("tag", "v1.0.0-beta.2")
			git("commit", "--allow-empty", "-qm", "fix: AFTER_SECOND_BETA")

			config := filepath.Join(root, "config.toml")
			body := "[changelog]\nbody = \"\"\"{{ version }}\\n{% for commit in commits %}{{ commit.message }}\\n{% endfor %}\"\"\"\n[git]\nconventional_commits = false\nfilter_unconventional = false\n"

			if backend == "git-chglog" {
				config = filepath.Join(root, "config.yml")
				body = "style: github\ntemplate: CHANGELOG.tpl\ninfo:\n  title: Changes\noptions:\n  header:\n    pattern: '^(\\w+): (.*)$'\n    pattern_maps: [Type, Subject]\n  commit_groups:\n    group_by: Type\n"

				if err := os.WriteFile(filepath.Join(root, "CHANGELOG.tpl"), []byte("{{range .Versions}}{{.Tag.Name}}\n{{range .CommitGroups}}{{range .Commits}}{{.Subject}}\n{{end}}{{end}}{{end}}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}

			renderer := &changelog.Renderer{GitCliffBin: binary, GitChglogBin: binary}
			for _, tag := range []string{"v1.0.0-beta.3", "v1.0.0"} {
				notes, err := renderer.RenderBody(t.Context(), backend, config, tag, "https://example.invalid/owner/repo")
				if err != nil {
					t.Fatal(err)
				}

				for _, change := range []string{"FIRST_BETA_CHANGE", "SECOND_BETA_CHANGE", "AFTER_SECOND_BETA"} {
					if !strings.Contains(notes, change) {
						t.Errorf("%s omits %s: %s", tag, change, notes)
					}
				}

				if strings.Contains(notes, "BASELINE_CHANGE") {
					t.Errorf("%s includes previous stable history: %s", tag, notes)
				}

				if backend == "git-cliff" {
					git("tag", tag)

					mock := mockbinary.New(t)
					mock.Add("forge-ci", "exit 0") // Version admission is tested by the engine tests.
					mock.OnPath()

					output := filepath.Join(root, "release-notes.md")
					cmd := exec.CommandContext(t.Context(), testenv.GNUExecutable(t, "bash"), "-euo", "pipefail", "-c", releaseNotesWorkflowScript(t))

					cmd.Env = append(os.Environ(), "CONFIG="+config, "ARGS=--latest", "OUTPUT="+output, "GITHUB_REPO=owner/repo", "REF_TYPE=tag", "REF_NAME=release-request/"+tag, "RELEASE_POLICY=stable-prerelease")

					log, err := cmd.CombinedOutput()
					if err != nil {
						t.Fatalf("post-tag release notes: %v\n%s", err, log)
					}

					rendered, err := os.ReadFile(output)
					if err != nil {
						t.Fatal(err)
					}

					for _, change := range []string{"FIRST_BETA_CHANGE", "SECOND_BETA_CHANGE", "AFTER_SECOND_BETA"} {
						if !strings.Contains(string(rendered), change) {
							t.Errorf("published %s omits %s: %s", tag, change, rendered)
						}
					}

					if strings.Contains(string(rendered), "BASELINE_CHANGE") {
						t.Errorf("published %s includes old stable history: %s", tag, rendered)
					}

					git("tag", "-d", tag)
				}
			}

			git("tag", "-d", "v0.9.0")

			for _, tag := range []string{"v1.0.0-beta.4", "v1.0.0"} {
				notes, err := renderer.RenderBody(t.Context(), backend, config, tag, "https://example.invalid/owner/repo")
				if err != nil {
					t.Fatal(err)
				}

				for _, change := range []string{"BASELINE_CHANGE", "FIRST_BETA_CHANGE", "SECOND_BETA_CHANGE", "AFTER_SECOND_BETA"} {
					if !strings.Contains(notes, change) {
						t.Errorf("first release %s omits %s: %s", tag, change, notes)
					}
				}
			}
		})
	}
}

func releaseNotesWorkflowScript(t *testing.T) string {
	t.Helper()

	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(reporoot.ReadFile(t, ".github/workflows/generate-changelog.yml"), &workflow); err != nil {
		t.Fatal(err)
	}

	for _, step := range workflow.Jobs["generate-changelog"].Steps {
		if step.Name == "Generate Changelog with git-cliff" {
			return step.Run
		}
	}

	t.Fatal("release-notes workflow step missing")

	return ""
}
