// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"time"

	"codefloe.com/itiquette/forge-lab-contract/labcontract"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

const (
	maxCABytes     = 1024 * 1024
	profileFull    = "full"
	profileFocused = "focused"
)

// LivePreflightSummary is credential-free output for the shell entrypoint.
// Explanation is the module's rendering of the contract with the run identity
// this suite's prefix derives: what the run will touch, for the operator and
// the run log.
type LivePreflightSummary struct {
	Selected    int
	Generation  string
	Explanation string
}

// CheckLiveInputs locates and validates the contract, the transport CA and the
// destructive guard exactly as the scenarios will, without touching a
// provider. It runs before anything is built so a missing or stale contract is
// reported in seconds, with where the suite looked for it.
func CheckLiveInputs(now time.Time) (LivePreflightSummary, error) {
	lab, err := currentLabContract()
	if err != nil {
		return LivePreflightSummary{}, err
	}

	selected := 0

	for _, forge := range liveForges {
		accepted, err := checkLiveForge(lab, forge, now)
		if err != nil {
			return LivePreflightSummary{}, err
		}

		if accepted {
			selected++
		}
	}

	if selected == 0 {
		return LivePreflightSummary{}, fmt.Errorf("the contract carries no endpoint this tier drives (gitlab or forgejo): %w", errs.ErrValidation)
	}

	if profileErr := validateLiveProfile(lab, selected); profileErr != nil {
		return LivePreflightSummary{}, profileErr
	}

	if lab.CAFile == nil {
		return LivePreflightSummary{}, fmt.Errorf("live targets require a non-null ca_file: %w", errs.ErrValidation)
	}

	if _, err := readCAFile(*lab.CAFile); err != nil {
		return LivePreflightSummary{}, err
	}

	return LivePreflightSummary{
		Selected:    selected,
		Generation:  lab.Generation.ID,
		Explanation: labcontract.Explain(lab, now),
	}, nil
}

// checkLiveForge runs the guard for one forge exactly as Accept will, and
// reports whether the contract carried it at all.
func checkLiveForge(lab *labcontract.Contract, forge provider.ForgeAPI, now time.Time) (bool, error) {
	_, err := acceptTarget(lab, forge, now)
	if errors.Is(err, labcontract.ErrNoEndpoint) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("%s destructive guard: %w", forge, err)
	}

	return true, nil
}

// validateLiveProfile is what makes `full` mean full: both forges present, each
// with a deployed runner and a Fulcio issuer mapping, so a green full run
// cannot have quietly skipped the in-runner or keyless half of the matrix.
func validateLiveProfile(lab *labcontract.Contract, selected int) error {
	profile := os.Getenv(liveProfileEnv)
	switch profile {
	case "", profileFocused:
		return nil
	case profileFull:
	default:
		return fmt.Errorf("%s must be full or focused, got %q: %w", liveProfileEnv, profile, errs.ErrValidation)
	}

	if selected != len(liveForges) {
		return fmt.Errorf("full live profile requires both gitlab and forgejo, selected %d: %w", selected, errs.ErrValidation)
	}

	for _, forge := range liveForges {
		endpoint, err := lab.Endpoint(labcontract.Kind(forge))
		if err != nil {
			return refused(err)
		}

		if !endpoint.HasCapability(labcontract.CapabilityWorkflowRuns) {
			return fmt.Errorf("full live profile endpoint %q has no deployed runner (capability workflow-runs): %w", endpoint.Name, errs.ErrValidation)
		}

		if _, _, ok := lab.FulcioFor(endpoint.Name); !ok {
			return fmt.Errorf("full live profile endpoint %q has no Fulcio issuer mapping: %w", endpoint.Name, errs.ErrValidation)
		}
	}

	return nil
}

// readCAFile reads the contract's transport CA: a bounded PEM file holding
// certificates and nothing else.
func readCAFile(path string) ([]byte, error) {
	info, err := os.Stat(path) //nolint:gosec // The contract's own ca_file, which the contract module accepted as a clean absolute path.
	if err != nil {
		return nil, fmt.Errorf("inspect ca_file: %w", err)
	}

	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxCABytes {
		return nil, fmt.Errorf("ca_file must be a regular file no larger than %d bytes: %w", maxCABytes, errs.ErrValidation)
	}

	body, err := os.ReadFile(path) //nolint:gosec // The contract's own CA path, validated above.
	if err != nil {
		return nil, fmt.Errorf("read ca_file: %w", err)
	}

	if err := validateCertificatePEM(body); err != nil {
		return nil, err
	}

	return bytes.Clone(body), nil
}
