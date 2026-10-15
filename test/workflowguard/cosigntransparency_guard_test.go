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

// cosignSigningCall matches the commands that make a Cosign signature.
var cosignSigningCall = regexp.MustCompile(`forge-ci (release sign|container sign|container attest)\b`)

// The cosign adapter learns the transparency choice only from
// FORGE_CI_COSIGN_TRANSPARENCY. On GitHub, sign.transparency was resolved into
// the plan but no signing step passed it on, so a release configured with
// `transparency: none` still wrote every signature to the public Rekor log.
// A step that pins keyless signing is exempt: a keyless signature always
// publishes, and the adapter's default is to publish.
func TestCosignSigningStepsCarryTheTransparencyChoice(t *testing.T) {
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
				Steps []struct {
					Name string            `yaml:"name"`
					Run  string            `yaml:"run"`
					Env  map[string]string `yaml:"env"`
				} `yaml:"steps"`
			} `yaml:"jobs"`
		}
		require.NoError(t, yaml.Unmarshal(body, &workflow), entry.Name())

		for jobName, job := range workflow.Jobs {
			for _, step := range job.Steps {
				if !cosignSigningCall.MatchString(step.Run) || step.Env["SIGN_METHOD"] == "sigstore" {
					continue
				}

				checked++

				if step.Env["FORGE_CI_COSIGN_TRANSPARENCY"] == "" {
					t.Errorf("%s job %q step %q signs with Cosign but does not pass FORGE_CI_COSIGN_TRANSPARENCY", entry.Name(), jobName, step.Name)
				}
			}
		}
	}

	require.Positive(t, checked, "no configurable Cosign signing steps were found; the guard has drifted")
}
