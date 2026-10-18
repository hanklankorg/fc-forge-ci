// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package config

import (
	"fmt"
	"slices"

	"github.com/diggsweden/forge-ci/internal/domain/build"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

func validateAndroidLibraryOptions(artifact Artifact) []string {
	if artifact.ProjectType != projecttype.GradleAndroid || artifact.BuildType != BuildTypeLibrary {
		return nil
	}

	violations := androidLibrarySBOMViolations(artifact)

	options := artifact.GradleAndroid
	if options == nil {
		return violations
	}

	for _, option := range []struct {
		name  string
		value *bool
	}{{"include-aab", options.IncludeAAB}, {"enable-android-signing", options.EnableAndroidSigning}} {
		if option.value != nil && *option.value {
			violations = append(violations, fmt.Sprintf("artifact %q: Android libraries cannot enable config.%s", artifact.Name, option.name))
		}
	}

	if options.BuildTypes != "" {
		variants, err := build.ParseAndroidBuildTypes(options.BuildTypes)
		if err != nil || variants.Debug {
			violations = append(violations, fmt.Sprintf("artifact %q: Android library publication requires the release build type", artifact.Name))
		}
	}

	return violations
}

func androidLibrarySBOMViolations(artifact Artifact) []string {
	if artifact.SBOMs == "" {
		return nil
	} // The library default is the supported build layer.

	layers, err := ExpandSBOMs(artifact.SBOMs)
	if err == nil && slices.Contains(layers, SBOMLayerAnalyzedArtifact) {
		return []string{fmt.Sprintf("artifact %q: Android library analyzed-artifact SBOMs are not supported; select sboms: build", artifact.Name)}
	}

	return nil
}
