// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pipeline

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/domain/runners"
	"github.com/diggsweden/forge-ci/internal/listval"
)

const (
	imageBuildJobs = "image builds"
	// macOSPlatform is the platform every Xcode and App Store job runs on.
	macOSPlatform = "darwin/arm64"
)

// releaseRunnerNeeds is the platform-bound runners a release plan's jobs run
// on: an image build runner for each platform its containers build, after
// RELEASE_PLATFORMS narrowed them, and a macOS runner for its Xcode jobs.
func releaseRunnerNeeds(plan ConfigPlan) []runners.Need {
	var needs []runners.Need

	for _, container := range plan.Containers.All {
		for _, platform := range listval.Tokens(container.Platforms) {
			needs = append(needs, runners.Need{Role: runners.Build, Platform: platform, Jobs: imageBuildJobs})
		}
	}

	if len(plan.Artifacts.XcodeIOS) != 0 {
		needs = append(needs, runners.Need{Role: runners.Ordinary, Platform: macOSPlatform, Jobs: "Xcode and App Store jobs"})
	}

	return needs
}

// RunnerNeeds is the image build runner each planned build runs on.
func (plan ReleaseImagesPlan) RunnerNeeds() []runners.Need {
	needs := make([]runners.Need, 0, len(plan.Builds))

	for _, build := range plan.Builds {
		needs = append(needs, runners.Need{Role: runners.Build, Platform: "linux/" + build.Arch, Jobs: imageBuildJobs})
	}

	return needs
}

// AdmitSignedXcodeRunner checks the macOS runner the caller of the GitHub
// layer named against the administrator's allowlist, for a build stage with a
// signed Xcode build: the one job that holds signing material and runs where
// its caller says. stagePlanJSON is the release or snapshot build-stage plan,
// read as build-xcode-ios.yml's caller reads it: a build signs unless its
// declaration says enable_code_signing false. runsOn and allowed are the
// values of runs-on-darwin-arm64 and runners.SignerAllowlistInput. The list
// is read whatever the plan holds, so a malformed one is never passed over.
func AdmitSignedXcodeRunner(stagePlanJSON, runsOn, allowed string) error {
	allowlist, err := runners.SignerAllowlist(allowed)
	if err != nil {
		return err
	}

	// The part the release and the snapshot build-stage plans share.
	var stage struct {
		Targets struct {
			XcodeIOS *TargetPlan[PlannedArtifact] `json:"xcode_ios"`
		} `json:"targets"`
	}

	if err := json.Unmarshal([]byte(stagePlanJSON), &stage); err != nil {
		return fmt.Errorf("parse build-stage plan: %w: %w", err, errs.ErrInvalidConfig)
	}

	xcode := stage.Targets.XcodeIOS
	if xcode == nil || xcode.Runs != (len(xcode.Items) > 0) {
		return fmt.Errorf("the build-stage plan has no coherent xcode_ios target to read the signed builds from: %w", errs.ErrInvalidConfig)
	}

	signs := func(artifact PlannedArtifact) bool {
		return artifact.XcodeIOS == nil || artifact.XcodeIOS.EnableCodeSigning == nil || *artifact.XcodeIOS.EnableCodeSigning
	}
	if !slices.ContainsFunc(xcode.Items, signs) {
		return nil
	}

	input, err := runners.Input(provider.ForgeGitHub, runners.Ordinary, macOSPlatform)
	if err != nil {
		return err
	}

	return runners.AdmitSignerRunner(input, runsOn, allowlist)
}
