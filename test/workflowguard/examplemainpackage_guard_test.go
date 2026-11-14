// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// A Go example built ./cmd/<name> in its releases and the module root in its
// pull requests, so a pull request passed on a package the release never
// built. Every build-go call of an example, the Forgejo action steps, the
// GitLab component includes and the artifact-first configuration, names
// main-package, and all name the same one.
func TestExampleGoBuildsNameOneMainPackage(t *testing.T) {
	t.Parallel()

	examples, err := filepath.Glob(filepath.Join(reporoot.Path(t), "examples", "*"))
	require.NoError(t, err)

	checked := 0

	for _, example := range examples {
		packages := map[string][]string{} // main-package -> where it is named
		name := func(where, value string) {
			if value == "" {
				t.Errorf("%s calls build-go without main-package", where)

				return
			}

			packages[value] = append(packages[value], where)
		}

		forgejo, err := filepath.Glob(filepath.Join(example, "forgejo", "*.yml"))
		require.NoError(t, err)

		for _, path := range forgejo {
			var workflow struct {
				Jobs map[string]struct {
					Steps []struct {
						Uses string         `yaml:"uses"`
						With map[string]any `yaml:"with"`
					} `yaml:"steps"`
				} `yaml:"jobs"`
			}
			readYAML(t, path, &workflow)

			for job, body := range workflow.Jobs {
				for _, step := range body.Steps {
					if strings.Contains(step.Uses, "/actions/build-go@") {
						name(path+" job "+job, stringValue(step.With["main-package"]))
					}
				}
			}
		}

		if gitlab := filepath.Join(example, "gitlab", ".gitlab-ci.yml"); fileExists(gitlab) {
			var pipeline struct {
				Include []struct {
					Component string         `yaml:"component"`
					Inputs    map[string]any `yaml:"inputs"`
				} `yaml:"include"`
			}
			readYAML(t, gitlab, &pipeline)

			for _, include := range pipeline.Include {
				if match := exampleComponent.FindStringSubmatch(include.Component); match != nil && match[1] == "build-go" {
					name(gitlab, stringValue(include.Inputs["main-package"]))
				}
			}
		}

		// Container-first Go builds compile in their Containerfile and
		// name no main-package; only artifact-first ones call build-go.
		configs, err := filepath.Glob(filepath.Join(example, "artifacts*.yml"))
		require.NoError(t, err)

		for _, path := range configs {
			var config struct {
				Artifacts []struct {
					ProjectType string `yaml:"project-type"`
					Config      struct {
						BuildMode   string `yaml:"build-mode"`
						MainPackage string `yaml:"main-package"`
					} `yaml:"config"`
				} `yaml:"artifacts"`
			}
			readYAML(t, path, &config)

			for _, artifact := range config.Artifacts {
				if artifact.ProjectType == "go" && artifact.Config.BuildMode == "artifact-first" {
					name(path, artifact.Config.MainPackage)
				}
			}
		}

		if len(packages) > 1 {
			t.Errorf("%s builds different Go main packages: %v", filepath.Base(example), packages)
		}

		checked += len(packages)
	}

	require.Positive(t, checked, "no example calls build-go; the guard checks nothing")
}

func readYAML(t *testing.T, path string, into any) {
	t.Helper()

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(body, into), path)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)

	return err == nil
}

func stringValue(value any) string {
	text, _ := value.(string)

	return text
}
