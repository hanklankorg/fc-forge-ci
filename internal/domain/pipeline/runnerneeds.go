// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pipeline

import (
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
