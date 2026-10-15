// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

const (
	// transparencyVariable is the CLI setting that decides, for signing and
	// for verification together, whether the public log is used.
	transparencyVariable = "FORGE_CI_COSIGN_TRANSPARENCY"

	// validatedTransparency is the one source a signer job may take it from.
	validatedTransparency = "${{ needs.coordinates.outputs.transparency_log }}"
)

// The CLI reads an unset or unknown value as `public`, and an entry in the
// public log cannot be withdrawn. So the layer's default is `none`, the
// preflight accepts the two values and nothing else, and every job that runs
// in the signer image, the only place Cosign runs, takes the validated value
// as job environment. A step cannot then sign or verify with another value
// than the rest of its workflow, and a job added later cannot forget it.
func TestTransparencyLogIsOffUnlessTheCallerAsksForThePublicLog(t *testing.T) {
	t.Parallel()

	for relative := range signerWorkflows() {
		root, err := parseDocument(reporoot.ReadFile(t, relative))
		if err != nil {
			t.Fatalf("%s: %v", relative, err)
		}

		input := workflowInput(root, "transparency-log")
		if scalarString(mappingValue(input, "required")) != "false" || scalarString(mappingValue(input, "default")) != "none" {
			t.Errorf("%s: input transparency-log must be optional and default to none", relative)
		}

		if reads := strings.Count(string(reporoot.ReadFile(t, relative)), "inputs['transparency-log']"); reads != 1 {
			t.Errorf("%s: transparency-log is read %d times, want once, by the preflight", relative, reads)
		}

		for _, value := range keyValues(root, transparencyVariable) {
			if value != validatedTransparency {
				t.Errorf("%s: %s is set to %q, want only %q", relative, transparencyVariable, value, validatedTransparency)
			}
		}

		for name, target := range loadWorkflow(t, relative).jobs {
			if target.containerImage() != signerImage {
				continue
			}

			if got := scalarString(mappingValue(mappingValue(target.node, "env"), transparencyVariable)); got != validatedTransparency {
				t.Errorf("%s: signer job %s has job environment %s=%q, want %q", relative, name, transparencyVariable, got, validatedTransparency)
			}
		}
	}
}

// Each preflight hands on `none` and `public` unchanged and stops on anything
// else, naming the input, before a signer container starts.
func TestTransparencyLogPreflightAcceptsTwoValues(t *testing.T) {
	t.Parallel()

	for relative := range signerWorkflows() {
		t.Run(filepath.Base(relative), func(t *testing.T) {
			t.Parallel()

			script := preflightScript(t, relative)

			for _, value := range []string{"none", "public"} {
				ok, output, _ := runPreflightWithStderr(t, script, exampleRepository, exampleSigner, "TRANSPARENCY_LOG_INPUT="+value)
				if !ok || !strings.HasSuffix(output, "\ntransparency_log="+value+"\n") {
					t.Errorf("transparency-log %q: accepted=%t output %q", value, ok, output)
				}
			}

			for _, value := range []string{"", "Public", "NONE", "off", "true", "none ", "public\nnone"} {
				ok, output, stderr := runPreflightWithStderr(t, script, exampleRepository, exampleSigner, "TRANSPARENCY_LOG_INPUT="+value)
				if ok || output != "" || !strings.Contains(stderr, "transparency-log must be none or public") {
					t.Errorf("transparency-log %q: accepted=%t output %q stderr %q", value, ok, output, stderr)
				}
			}
		})
	}
}
