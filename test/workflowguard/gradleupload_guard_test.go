// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// A Gradle project's buildSrc builds the build itself and is no module of the
// product: the snapshot's file selection skips it, and the release's two
// uploads must leave its jar out too. The patterns are read from the workflow
// and the template and matched against a project that has a buildSrc.
func TestGradleBuildUploadsLeaveOutBuildSrc(t *testing.T) {
	t.Parallel()

	project := []string{
		"build/libs/app.jar",
		"lib/build/libs/lib.jar",
		"buildSrc/build/libs/buildSrc.jar",
		"lib/buildSrc/build/libs/buildSrc.jar",
	}
	product := []string{"build/libs/app.jar", "lib/build/libs/lib.jar"}

	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}

	body, err := os.ReadFile(filepath.Join(reporoot.Path(t), ".github", "workflows", "build-gradle-app.yml"))
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(body, &workflow))

	var github []string

	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if step.Name == "Upload Gradle artifacts" {
				github = strings.Fields(strings.ReplaceAll(step.With["path"], "${{ inputs['working-directory'] }}", "."))
			}
		}
	}

	var include, exclude []string

	for _, pattern := range github {
		if negated, ok := strings.CutPrefix(pattern, "!"); ok {
			exclude = append(exclude, negated)
		} else {
			include = append(include, pattern)
		}
	}

	require.Equal(t, product, uploaded(t, project, include, exclude), "build-gradle-app.yml")

	var template map[string]struct {
		Artifacts struct {
			Paths   []string `yaml:"paths"`
			Exclude []string `yaml:"exclude"`
		} `yaml:"artifacts"`
	}

	// The template's second document holds the job; the first its inputs.
	body, err = os.ReadFile(filepath.Join(reporoot.Path(t), "templates", "build-gradle.yml"))
	require.NoError(t, err)

	_, jobs, found := strings.Cut(string(body), "\n---\n")
	require.True(t, found)
	require.NoError(t, yaml.Unmarshal([]byte(strings.ReplaceAll(jobs, "$[[ inputs.working-directory ]]", ".")), &template))

	gitlab := template["$[[ inputs.job-name ]]"].Artifacts
	include = make([]string, 0, len(gitlab.Paths))

	// GitLab keeps everything below a path that names a directory.
	for _, pattern := range gitlab.Paths {
		if strings.HasSuffix(pattern, "/") {
			pattern += "**"
		}

		include = append(include, pattern)
	}

	require.Equal(t, product, uploaded(t, project, include, gitlab.Exclude), "templates/build-gradle.yml")
}

// uploaded returns the files an upload keeps: those an include pattern
// matches and no exclude pattern does.
func uploaded(t *testing.T, files, include, exclude []string) []string {
	t.Helper()

	matches := func(patterns []string, file string) bool {
		for _, pattern := range patterns {
			ok, err := doublestar.Match(path.Clean(pattern), file)
			require.NoError(t, err)

			if ok {
				return true
			}
		}

		return false
	}

	var kept []string

	for _, file := range files {
		if matches(include, file) && !matches(exclude, file) {
			kept = append(kept, file)
		}
	}

	return kept
}
