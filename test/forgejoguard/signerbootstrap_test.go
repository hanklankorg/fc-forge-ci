// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// signerImage is the validated coordinate every signer job runs by digest.
const signerImage = "${{ needs.coordinates.outputs.signer_image }}"

// bootstrapSteps must open every signer job, in this order, before the job may
// use a secret or the engine.
//
// The checkout holds the checksum of the engine binary and not the binary, so
// the job downloads it. That is a step of its own, after the checkout is
// known to be the pinned commit and before the toolchain check: left to the
// first engine call, the download would run in a step that holds a signing
// key, and after the check that judges every executable of the job.
func bootstrapSteps() []string {
	return []string{
		"Checkout layer helpers",
		"Verify trusted helper checkout",
		"Fetch engine binary",
		"Verify signer toolchain",
	}
}

// The positions of the two steps the checker reads the script of.
const (
	fetchStep     = 2
	toolchainStep = 3
)

// signerWorkflows pins how many signer jobs each workflow declares. The counts
// are deliberate: adding a job that runs inside the signer image is a
// signing-boundary change and should fail here until it is reviewed.
func signerWorkflows() map[string]int {
	return map[string]int{
		".forgejo/workflows/sign-and-publish-release.yml":      4,
		".forgejo/workflows/sign-container-images.yml":         1,
		".forgejo/workflows/promote-base-container-images.yml": 1,
		".forgejo/workflows/verify-base-container-images.yml":  1,
	}
}

var secretReference = regexp.MustCompile(`\$\{\{\s*secrets\s*[.\[]`)

// checkSignerBootstrap verifies the bootstrap contract for every job in the
// workflow that runs inside the signer image.
//
// Each signer job must open with checkout, live checkout verification, the
// download of the engine binary, and signer-toolchain verification, in that
// order, under job-wide strict mode. No secret and no engine command may
// appear before the toolchain verifier has run, because until then nothing
// has established that the toolchain is the reviewed one.
func checkSignerBootstrap(data []byte, wantJobs int) error {
	parsed, err := parseWorkflow(data)
	if err != nil {
		return err
	}

	signerJobCount := 0

	for name, job := range parsed.jobs {
		if job.containerImage() != signerImage {
			continue
		}

		signerJobCount++

		if err := checkSignerJob(name, job); err != nil {
			return err
		}
	}

	if signerJobCount != wantJobs {
		return fmt.Errorf("%w: %d job(s) run in the signer image, want %d", errRule, signerJobCount, wantJobs)
	}

	return nil
}

func checkSignerJob(name string, target job) error {
	steps := target.steps()
	if len(steps) < len(bootstrapSteps()) {
		return fmt.Errorf("%w: job %s has %d steps, want at least %d", errRule, name, len(steps), len(bootstrapSteps()))
	}

	for index, want := range bootstrapSteps() {
		if got := scalarString(mappingValue(steps[index], "name")); got != want {
			return fmt.Errorf("%w: job %s step %d is %q, want %q", errRule, name, index+1, got, want)
		}
	}

	strict, ok := target.env("SIGNER_STRICT")
	if !ok || strict != "1" {
		return fmt.Errorf("%w: job %s must set job-wide SIGNER_STRICT: '1'", errRule, name)
	}

	if !mentions(steps[fetchStep], "fetch_forge_ci") {
		return fmt.Errorf("%w: job %s step %d must download the engine binary with fetch_forge_ci", errRule, name, fetchStep+1)
	}

	if !mentions(steps[toolchainStep], "verify-signer-toolchain.sh") {
		return fmt.Errorf("%w: job %s step %d must run verify-signer-toolchain.sh", errRule, name, toolchainStep+1)
	}

	for index, step := range steps {
		if mentions(step, "SIGNER_STRICT") {
			return fmt.Errorf("%w: job %s step %d overrides SIGNER_STRICT", errRule, name, index+1)
		}

		if index >= len(bootstrapSteps()) {
			continue
		}

		if mentions(step, "run_forge_ci") {
			return fmt.Errorf("%w: job %s runs the engine in step %d, before the toolchain verifier", errRule, name, index+1)
		}

		if anyString(step, secretReference.MatchString) {
			return fmt.Errorf("%w: job %s references a secret in step %d, before the toolchain verifier", errRule, name, index+1)
		}
	}

	return nil
}

// Every signer job in every signer workflow satisfies the bootstrap contract.
func TestSignerJobsBootstrapBeforeSecretsOrEngine(t *testing.T) {
	t.Parallel()

	for relative, wantJobs := range signerWorkflows() {
		if err := checkSignerBootstrap(reporoot.ReadFile(t, relative), wantJobs); err != nil {
			t.Errorf("%s: %v", relative, err)
		}
	}
}

// Only the known signer workflows run jobs inside the signer image. A new one
// would otherwise escape the contract above.
func TestNoUnlistedWorkflowRunsInTheSignerImage(t *testing.T) {
	t.Parallel()

	for _, relative := range layerWorkflows(t) {
		if _, known := signerWorkflows()[relative]; known {
			continue
		}

		parsed := loadWorkflow(t, relative)
		for name, job := range parsed.jobs {
			if job.containerImage() == signerImage {
				t.Errorf("%s job %s runs in the signer image but is not listed in signerWorkflows", relative, name)
			}
		}
	}
}
