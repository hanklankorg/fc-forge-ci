// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// A Forgejo workflow that plans native platform legs passes its image build
// runners to the plan, so the engine refuses a leg without its own runner
// before any job is scheduled; no workflow guards that by hand. Where the
// legs choose their runner from repository variables, the plan is given the
// same expressions the legs' runs-on reads.
func TestForgejoPlatformPlansAdmitTheirBuildRunners(t *testing.T) {
	t.Parallel()

	root := reporoot.Path(t)
	planned := 0

	for _, tree := range []string{".forgejo/workflows", "examples", "test/live/fixtures/consumers"} {
		err := filepath.WalkDir(filepath.Join(root, tree), func(path string, entry fs.DirEntry, err error) error {
			if err != nil || !entry.Type().IsRegular() {
				return err
			}

			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}

			relative = filepath.ToSlash(relative)
			if !forgejoAuthorizationWorkflow(relative) {
				return nil
			}

			body := string(reporoot.ReadFile(t, relative))
			if strings.Contains(body, "Require an arm64 runner") {
				t.Errorf("%s: a hand-written step guards the arm64 runner; platform-plan admits it", relative)
			}

			if !strings.Contains(body, "container platform-plan") {
				return nil
			}

			workflow := loadWorkflow(t, relative)

			for _, target := range workflow.jobs {
				for _, step := range target.steps() {
					run := scalarString(mappingValue(step, "run"))
					if !strings.Contains(run, "container platform-plan") {
						continue
					}

					planned++

					for _, arch := range []string{"amd64", "arm64"} {
						if !strings.Contains(run, `--runner-input "build-runs-on-linux-`+arch+`=$`) {
							t.Errorf("%s: the platform plan is not given build-runs-on-linux-%s", relative, arch)
						}

						value := scalarString(mappingValue(mappingValue(step, "env"), "BUILD_RUNS_ON_LINUX_"+strings.ToUpper(arch)))
						value = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, "${{"), "}}"))

						if value == "" {
							t.Errorf("%s: the platform plan step binds no BUILD_RUNS_ON_LINUX_%s", relative, strings.ToUpper(arch))
						}

						export, ok := workflow.jobs["export"]
						if strings.HasPrefix(value, "vars.") && (!ok || !strings.Contains(export.runsOn(), "("+value+")")) {
							t.Errorf("%s: the export legs do not run on (%s), the %s runner the plan admitted", relative, value, arch)
						}
					}
				}
			}

			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	if planned < 3 {
		t.Fatalf("found %d Forgejo platform plans, want the runtime images' and the container-first demos'", planned)
	}
}
