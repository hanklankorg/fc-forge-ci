// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// cosignKeySecret matches a read of the Cosign key secret or its passphrase.
var cosignKeySecret = regexp.MustCompile(`secrets\.RELEASE_COSIGN_(PRIVATE_KEY|PASSPHRASE)\b`)

// keySecretSelected matches the two ways a workflow learns that this run signs
// with the key secret: the plan's flag, or the exact key reference input.
var keySecretSelected = regexp.MustCompile(`\.sign\.uses_key_secret\b|inputs\['sign-key'\] == 'env://COSIGN_KEY'`)

// gatedKeySecret is the one form a signing step reads the secret in: empty
// unless the run signs with the key secret.
var gatedKeySecret = regexp.MustCompile(`^\$\{\{ [^|]*(\.sign\.uses_key_secret|inputs\['sign-key'\] == 'env://COSIGN_KEY') && secrets\.RELEASE_COSIGN_(PRIVATE_KEY|PASSPHRASE) \|\| '' \}\}$`)

// keySecretStep and keySecretJob are the parts of a workflow the guard reads.
type keySecretStep struct {
	Name            string            `yaml:"name"`
	If              string            `yaml:"if"`
	Run             string            `yaml:"run"`
	ContinueOnError any               `yaml:"continue-on-error"`
	With            map[string]any    `yaml:"with"`
	Env             map[string]string `yaml:"env"`
}

type keySecretJob struct {
	Env   map[string]string `yaml:"env"`
	Steps []keySecretStep   `yaml:"steps"`
}

// The Cosign key secret reaches a GitHub job only in the steps that use it:
// the step that binds it to the caller's trusted public key, and the Cosign
// signing steps after it. A step reads it only when the run signs with
// env://COSIGN_KEY, so a keyless or cloud-KMS release never holds it, and no
// job or workflow puts it in an environment every step inherits.
func TestCosignKeySecretReachesOnlyBoundSigningSteps(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(reporoot.Path(t), ".github", "workflows")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	signers := 0

	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".yml" {
			continue
		}

		body, err := os.ReadFile(filepath.Join(dir, entry.Name())) //nolint:gosec // repository contract fixture.
		require.NoError(t, err)

		var workflow struct {
			Env  map[string]string       `yaml:"env"`
			Jobs map[string]keySecretJob `yaml:"jobs"`
		}
		require.NoError(t, yaml.Unmarshal(body, &workflow), entry.Name())

		for name, value := range workflow.Env {
			if cosignKeySecret.MatchString(value) {
				t.Errorf("%s puts the Cosign key secret in the workflow environment (%s)", entry.Name(), name)
			}
		}

		for jobName, job := range workflow.Jobs {
			signers += checkKeySecretJob(t, entry.Name()+" job "+jobName, job)
		}
	}

	require.Positive(t, signers, "no step signs with the Cosign key secret; the guard has drifted")
}

// checkKeySecretJob reports each read of the key secret in job that is not a
// gated binding step or a gated signing step after one, and returns how many
// signing steps read it.
func checkKeySecretJob(t *testing.T, where string, job keySecretJob) int {
	t.Helper()

	for name, value := range job.Env {
		if cosignKeySecret.MatchString(value) {
			t.Errorf("%s puts the Cosign key secret in the job environment (%s)", where, name)
		}
	}

	bound, signers := false, 0

	for _, step := range job.Steps {
		binds := strings.Contains(step.Run, "forge-ci validate signing-key --key env://COSIGN_KEY")

		// Only an env value carries the secret: one passed to an action or
		// written into the script escapes every check below.
		if cosignKeySecret.MatchString(step.Run) || cosignKeySecret.MatchString(fmt.Sprint(step.With)) {
			t.Errorf("%s step %q passes the Cosign key secret outside env", where, step.Name)
		}

		for name, value := range step.Env {
			if !cosignKeySecret.MatchString(value) {
				continue
			}

			switch {
			case binds:
				// The binding must run whenever the key is selected, and
				// its refusal must stop the job: no alternative branch in
				// its condition, and no continue-on-error.
				if !keySecretSelected.MatchString(step.If) || strings.Contains(step.If, "||") || strings.Contains(step.If, "always()") {
					t.Errorf("%s step %q binds the key secret under %q, not exactly when the key secret is selected", where, step.Name, step.If)
				}

				if step.ContinueOnError != nil && step.ContinueOnError != false {
					t.Errorf("%s step %q lets a failed key binding continue", where, step.Name)
				}

				bound = true
			case !cosignSigningCall.MatchString(step.Run):
				t.Errorf("%s step %q reads the Cosign key secret (%s) but neither binds nor signs", where, step.Name, name)
			case !gatedKeySecret.MatchString(value):
				t.Errorf("%s step %q reads the Cosign key secret (%s) whatever the run signs with", where, step.Name, name)
			case !bound:
				t.Errorf("%s step %q signs with the key secret before the job binds it to the trusted public key", where, step.Name)
			default:
				signers++
			}
		}
	}

	return signers
}
