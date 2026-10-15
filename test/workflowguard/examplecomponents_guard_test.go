// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard_test

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// exampleComponent matches an example's include of one of this repository's
// GitLab components, and captures the component's name.
var exampleComponent = regexp.MustCompile(`^[^@\s]+/forge-ci/([a-z0-9-]+)@[^\s]+$`)

type componentSpec struct {
	Spec struct {
		Inputs map[string]struct {
			Default any   `yaml:"default"`
			Options []any `yaml:"options"`
		} `yaml:"inputs"`
	} `yaml:"spec"`
}

// The GitLab examples are what adopters copy, and nothing else checks the
// inputs they pass: GitLab refuses an undeclared or missing input only when
// the pipeline is created. Every example include of a component under
// templates/ passes only inputs its spec declares, every input the spec gives
// no default, and a value its options allow, the GitLab counterpart of
// TestExampleCallsMatchTheCalledWorkflows.
func TestExampleIncludesMatchTheComponentSpecs(t *testing.T) {
	t.Parallel()

	root := reporoot.Path(t)
	specs := map[string]componentSpec{}
	pipelines, includes := 0, 0

	err := filepath.WalkDir(filepath.Join(root, "examples"), func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if d.IsDir() && d.Name() == "gitlab" {
			if _, statErr := os.Stat(filepath.Join(path, ".gitlab-ci.yml")); statErr != nil {
				t.Errorf("%s has no .gitlab-ci.yml: %v", path, statErr)
			}
		}

		if d.IsDir() || d.Name() != ".gitlab-ci.yml" {
			return nil
		}

		pipelines++

		body, err := os.ReadFile(path) //nolint:gosec // repository example fixture.
		require.NoError(t, err)

		var pipeline struct {
			Include []struct {
				Component string         `yaml:"component"`
				Inputs    map[string]any `yaml:"inputs"`
			} `yaml:"include"`
		}
		require.NoError(t, yaml.Unmarshal(body, &pipeline), path)

		for _, include := range pipeline.Include {
			match := exampleComponent.FindStringSubmatch(include.Component)
			if match == nil {
				t.Errorf("%s includes %q, not a component of this repository", path, include.Component)

				continue
			}

			includes++

			spec, known := specs[match[1]]
			if !known {
				spec = loadComponentSpec(t, filepath.Join(root, "templates", match[1]+".yml"))
				specs[match[1]] = spec
			}

			checkExampleInclude(t, path+" include "+match[1], spec, include.Inputs)
		}

		return nil
	})
	require.NoError(t, err)
	require.Positive(t, pipelines, "no examples/**/gitlab/.gitlab-ci.yml found; the guard has drifted")
	require.Positive(t, includes, "no GitLab example includes a component; the guard has drifted")
}

func loadComponentSpec(t *testing.T, path string) componentSpec {
	t.Helper()

	body, err := os.ReadFile(path) //nolint:gosec // repository component.
	require.NoError(t, err, "an example includes a component this repository does not have")

	// The spec is the first YAML document of a component; the jobs follow it.
	var spec componentSpec
	require.NoError(t, yaml.NewDecoder(bytes.NewReader(body)).Decode(&spec), path)
	require.NotEmpty(t, spec.Spec.Inputs, "%s declares no spec inputs", path)

	return spec
}

func checkExampleInclude(t *testing.T, where string, spec componentSpec, inputs map[string]any) {
	t.Helper()

	for name, value := range inputs {
		declared, ok := spec.Spec.Inputs[name]
		if !ok {
			t.Errorf("%s passes an input the component does not declare: %s", where, name)

			continue
		}

		if len(declared.Options) > 0 && !slices.ContainsFunc(declared.Options, func(option any) bool {
			return fmt.Sprint(option) == fmt.Sprint(value)
		}) {
			t.Errorf("%s passes %s=%v, not one of %v", where, name, value, declared.Options)
		}
	}

	for name, declared := range spec.Spec.Inputs {
		if _, passed := inputs[name]; declared.Default == nil && !passed {
			t.Errorf("%s leaves out the required input %s", where, name)
		}
	}
}
