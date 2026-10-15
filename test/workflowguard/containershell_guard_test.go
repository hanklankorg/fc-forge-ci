// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// bashOnly matches syntax dash rejects or misreads: arrays, [[ ]], array
// expansion, here-strings and ANSI-C quoting with an escape ($'\n'), which dash
// reads as a literal dollar sign and string. Comments are not stripped, so a
// step that needs one of these in a comment must reword it.
var bashOnly = regexp.MustCompile(`\w\+?=\(|\[\[ |\$\{\w+\[@\]\}|<<<|\$'\\`)

// A step in a job that runs in a container gets GitHub's default shell there,
// sh, which is dash on Debian images. lint-nanolinter built its arguments in a
// bash array and every pull-request gate failed with `Syntax error: "("
// unexpected` before the lint ran, and the GitHub release job kept the
// newline `${KEY%$'\n'}` was meant to strip, so the Cosign public key no
// longer matched its pinned digest. Steps on the runner itself default to bash
// and are not checked.
func TestContainerJobStepsArePOSIXShell(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(reporoot.Path(t), ".github", "workflows")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	checked := 0

	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".yml" {
			continue
		}

		body, err := os.ReadFile(filepath.Join(dir, entry.Name())) //nolint:gosec // repository contract fixture.
		require.NoError(t, err)

		var workflow struct {
			Jobs map[string]struct {
				Container any `yaml:"container"`
				Defaults  struct {
					Run struct {
						Shell string `yaml:"shell"`
					} `yaml:"run"`
				} `yaml:"defaults"`
				Steps []struct {
					Name  string `yaml:"name"`
					Run   string `yaml:"run"`
					Shell string `yaml:"shell"`
				} `yaml:"steps"`
			} `yaml:"jobs"`
		}
		require.NoError(t, yaml.Unmarshal(body, &workflow), entry.Name())

		for jobName, job := range workflow.Jobs {
			if job.Container == nil || job.Defaults.Run.Shell != "" {
				continue
			}

			for _, step := range job.Steps {
				if step.Run == "" || step.Shell != "" {
					continue
				}

				checked++

				if match := bashOnly.FindString(step.Run); match != "" {
					t.Errorf("%s job %q step %q uses bash-only %q but runs under the container's sh", entry.Name(), jobName, step.Name, match)
				}
			}
		}
	}

	require.Positive(t, checked, "no container job steps were found; the guard has drifted")
}

// The pattern names each construct by a line that has broken a container
// step, and leaves the POSIX forms that replace them alone.
func TestBashOnlyPatternMatchesWhatDashMisreads(t *testing.T) {
	t.Parallel()

	for line, bashOnlyWant := range map[string]bool{
		`args=(--a --b)`:                        true,
		`if [[ -n "$x" ]]; then`:                true,
		`run "${args[@]}"`:                      true,
		`grep x <<<"$y"`:                        true,
		`printf '%s\n' "${KEY%$'\n'}" >"$file"`: true,
		`key=$(printf '%s' "${KEY}")`:           false,
		`printf '%s\n' "${key}" >"${file}"`:     false,
		`grep -q 'x$' file`:                     false,
		`if [ -n "$x" ]; then`:                  false,
	} {
		if got := bashOnly.MatchString(line); got != bashOnlyWant {
			t.Errorf("bashOnly.MatchString(%q) = %v, want %v", line, got, bashOnlyWant)
		}
	}
}
