// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pipeline

import (
	"fmt"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/domain/publish"
)

// Reject incompatible destinations before preparing a permanent release tag.
// Prerelease support does not silently rewrite store channels or version formats.
func validatePrereleasePublications(plan ConfigPlan, tag string) error {
	if len(plan.Artifacts.XcodeIOS) != 0 {
		return fmt.Errorf("iOS prereleases require a numeric marketing-version mapping, which is not supported: %w", errs.ErrUnsupported)
	}

	for _, artifact := range plan.Artifacts.GooglePlay {
		if artifact.GradleAndroid != nil && strings.EqualFold(strings.TrimSpace(artifact.GradleAndroid.GooglePlayTrack), "production") {
			return fmt.Errorf("prerelease %s cannot publish %q to Google Play production; select a testing track: %w", tag, artifact.Name, errs.ErrInvalidConfig)
		}
	}

	// These types publish under a Maven coordinate version.
	mavenVersioned := []projecttype.Type{projecttype.Maven, projecttype.Gradle, projecttype.GradleAndroid}

	for _, targets := range [][]PlannedArtifact{plan.Artifacts.MavenCentral, plan.Artifacts.ForgePackages} {
		for _, artifact := range targets {
			if !projecttype.IsIn(artifact.ProjectType, mavenVersioned) {
				continue
			}

			if err := publish.ValidateMavenVersion(strings.TrimPrefix(tag, "v"), false); err != nil {
				return fmt.Errorf("immutable Maven publication %q: %w", artifact.Name, err)
			}
		}
	}

	return nil
}
