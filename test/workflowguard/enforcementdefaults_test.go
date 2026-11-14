// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/enforcement"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// A wrapper default must not silently replace the engine's report-only default
// with enforcement. Required child inputs have no fallback: their parent must
// forward the chosen value, as the administrator-forwarding guards also check.
func TestShippedEnforcementDefaultsMatchTheEngineAcrossForges(t *testing.T) {
	t.Parallel()

	policy, err := enforcement.Current()
	if err != nil {
		t.Fatal(err)
	}

	files := append(yamlFiles(t, ".github/workflows"), yamlFiles(t, ".forgejo/workflows")...)
	files = append(files, yamlFiles(t, "templates")...)

	for _, directory := range []string{".github/actions", ".forgejo/actions"} {
		for _, entry := range reporoot.ReadDir(t, directory) {
			if entry.IsDir() {
				files = append(files, directory+"/"+entry.Name()+"/action.yml")
			}
		}
	}

	checked := 0
	seen := map[enforcement.Check]bool{}

	for _, path := range files {
		var document yaml.Node
		if err := yaml.Unmarshal(reporoot.ReadFile(t, path), &document); err != nil {
			t.Fatalf("%s: %v", path, err)
		}

		root := document.Content[0]
		component := strings.HasPrefix(path, "templates/")
		action := strings.HasSuffix(path, "/action.yml")
		inputs := mappingChild(mappingChild(mappingChild(root, "on"), "workflow_call"), "inputs")

		if component {
			inputs = mappingChild(mappingChild(root, "spec"), "inputs")
		} else if action {
			inputs = mappingChild(root, "inputs")
		}

		for _, check := range enforcement.Checks {
			declaration := mappingChild(inputs, enforcement.Setting(check))
			if declaration == nil {
				continue
			}

			value := mappingChild(declaration, "default")
			if value == nil {
				if !component && scalarChild(declaration, "required") != "true" {
					t.Errorf("%s: optional %s has no declared default", path, enforcement.Setting(check))
				}

				continue
			}

			want, err := policy.Enforces(check)
			if err != nil {
				t.Fatal(err)
			}

			if value.Kind != yaml.ScalarNode || value.Value != strconv.FormatBool(want) {
				t.Errorf("%s: %s defaults to %q, engine default is %t", path, enforcement.Setting(check), value.Value, want)
			}

			checked++
			seen[check] = true
		}
	}

	if checked < 40 || len(seen) != len(enforcement.Checks) {
		t.Fatalf("default guard checked only %d inputs across %d check classes", checked, len(seen))
	}
}
