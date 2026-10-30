// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

var minimalPreparationWorkflows = []string{
	"version-bump.yml",
	"release-prepare-stage.yml",
	"generate-changelog.yml",
	"resolve-forge-ci-bootstrap.yml",
}

func TestMinimalPreparationSnapshotIncludesLocalWorkflowDependencies(t *testing.T) {
	t.Parallel()

	for _, name := range minimalPreparationWorkflows {
		body, err := os.ReadFile(filepath.Join(reporoot.Path(t), ".github/workflows", name))
		if err != nil {
			t.Fatal(err)
		}

		var workflow struct {
			Jobs map[string]struct {
				Uses string `yaml:"uses"`
			} `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(body, &workflow); err != nil {
			t.Fatal(err)
		}

		for job, call := range workflow.Jobs {
			dependency, local := strings.CutPrefix(call.Uses, "./.github/workflows/")
			if local && !slices.Contains(minimalPreparationWorkflows, dependency) {
				t.Errorf("%s job %s calls %s, missing from the hosted snapshot", name, job, dependency)
			}
		}
	}
}
