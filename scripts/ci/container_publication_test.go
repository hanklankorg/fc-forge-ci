// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package ci

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Execute the workflow's publication glue with inert local collaborators. The
// fake registry begins with an older final version/latest/staging candidate;
// neither a provider nor a container engine nor credentials are used.
func TestContainerFinalAliasesWaitForSigningAndAttestation(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../../.github/workflows/publish-container.yml")
	require.NoError(t, err)

	type step struct {
		Name string            `yaml:"name"`
		ID   string            `yaml:"id"`
		If   string            `yaml:"if"`
		Run  string            `yaml:"run"`
		Env  map[string]string `yaml:"env"`
	}

	var workflow struct {
		Jobs map[string]struct {
			Steps []step `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(body, &workflow))
	steps := workflow.Jobs["merge"].Steps
	require.NotEmpty(t, steps)

	for _, s := range steps {
		if s.ID == "outputs" {
			require.Equal(t, "${{ steps.candidate.outputs.image-digest-ref }}", s.Env["IMAGE_REF"])
			require.Empty(t, s.Env["TAGS"], "outputs must not resolve a mutable final alias")
		}

		if strings.Contains(s.Run, "container ledger add") {
			require.Equal(t, "${{ steps.candidate.outputs.image-digest-ref }}", s.Env["IMAGE_DIGEST_REF"])
			require.Equal(t, "${{ steps.candidate.outputs.digest }}", s.Env["DIGEST"])
		}
	}

	const image = "ghcr.io/owner/app"

	digestRef := image + "@sha256:" + strings.Repeat("a", 64)
	finals := image + ":v1.2.3\n" + image + ":latest\n" + image + ":staging-v1.2.3\n"

	for _, scenario := range []string{"sign-failure", "attest-failure", "signed-success", "unsigned", "overlap-failure", "unsigned-overlap-failure"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			aliases, trace := filepath.Join(root, "aliases"), filepath.Join(root, "trace")
			require.NoError(t, os.WriteFile(aliases, []byte("prior digest\n"), 0o600))

			stub := `#!/bin/bash
set -euo pipefail
echo "$*" >> "$TRACE"
case "$*" in
  'container manifest merge')
    printf '%s\n' "$TAGS" > "$CANDIDATE"
    if [[ "$TAGS" == *':latest'* || "$TAGS" == *':v1.2.3'* ]]; then printf '%s\n' "$TAGS" > "$ALIASES"; fi ;;
  'container manifest inspect') test -f "$CANDIDATE" ;;
  'validate signing-privacy') ;;
  container\ sign\ *) [[ "$SCENARIO" != sign-failure ]] ;;
  container\ attest\ *) [[ "$SCENARIO" != attest-failure ]] ;;
  'container manifest tag')
    test "$IMAGE_REF" = "$DIGEST_REF"
    printf '%s' "$TAGS" > "$ALIASES"
    [[ "$SCENARIO" != *overlap-failure ]] ;;
  *) echo "unexpected collaborator call: $*" >&2; exit 97 ;;
esac
`
			require.NoError(t, os.WriteFile(filepath.Join(root, "forge-ci"), []byte(stub), 0o700))

			values := map[string]string{
				"needs.prep.outputs.image-name":     image,
				"needs.prep.outputs.tags":           image + ":v1.2.3\n" + image + ":latest",
				"needs.prep.outputs['staging-tag']": image + ":staging-v1.2.3",
				"github.run_id":                     "42", "github.run_attempt": "2",
				"steps.candidate.outputs.image-digest-ref": digestRef,
				"steps.outputs.outputs.image-digest-ref":   digestRef,
			}
			expr := regexp.MustCompile(`\$\{\{\s*(.*?)\s*\}\}`)
			render := func(s string) string {
				return expr.ReplaceAllStringFunc(s, func(match string) string { return values[strings.TrimSpace(expr.FindStringSubmatch(match)[1])] })
			}

			var (
				failed   bool
				promoted bool
			)

			for _, step := range steps {
				selected := strings.Contains(step.Run, "container manifest ") || strings.Contains(step.Run, "container sign ") || strings.Contains(step.Run, "container attest ")
				if !selected {
					continue
				}

				if strings.Contains(step.Run, "container sign ") || strings.Contains(step.Run, "container attest ") {
					require.Contains(t, step.If, "inputs['sign-image']")
					require.Contains(t, step.If, "inputs['sign-method'] != 'gpg'")

					if strings.HasPrefix(scenario, "unsigned") {
						continue
					}

					require.Contains(t, step.Run, "steps.candidate.outputs.image-digest-ref")
				}

				if strings.Contains(step.Run, "container manifest tag") {
					require.Empty(t, step.If, "promotion must retain default success gating")

					promoted = true
				}

				cmd := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", render(step.Run))

				cmd.Env = []string{"PATH=" + root + ":/usr/bin:/bin", "ALIASES=" + aliases, "TRACE=" + trace, "CANDIDATE=" + filepath.Join(root, "candidate"), "SCENARIO=" + scenario, "DIGEST_REF=" + digestRef}
				for k, v := range step.Env {
					cmd.Env = append(cmd.Env, k+"="+render(v))
				}

				output, runErr := cmd.CombinedOutput()
				if runErr != nil {
					require.Contains(t, scenario, "failure", "%s: %s", step.Name, output)

					failed = true

					break // Actions' default success() gate prevents later steps.
				}
			}

			got, err := os.ReadFile(aliases)
			require.NoError(t, err)
			calls, err := os.ReadFile(trace)
			require.NoError(t, err)

			switch {
			case strings.Contains(scenario, "overlap-failure"):
				require.True(t, failed)
				require.True(t, promoted)
				require.Equal(t, finals, string(got))
				require.Equal(t, 1, strings.Count(string(calls), "container manifest inspect"), "failed alias verification must block output hand-off")
			case strings.Contains(scenario, "failure"):
				require.True(t, failed)
				require.False(t, promoted)
				require.Equal(t, "prior digest\n", string(got))
				require.NotContains(t, string(calls), "container manifest tag")
			default:
				require.False(t, failed)
				require.True(t, promoted)
				require.Equal(t, finals, string(got))
			}

			if strings.HasPrefix(scenario, "unsigned") {
				require.NotContains(t, string(calls), "container sign ")
				require.NotContains(t, string(calls), "container attest ")
			} else if scenario == "signed-success" {
				require.Contains(t, string(calls), "container sign "+digestRef)
				require.Contains(t, string(calls), "container attest --type slsaprovenance1 "+digestRef)
			}
		})
	}
}
