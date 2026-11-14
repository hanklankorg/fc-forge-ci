// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"gopkg.in/yaml.v3"
)

func TestNativeVersionPreparationReferencesNoSigningSecret(t *testing.T) {
	t.Parallel()

	for _, file := range []string{"version-bump.yml", "release-prepare-stage.yml"} {
		document, err := effectiveWorkflow(reporoot.ReadFile(t, ".github/workflows/"+file))
		if err != nil {
			t.Fatal(err)
		}

		jobs := mappingChild(document, "jobs")
		preparation := mappingChild(jobs, "prepare-version-files")

		body, err := yaml.Marshal(preparation)
		if err != nil || preparation == nil || strings.Contains(string(body), "secrets.") || scalarChild(mappingChild(preparation, "permissions"), "contents") != "read" {
			t.Fatalf("%s native preparation has signing secrets or write permission", file)
		}

		signerName := "version-bump"
		if file == "version-bump.yml" {
			signerName = "bump-version"
		}

		steps := mappingChild(mappingChild(jobs, signerName), "steps")
		for _, step := range steps.Content {
			run := scalarChild(step, "run")
			if strings.Contains(run, "forge-ci version bump-plan") && !strings.Contains(run, "--patch-sha256") {
				t.Fatalf("%s signing job runs an unverified native version plan", file)
			}

			if strings.Contains(run, "forge-ci version bump") && !strings.Contains(run, "bump-plan") &&
				(scalarChild(step, "if") != `${{ !contains(fromJSON('["maven","npm","cargo"]'), inputs['project-type']) }}` ||
					scalarChild(mappingChild(step, "env"), "PROJECT_TYPE") != "${{ inputs['project-type'] }}") {
				t.Fatalf("%s signing job can run native project tooling", file)
			}
		}
	}
}
