// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"fmt"
	"maps"
	"runtime"
	"slices"
	"strings"

	"codefloe.com/itiquette/forge-lab-contract/labcontract"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// releasePlatformsVariable is the consumer variable that narrows the platforms
// a release builds.
const releasePlatformsVariable = "RELEASE_PLATFORMS"

// capabilityWorkflowRuns marks an endpoint whose jobs reach a runner.
const capabilityWorkflowRuns = "workflow-runs"

// LabRunnerArch is the architecture the current contract's runners execute,
// for the launcher, which builds this tier's runner binaries for it.
func LabRunnerArch() (string, error) {
	lab, err := currentLabContract()
	if err != nil {
		return "", err
	}

	return labRunnerArch(lab)
}

// labRunnerArch is the one architecture the runners of the endpoints this tier
// drives execute. Runners that publish none, or differ, are refused: the tier
// builds for one runner architecture and never emulates another. A lab with no
// runner runs nothing there, so the host's own architecture is as good as any.
func labRunnerArch(lab *labcontract.Contract) (string, error) {
	scope, err := liveProviderScope()
	if err != nil {
		return "", err
	}
	arch := ""

	for _, forge := range liveForges {
		if scope != "" && scope != forge {
			continue
		}
		endpoint, err := lab.Endpoint(labcontract.Kind(forge))
		if errors.Is(err, labcontract.ErrNoEndpoint) {
			continue
		}

		if err != nil {
			return "", err
		}

		if !slices.Contains(endpoint.Capabilities, capabilityWorkflowRuns) {
			continue
		}

		runnerArch := runnerOf(endpoint).Arch

		switch {
		case runnerArch == "":
			return "", fmt.Errorf("endpoint %q runs workflows but its contract publishes no runner.arch; emit the contract again with a Forge Lab that publishes it: %w",
				endpoint.Name, errs.ErrValidation)
		case arch == "":
			arch = runnerArch
		case runnerArch != arch:
			return "", fmt.Errorf("the lab's runners execute %s and %s, and this tier builds for one runner architecture: %w",
				arch, runnerArch, errs.ErrValidation)
		}
	}

	if arch == "" {
		arch = runtime.GOARCH
	}

	return arch, nil
}

// RunnerArch is the architecture the contract says this endpoint's runner
// executes.
func RunnerArch(tb TB, target Target) string {
	tb.Helper()
	requireAccepted(tb, target)

	arch := runnerOf(target.endpoint).Arch
	if arch == "" {
		tb.Fatalf("livetest: the contract publishes no runner.arch for endpoint %q; emit it again with a Forge Lab that publishes it",
			target.endpoint.Name)
	}

	return arch
}

// runnerOf is the runner the endpoint's jobs reach, empty when the contract
// publishes none.
func runnerOf(endpoint labcontract.Endpoint) labcontract.Runner {
	if endpoint.Runner == nil {
		return labcontract.Runner{}
	}

	return *endpoint.Runner
}

// NativeReleasePlatforms returns a trial's consumer variables with the release
// platforms set to the lab runner's own when the caller left them out. A
// selection naming another architecture is refused before anything is pushed:
// a release builds and runs each selected platform on the runner, and never
// under CPU emulation.
func NativeReleasePlatforms(variables map[string]string, runnerArch string) (map[string]string, error) {
	resolved := maps.Clone(variables)
	if resolved == nil {
		resolved = map[string]string{}
	}

	selection := resolved[releasePlatformsVariable]
	if selection == "" {
		resolved[releasePlatformsVariable] = "linux/" + runnerArch

		return resolved, nil
	}

	for platform := range strings.SplitSeq(selection, ",") {
		if _, arch, _ := strings.Cut(platform, "/"); arch != runnerArch {
			return nil, fmt.Errorf("%s=%s selects %s, but the lab's runners execute %s; a release never builds another architecture under emulation, so select linux/%s or run the trial on a lab of that architecture: %w",
				releasePlatformsVariable, selection, platform, runnerArch, runnerArch, errs.ErrValidation)
		}
	}

	return resolved, nil
}
