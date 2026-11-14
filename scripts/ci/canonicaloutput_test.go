// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package ci

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type canonicalOutputStep struct {
	ID              string            `yaml:"id"`
	Run             string            `yaml:"run"`
	If              string            `yaml:"if"`
	Uses            string            `yaml:"uses"`
	ContinueOnError any               `yaml:"continue-on-error"`
	Env             map[string]string `yaml:"env"`
}

// Read the ACTUAL reusable workflow rather than duplicating its output wiring
// or success-emission shell in the inert execution test below.
func canonicalOutputContract(t *testing.T) (canonicalOutputStep, canonicalOutputStep, canonicalOutputStep) {
	t.Helper()

	body, err := os.ReadFile("../../.github/workflows/canonical-cli-bootstrap.yml")
	if err != nil {
		t.Fatal(err)
	}

	var workflow struct {
		On struct {
			Call struct {
				Outputs map[string]struct {
					Value string `yaml:"value"`
				} `yaml:"outputs"`
			} `yaml:"workflow_call"`
		} `yaml:"on"`
		Jobs map[string]struct {
			Outputs         map[string]string     `yaml:"outputs"`
			Steps           []canonicalOutputStep `yaml:"steps"`
			ContinueOnError any                   `yaml:"continue-on-error"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		t.Fatal(err)
	}

	receiver := workflow.Jobs["canonical-receive"]
	if receiver.ContinueOnError != nil {
		t.Fatal("receiver must propagate failures")
	}

	if got := workflow.On.Call.Outputs["artifact-id"].Value; got != "${{ jobs.canonical-receive.outputs.artifact-id }}" {
		t.Fatalf("public output bypass/fallback: %q", got)
	}

	if got := receiver.Outputs["artifact-id"]; got != "${{ steps.verified.outputs.artifact-id }}" {
		t.Fatalf("receiver output bypass/fallback: %q", got)
	}

	for output, field := range map[string]string{"producer-run-id": "run-id", "producer-run-attempt": "run-attempt"} {
		if got := workflow.On.Call.Outputs[output].Value; got != "${{ jobs.canonical-build.outputs."+field+" }}" {
			t.Fatalf("relabelled original producer %s: %q", output, got)
		}
	}

	var admit, bootstrap, emit canonicalOutputStep

	bootstrapIndex, emitIndex := -1, -1

	for index, step := range receiver.Steps {
		if step.ContinueOnError != nil {
			t.Fatalf("receiver step %d can mask failure", index)
		}

		if strings.HasSuffix(step.Run, `canonical-cli-delivery.sh" admit`) {
			admit = step
		}

		if step.ID == "bootstrap" {
			bootstrap, bootstrapIndex = step, index
		}

		if step.ID == "verified" {
			emit, emitIndex = step, index
		}
	}

	if admit.Run == "" || bootstrap.Run != `/bin/bash "$ENGINE_CHECKOUT/scripts/ci/canonical-cli-delivery.sh" receive` || emitIndex != bootstrapIndex+1 || emitIndex != len(receiver.Steps)-1 {
		t.Fatal("missing/incorrect receive -> emission ordering")
	}

	if bootstrap.If != "" || emit.If != "${{ success() && steps.bootstrap.outcome == 'success' }}" {
		t.Fatal("emission must require overall success AND successful bootstrap")
	}

	if emit.Uses != "" || emit.Env["ORIGINAL_ARTIFACT_ID"] != "${{ needs.canonical-build.outputs.artifact-id }}" || len(emit.Env) != 1 {
		t.Fatal("emission must use only the original producer ID")
	}

	return admit, bootstrap, emit
}

func TestCanonicalVerifiedArtifactOutputContract(t *testing.T) {
	canonicalOutputContract(t)
}

func TestCanonicalVerifiedArtifactFailureOutputs(t *testing.T) {
	admit, bootstrap, emit := canonicalOutputContract(t)
	for _, scenario := range []string{"success", "metadata-failure", "download-failure", "hash-failure", "bootstrap-failure", "cli-failure"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newCanonicalFixture(t)

			entry, err := os.ReadFile("canonical-cli-delivery.sh")
			if err != nil {
				t.Fatal(err)
			}

			canonicalWrite(t, filepath.Join(fixture.engine, "scripts/ci/canonical-cli-delivery.sh"), string(entry), 0o600)

			payload := fixture.cli
			if scenario == "cli-failure" {
				payload += "exit 42\n"
			}

			if scenario == "bootstrap-failure" || scenario == "cli-failure" {
				record := fmt.Sprintf("%x  bin/forge-ci\n", sha256.Sum256([]byte(payload)))
				// The existing build verifier reads the first record; the unchanged
				// reviewed strict bootstrap rejects a committed multi-line record.
				if scenario == "bootstrap-failure" {
					record += record
				}

				for _, arch := range []string{"amd64", "arm64"} {
					canonicalWrite(t, filepath.Join(fixture.engine, ".engine/bin/forge-ci-linux-"+arch+".sha256"), record, 0o600)
				}
				// Fixture-only reapproval, never a production/owner commit.
				canonicalGit(t, fixture.engine, "-c", "core.fsmonitor=false", "-c", "filter.marker.clean=/bin/cat", "add", ".engine/bin")
				canonicalGit(t, fixture.engine, "-c", "core.fsmonitor=false", "-c", "filter.marker.clean=/bin/cat", "-c", "user.name=Offline", "-c", "user.email=offline@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "inert failure fixture")
				commit := canonicalGit(t, fixture.engine, "rev-parse", "HEAD")
				fixture.env["ENGINE_COMMIT"], fixture.env["PRODUCER_ENGINE_COMMIT"] = commit, commit
				fixture.env["PRODUCER_ARTIFACT_NAME"] = "forge-ci-canonical-linux-100-2-" + commit
				fixture.writeMetadata(t, nil)
			}

			if out, err := fixture.run(t, "prepare"); err != nil {
				t.Fatalf("prepare: %v\n%s", err, out)
			}

			if scenario == "metadata-failure" {
				fixture.writeMetadata(t, func(m map[string]any) { m["id"] = 301 })
			}

			if scenario == "hash-failure" {
				payload += "# changed bytes\n"
			}

			archivePayload := filepath.Join(fixture.root, "inert-download-payload")
			canonicalWrite(t, archivePayload, payload, 0o600)

			download := `cp "$INERT_DOWNLOAD_PAYLOAD" "$DELIVERY_DIRECTORY/forge-ci-linux-amd64" && cp "$INERT_DOWNLOAD_PAYLOAD" "$DELIVERY_DIRECTORY/forge-ci-linux-arm64"`
			if scenario == "download-failure" {
				download = "false"
			}
			// Offline fail-fast execution models the workflow's default success
			// gating, whose exact if/outcome wiring is asserted above. Execute the
			// ACTUAL admission, bootstrap and emission scripts. Only the external
			// native downloader is replaced with an inert copy/failure, no network.
			command := exec.CommandContext(t.Context(), "/bin/bash", "--noprofile", "--norc", "-c", "set -euo pipefail\n"+admit.Run+"\n"+download+"\n"+bootstrap.Run+"\n"+emit.Run)
			fixture.env["ORIGINAL_ARTIFACT_ID"] = fixture.env["PRODUCER_ARTIFACT_ID"]
			fixture.env["INERT_DOWNLOAD_PAYLOAD"] = archivePayload

			for key, value := range fixture.env {
				command.Env = append(command.Env, key+"="+value)
			}
			// Only the simulated download uses cp; production run scripts select
			// trusted tools themselves. No SDK/PATH forge-ci probe is introduced.
			command.Env = append(command.Env, "PATH=/usr/bin:/bin")
			out, runErr := command.CombinedOutput()
			// Prove the intended failure boundary was reached, not just an
			// unrelated earlier error that happened to leave outputs empty.
			wantHelper := scenario == "success" || scenario == "bootstrap-failure" || scenario == "cli-failure"
			wantCLI := scenario == "success" || scenario == "cli-failure"

			for marker, wanted := range map[string]bool{fixture.helperMarker: wantHelper, fixture.marker: wantCLI} {
				body, err := os.ReadFile(marker)
				if wanted {
					if err != nil || len(body) == 0 {
						t.Fatalf("expected failure boundary not reached (%s): %q (%v)\n%s", marker, body, err, out)
					}
				} else if !os.IsNotExist(err) {
					t.Fatalf("process before admission (%s): %q (%v)\n%s", marker, body, err, out)
				}
			}

			outputs, readErr := os.ReadFile(fixture.env["GITHUB_OUTPUT"])
			if scenario == "success" {
				if runErr != nil || readErr != nil || string(outputs) != "artifact-id=300\n" {
					t.Fatalf("success output: %v, %v, %q\n%s", runErr, readErr, outputs, out)
				}
			} else {
				if runErr == nil {
					t.Fatalf("accepted failure %s: %s", scenario, out)
				}

				if !os.IsNotExist(readErr) {
					t.Fatalf("verified ID output after failure: %q (%v)\n%s", outputs, readErr, out)
				}
			}
		})
	}
}
