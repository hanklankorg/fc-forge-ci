// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// Execute the templates' literal readers with artifact-controlled values.
// Command substitutions, spaces and equals signs must survive as data, and
// unrelated output names must never enter the job environment.
func TestPublishOutputsAreDataAndOnlyKnownNamesAreImported(t *testing.T) {
	t.Parallel()

	for _, file := range []string{"publish-apple-appstore.yml", "publish-forge-packages.yml", "publish-maven-central.yml"} {
		t.Run(file, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			output := filepath.Join(dir, "output.env")

			const payload = "artifact with spaces $(touch EXPLOITED) `touch EXPLOITED` =value= .ipa"

			contents := "IPA_FILE=" + payload + "\nREQUEST_ID=" + payload + "\nVERSION=" + payload + "\nIS_SNAPSHOT=false\nUNRELATED_OUTPUT=must-not-be-exported\n"
			if err := os.WriteFile(output, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}

			scripts := publisherScripts(t, file)

			var readers strings.Builder

			for _, script := range scripts {
				if strings.Contains(script, "while IFS= read -r line") {
					readers.WriteString(script + "\n")
				}
			}

			if readers.Len() == 0 {
				t.Fatal("the component has no literal output reader")
			}

			readers.WriteString("test -z \"${UNRELATED_OUTPUT:-}\"\n")

			if file == "publish-apple-appstore.yml" {
				readers.WriteString("test \"$IPA_FILE\" = \"$EXPECTED\" && test \"$REQUEST_ID\" = \"$EXPECTED\"\n")
			} else {
				readers.WriteString("test \"$VERSION\" = \"$EXPECTED\" && test \"$IS_SNAPSHOT\" = false\n")
			}

			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()

			command := exec.CommandContext(ctx, "/bin/bash", "--noprofile", "--norc", "-eu", "-c", readers.String()) //nolint:gosec // Executes only the actual template's data readers in an isolated fixture.
			command.Dir = dir

			command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "CI_OUTPUT=" + output, "EXPECTED=" + payload}
			if result, err := command.CombinedOutput(); err != nil {
				t.Fatalf("output values did not survive literally: %v\n%s", err, result)
			}

			if _, err := os.Stat(filepath.Join(dir, "EXPLOITED")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("output data executed a shell command")
			}

			// The job creates its output file away from artifact extraction, so
			// old artifact contents cannot be appended to and then imported.
			initScript := scripts[0] + "\ntest \"$CI_OUTPUT\" != \"$OLD_OUTPUT\" && test ! -s \"$CI_OUTPUT\"\n"
			init := exec.CommandContext(ctx, "/bin/bash", "--noprofile", "--norc", "-eu", "-c", initScript) //nolint:gosec // Reviewed template initializer; all paths are in t.TempDir.
			init.Dir = dir

			init.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "TMPDIR=" + dir, "CI_OUTPUT=" + output, "OLD_OUTPUT=" + output}
			if result, err := init.CombinedOutput(); err != nil {
				t.Fatalf("output initialization retained artifact data: %v\n%s", err, result)
			}
		})
	}
}

func publisherScripts(t *testing.T, file string) []string {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(reporoot.Path(t), "templates", file))
	if err != nil {
		t.Fatal(err)
	}

	decoder := yaml.NewDecoder(bytes.NewReader(body))

	var header any
	if err := decoder.Decode(&header); err != nil {
		t.Fatal(err)
	}

	var jobs map[string]struct {
		Script []string `yaml:"script"`
	}
	if err := decoder.Decode(&jobs); err != nil {
		t.Fatal(err)
	}

	return jobs["$[[ inputs.job-name ]]"].Script
}
