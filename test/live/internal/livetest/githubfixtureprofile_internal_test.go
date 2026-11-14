// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"bytes"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func gpgProfileTestSource(t *testing.T) (string, layerGit) {
	t.Helper()

	root, git := stageTestRepo(t, "sha1")
	template := filepath.Join(reporoot.Path(t), "test/live/fixtures/consumers/hello-go-cli/consumer")

	err := filepath.WalkDir(template, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.IsDir() {
			return nil
		}

		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		relative, err := filepath.Rel(template, path)
		if err != nil {
			return err
		}

		stageTestWrite(t, root, relative, string(body), 0644)

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	stageTestCommit(t, git, root)

	return root, git
}

func TestGitHubGPGProfileStagesExactlyOneRelease(t *testing.T) {
	root, git := gpgProfileTestSource(t)

	head, err := git.run(t.Context(), root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	for _, profile := range []struct {
		directory string
		want      string
	}{
		{"test/live/github", "pullrequest.yml\nrelease.yml"},
		{"test/live/github-gpg", "release-gpg.yml"},
	} {
		t.Run(profile.directory, func(t *testing.T) {
			if err := ValidateGitHubGPGFixtureProfile(t.Context(), root, profile.directory, nil); err != nil {
				t.Fatal(err)
			}

			trial := &GitHubTrial{tb: t} // No client, token, session or remote operation.
			dir, _ := trial.stageConsumer(root, profile.directory, trialEngine, nil)

			installed := strings.TrimSpace(trial.git(dir, "ls-tree", "--name-only", "HEAD:.github/workflows"))
			if installed != profile.want {
				t.Fatalf("installed %q; want %q", installed, profile.want)
			}

			publishers := 0

			for name := range strings.SplitSeq(installed, "\n") {
				body, err := os.ReadFile(filepath.Join(dir, ".github/workflows", name))
				if err != nil {
					t.Fatal(err)
				}

				var workflow struct {
					On struct {
						Push struct {
							Tags []string `yaml:"tags"`
						} `yaml:"push"`
					} `yaml:"on"`
				}

				if err := yaml.Unmarshal(body, &workflow); err != nil {
					t.Fatal(err)
				}

				if len(workflow.On.Push.Tags) != 0 {
					publishers++

					if !reflect.DeepEqual(workflow.On.Push.Tags, []string{"release-request/v*"}) {
						t.Fatal("production request trigger changed")
					}
				}
			}

			if publishers != 1 {
				t.Fatalf("installed %d tag-triggered writers, want exactly one", publishers)
			}

			// Unselected fixture source remains data outside the executing path.
			for _, path := range []string{"test/live/github/workflows/release.yml", "test/live/github-gpg/workflows/release-gpg.yml", "cmd/my-cli/main.go", ".forge-ci/artifacts.yml", ".forge-ci/artifacts-gpg.yml"} {
				original, err := git.output(t.Context(), root, nil, "show", head+":"+path)
				if err != nil {
					t.Fatal(err)
				}

				staged, err := os.ReadFile(filepath.Join(dir, path))
				if err != nil {
					t.Fatal(err)
				}

				if !bytes.Equal(staged, original) {
					t.Fatalf("nonexecuting source changed: %s", path)
				}
			}
		})
	}

	if err := git.requireClean(t.Context(), root, "."); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubGPGProfileRefusesAmbiguousCommittedDirectory(t *testing.T) {
	root, git := gpgProfileTestSource(t)

	body, err := os.ReadFile(filepath.Join(root, "test/live/github-gpg/workflows/release-gpg.yml"))
	if err != nil {
		t.Fatal(err)
	}

	// Reproduce the exact old layout without changing its sealed export.
	stageTestWrite(t, root, "test/live/github/workflows/release-gpg.yml", string(body), 0644)
	stageTestCommit(t, git, root)

	if err := ValidateGitHubGPGFixtureProfile(t.Context(), root, "test/live/github", nil); !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("ambiguous profile admitted: %v", err)
	}

	// A worktree-only deletion cannot hide the committed second writer.
	if err = os.Remove(filepath.Join(root, "test/live/github/workflows/release-gpg.yml")); err != nil {
		t.Fatal(err)
	}

	if err := ValidateGitHubGPGFixtureProfile(t.Context(), root, "test/live/github", nil); !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("admission trusted worktree instead of committed blobs: %v", err)
	}

	stageTestWrite(t, root, "test/live/github/workflows/release-gpg.yml", string(body), 0644)

	trial := &GitHubTrial{tb: stopOnFatalTB{t}}

	defer func() {
		caught := recover()
		if failure, ok := caught.(acceptanceFailure); !ok || !strings.Contains(string(failure), "fixture profile") {
			t.Fatalf("native staging did not refuse before push: %v", caught)
		}
	}()

	trial.stageConsumer(root, "test/live/github", trialEngine, nil)
	t.Fatal("ambiguous native staging returned")
}

func TestGitHubGPGProfileKeepsOtherFixtureContracts(t *testing.T) {
	ordinary := "jobs:\n  release:\n    with:\n      artifacts-config: custom.yml\n      matrix-input: [linux, amd64]\n"
	for _, profile := range []struct {
		fixtures  string
		workflows map[string]string
	}{
		{"test/live/github", map[string]string{"release.yml": ordinary, "other-release.yml": ordinary}},
		{githubGPGFixtures, map[string]string{githubGPGWorkflow: "config: &profile .forge-ci/artifacts-gpg.yml\njobs:\n  release:\n    with:\n      artifacts-config: *profile\n"}},
	} {
		workflows := profile.workflows
		before := maps.Clone(workflows)

		if _, err := validateGitHubGPGFixtureWorkflows(profile.fixtures, workflows, nil); err != nil || !reflect.DeepEqual(workflows, before) {
			t.Fatalf("existing contract or source changed: %v", err)
		}
	}

	aliased := "config: &profile .forge-ci/artifacts-gpg.yml\njobs:\n  release:\n    with:\n      artifacts-config: *profile\n"
	if _, err := validateGitHubGPGFixtureWorkflows(githubGPGFixtures, map[string]string{githubGPGWorkflow: aliased, "other.yml": ordinary}, nil); !errors.Is(err, errs.ErrValidation) {
		t.Fatal("alias concealed ambiguous GPG profile")
	}
}

func TestGitHubGPGProfileAdmissionPrecedesEffects(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(reporoot.Path(t), "test/live/githubconsumer/consumertrial_test.go"))
	if err != nil {
		t.Fatal(err)
	}

	entry, _, _ := strings.Cut(string(body), "func runTrial(")
	admission := strings.Index(entry, "livetest.ValidateGitHubGPGFixtureProfile(")

	if !strings.Contains(entry, "root, fixtures, settings.Overrides)") {
		t.Fatal("precredential admission must inspect the actual render overrides")
	}

	for _, effect := range []string{"livetest.GitHubReleaseToken(", "livetest.OpenGitHubTrial(", "trial.EnsureEngine(", "trial.ReuseEngine("} {
		position := strings.Index(entry, effect)
		if admission < 0 || position < 0 || admission >= position {
			t.Fatalf("profile admission must precede %s", effect)
		}
	}
}

func requireGPGStagingRefusal(t *testing.T, root, fixtures string, overrides map[string]string) {
	t.Helper()

	trial := &GitHubTrial{tb: stopOnFatalTB{t}} // No API/client, session, token or key.

	defer func() {
		failure, ok := recover().(acceptanceFailure)
		if !ok || !strings.Contains(string(failure), "fixture profile") {
			t.Fatalf("native staging did not refuse at admission: %v", failure)
		}
	}()

	trial.stageConsumer(root, fixtures, trialEngine, overrides)
	t.Fatal("unsafe installed workflow inventory returned from native staging")
}

// Independent review's highest-severity reproduction: equivalent native-reader
// paths plus a second real same-tag writer. Both admission boundaries must refuse;
// no filtering/deleting the second workflow is an acceptable outcome.
func TestIndependentGPGIsolationEquivalentConfigPath(t *testing.T) {
	for _, alias := range []string{"./.forge-ci/artifacts-gpg.yml", ".forge-ci/./artifacts-gpg.yml"} {
		t.Run(alias, func(t *testing.T) {
			root, git := gpgProfileTestSource(t)
			profile := "test/live/github-gpg/workflows/"

			body, err := os.ReadFile(filepath.Join(root, profile, githubGPGWorkflow))
			if err != nil {
				t.Fatal(err)
			}

			changed := strings.Replace(string(body), githubGPGConfig, alias, 1)
			stageTestWrite(t, root, profile+githubGPGWorkflow, changed, 0644)

			second, err := os.ReadFile(filepath.Join(root, "test/live/github/workflows/release.yml"))
			if err != nil {
				t.Fatal(err)
			}

			stageTestWrite(t, root, profile+"second-release.yml", string(second), 0644)
			stageTestCommit(t, git, root)

			if err := git.requireCleanTop(t.Context(), root); err != nil {
				t.Fatal(err)
			}

			actual, err := os.ReadFile(filepath.Join(root, alias))
			if err != nil {
				t.Fatal(err)
			}

			canonical, err := os.ReadFile(filepath.Join(root, githubGPGConfig))
			if err != nil || !bytes.Equal(actual, canonical) {
				t.Fatal("not an equivalent actual native filesystem config path")
			}

			if err := ValidateGitHubGPGFixtureProfile(t.Context(), root, githubGPGFixtures, nil); err == nil {
				t.Fatal("equivalent path and second same-tag writer admitted before credentials")
			}

			requireGPGStagingRefusal(t, root, githubGPGFixtures, nil)
		})
	}
}

func TestGitHubGPGProfileConfigBindingRefusesNativeAliases(t *testing.T) {
	for _, config := range []string{
		"./.forge-ci/artifacts-gpg.yml", ".forge-ci/./artifacts-gpg.yml",
		".forge-ci//artifacts-gpg.yml", ".forge-ci/../.forge-ci/artifacts-gpg.yml",
		"../outside.yml", "/.forge-ci/artifacts-gpg.yml", `C:\fixture\artifacts-gpg.yml`,
		`.forge-ci\artifacts-gpg.yml`, ".forge-ci/artifacts-gpg.yaml", ".forge-ci/copy.unknown",
		".forge-ci/link.yml", "linked/artifacts-gpg.yml", ".forge-ci/artifacts.yml",
	} {
		t.Run(config, func(t *testing.T) {
			root, git := gpgProfileTestSource(t)
			profilePath := githubGPGFixtures + "/workflows/" + githubGPGWorkflow

			body, err := os.ReadFile(filepath.Join(root, profilePath))
			if err != nil {
				t.Fatal(err)
			}

			canonical, err := os.ReadFile(filepath.Join(root, githubGPGConfig))
			if err != nil {
				t.Fatal(err)
			}

			// Equal contents do not authorize another profile name/extension.
			stageTestWrite(t, root, ".forge-ci/copy.unknown", string(canonical), 0644)
			stageTestWrite(t, root, ".forge-ci/artifacts-gpg.yaml", string(canonical), 0644)

			if err := os.Symlink("artifacts-gpg.yml", filepath.Join(root, ".forge-ci/link.yml")); err != nil {
				t.Fatal(err)
			}

			if err := os.Symlink(".forge-ci", filepath.Join(root, "linked")); err != nil {
				t.Fatal(err)
			}

			for _, alias := range []string{".forge-ci/link.yml", "linked/artifacts-gpg.yml"} {
				actual, err := os.ReadFile(filepath.Join(root, alias))
				if err != nil || !bytes.Equal(actual, canonical) {
					t.Fatal("native filesystem symlink alias not exercised")
				}
			}

			stageTestWrite(t, root, profilePath, strings.Replace(string(body), githubGPGConfig, config, 1), 0644)
			stageTestCommit(t, git, root)

			if err := ValidateGitHubGPGFixtureProfile(t.Context(), root, githubGPGFixtures, nil); err == nil {
				t.Fatal("mismatched/noncanonical config admitted before credentials")
			}

			requireGPGStagingRefusal(t, root, githubGPGFixtures, nil)
		})
	}
}

func TestGitHubGPGProfileDefaultAndOverrideBinding(t *testing.T) {
	for _, scenario := range []struct {
		name, fixtures, config string
		overrides              map[string]string
	}{
		{"canonical-default-mismatch", "test/live/github", githubGPGConfig, nil},
		{"alias-default-mismatch", "test/live/github", ".forge-ci/./artifacts-gpg.yml", nil},
		{"symlink-default-mismatch", "test/live/github", ".forge-ci/link.yml", nil},
		{"directory-symlink-default", "test/live/github", "linked/artifacts-gpg.yml", nil},
		{"override-default-to-gpg", "test/live/github", "", map[string]string{"artifacts-config": githubGPGConfig}},
		{"override-default-alias", "test/live/github", "", map[string]string{"artifacts-config": ".forge-ci//artifacts-gpg.yml"}},
		{"override-gpg-to-default", githubGPGFixtures, "", map[string]string{"artifacts-config": ".forge-ci/artifacts.yml"}},
		{"override-gpg-alias", githubGPGFixtures, "", map[string]string{"artifacts-config": "./.forge-ci/artifacts-gpg.yml"}},
		{"override-cannot-hide-raw", githubGPGFixtures, ".forge-ci/./artifacts-gpg.yml", map[string]string{"artifacts-config": githubGPGConfig}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			root, git := gpgProfileTestSource(t)

			name := "release.yml"
			if scenario.fixtures == githubGPGFixtures {
				name = githubGPGWorkflow
			}

			workflowPath := scenario.fixtures + "/workflows/" + name

			body, err := os.ReadFile(filepath.Join(root, workflowPath))
			if err != nil {
				t.Fatal(err)
			}

			if scenario.config != "" {
				original := ".forge-ci/artifacts.yml"
				if scenario.fixtures == githubGPGFixtures {
					original = githubGPGConfig
				}

				body = []byte(strings.Replace(string(body), original, scenario.config, 1))
			}

			stageTestWrite(t, root, workflowPath, string(body), 0644)

			if err := os.Symlink("artifacts-gpg.yml", filepath.Join(root, ".forge-ci/link.yml")); err != nil {
				t.Fatal(err)
			}

			if err := os.Symlink(".forge-ci", filepath.Join(root, "linked")); err != nil {
				t.Fatal(err)
			}

			stageTestCommit(t, git, root)

			if err := ValidateGitHubGPGFixtureProfile(t.Context(), root, scenario.fixtures, scenario.overrides); err == nil {
				t.Fatal("profile mismatch admitted before credentials")
			}

			requireGPGStagingRefusal(t, root, scenario.fixtures, scenario.overrides)
		})
	}
}

func TestGitHubGPGProfileYAMLAliasAndCanonicalOverrideStage(t *testing.T) {
	root, git := gpgProfileTestSource(t)
	workflowPath := githubGPGFixtures + "/workflows/" + githubGPGWorkflow

	body, err := os.ReadFile(filepath.Join(root, workflowPath))
	if err != nil {
		t.Fatal(err)
	}

	body = []byte("config: &gpg " + githubGPGConfig + "\n" + strings.Replace(string(body), "artifacts-config: "+githubGPGConfig, "artifacts-config: *gpg", 1))
	stageTestWrite(t, root, workflowPath, string(body), 0644)
	stageTestCommit(t, git, root)

	for _, overrides := range []map[string]string{nil, {"artifacts-config": githubGPGConfig}} {
		if err := ValidateGitHubGPGFixtureProfile(t.Context(), root, githubGPGFixtures, overrides); err != nil {
			t.Fatal(err)
		}

		trial := &GitHubTrial{tb: t}
		dir, _ := trial.stageConsumer(root, githubGPGFixtures, trialEngine, overrides)

		if installed := strings.TrimSpace(trial.git(dir, "ls-tree", "--name-only", "HEAD:.github/workflows")); installed != githubGPGWorkflow {
			t.Fatalf("actual installed set: %q", installed)
		}

		installedBody := trial.git(dir, "show", "HEAD:.github/workflows/"+githubGPGWorkflow)

		var installedWorkflow struct {
			Jobs map[string]struct {
				With map[string]any `yaml:"with"`
			} `yaml:"jobs"`
		}

		if err := yaml.Unmarshal([]byte(installedBody), &installedWorkflow); err != nil {
			t.Fatal(err)
		}

		selected := 0

		for _, job := range installedWorkflow.Jobs {
			if value, exists := job.With["artifacts-config"]; exists {
				if value != githubGPGConfig {
					t.Fatalf("actual installed config %v is not the admitted profile", value)
				}

				selected++
			}
		}

		if selected != 1 {
			t.Fatalf("actual installed GPG config selectors: %d", selected)
		}
	}
}

func TestGitHubGPGProfileRootAndRegularConfigBinding(t *testing.T) {
	for _, scenario := range []string{"fixture-dot", "fixture-slash", "wrong-basename", "canonical-config-symlink"} {
		t.Run(scenario, func(t *testing.T) {
			root, git := gpgProfileTestSource(t)
			fixtures := githubGPGFixtures

			switch scenario {
			case "fixture-dot":
				fixtures = "test/live/./github-gpg"
			case "fixture-slash":
				fixtures = "test/live//github-gpg"
			case "wrong-basename":
				directory := filepath.Join(root, githubGPGFixtures, "workflows")
				if err := os.Rename(filepath.Join(directory, githubGPGWorkflow), filepath.Join(directory, "other.yml")); err != nil {
					t.Fatal(err)
				}
			case "canonical-config-symlink":
				config := filepath.Join(root, githubGPGConfig)
				if err := os.Rename(config, config+".real"); err != nil {
					t.Fatal(err)
				}

				if err := os.Symlink("artifacts-gpg.yml.real", config); err != nil {
					t.Fatal(err)
				}
			}

			stageTestWrite(t, root, "binding-case.txt", scenario, 0644)
			stageTestCommit(t, git, root)

			if err := ValidateGitHubGPGFixtureProfile(t.Context(), root, fixtures, nil); err == nil {
				t.Fatal("root/basename/regular-blob mismatch admitted before credentials")
			}

			requireGPGStagingRefusal(t, root, fixtures, nil)
		})
	}
}
