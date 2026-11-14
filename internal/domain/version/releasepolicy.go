// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version

import (
	"fmt"
	"golang.org/x/mod/semver"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

const (
	// ReleasePolicyStable permits stable versions only and is the default.
	ReleasePolicyStable = "stable"
	// ReleasePolicyStablePrerelease also permits immutable prerelease versions.
	ReleasePolicyStablePrerelease = "stable-prerelease"
)

// ValidateReleaseTag admits a version under trusted caller policy. Empty policy
// is stable-only; build metadata is not a distinct publishable release identity.
func ValidateReleaseTag(tag, policy string) (Semver, error) {
	if err := ValidateReleasePolicy(policy); err != nil {
		return Semver{}, err
	}

	parsed, ok := ParseSemverTag(tag)
	if !ok || parsed.Build != "" {
		return Semver{}, fmt.Errorf("release tag must be vMAJOR.MINOR.PATCH with an optional prerelease and no build metadata (got %q): %w", tag, errs.ErrValidation)
	}

	if parsed.Prerelease != "" && policy != ReleasePolicyStablePrerelease {
		return Semver{}, fmt.Errorf("release tag must be stable vMAJOR.MINOR.PATCH under release-policy stable (got %q): %w", tag, errs.ErrValidation)
	}

	return parsed, nil
}

// ValidateReleasePolicy rejects unknown modes, including legacy overrides.
func ValidateReleasePolicy(policy string) error {
	if policy != "" && policy != ReleasePolicyStable && policy != ReleasePolicyStablePrerelease {
		return fmt.Errorf("release-policy must be stable or stable-prerelease (got %q): %w", policy, errs.ErrUsage)
	}

	return nil
}

// LatestReleaseTag compares admitted versions numerically, including beta.10.
func LatestReleaseTag(tags []string, policy string) string {
	var latest string

	for _, tag := range tags {
		if _, err := ValidateReleaseTag(tag, policy); err != nil {
			continue
		}

		if latest == "" || semver.Compare(tag, latest) > 0 {
			latest = tag
		}
	}

	return latest
}
