// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish

import (
	"fmt"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// NPMVersionTagTemplate explicitly selects a tag bound to the packed version.
const NPMVersionTagTemplate = "release-{version}"

// NPMDottedVersionTagTemplate selects the Forgejo-compatible immutable alias.
const NPMDottedVersionTagTemplate = "release.{version}"

// ValidateNPMTag checks a publication setting before package or registry access.
// No channel is implicit: latest, next, snapshot and other movable names refuse.
func ValidateNPMTag(tag string) error {
	if tag == NPMVersionTagTemplate || tag == NPMDottedVersionTagTemplate {
		return nil
	}

	version, ok := strings.CutPrefix(tag, "release-")
	if !ok {
		version, ok = strings.CutPrefix(tag, "release.")
	}

	canonical, err := NPMRegistryVersion(version)
	if !ok || err != nil || canonical != version {
		return fmt.Errorf("npm-tag must be explicitly set to release-{version}, release.{version}, release-<package-version> or release.<package-version>; movable tags such as latest and next are forbidden: %w", errs.ErrInvalidConfig)
	}

	return nil
}

// NPMVersionTag resolves the explicit setting against the packed package, never
// the Git ref or the checkout's manifest. npm strips SemVer build metadata from
// registry versions, so the bound tag uses that same canonical version.
func NPMVersionTag(version, tag string) (string, error) {
	if err := ValidateNPMTag(tag); err != nil {
		return "", err
	}

	canonical, err := NPMRegistryVersion(version)
	if err != nil {
		return "", err
	}

	prefix, template := "release-", NPMVersionTagTemplate
	if strings.HasPrefix(tag, "release.") {
		prefix, template = "release.", NPMDottedVersionTagTemplate
	}

	want := prefix + canonical
	if tag != template && tag != want {
		return "", fmt.Errorf("npm-tag %q does not match packed version %q; use %q or %s: %w", tag, version, want, template, errs.ErrInvalidConfig)
	}

	return want, nil
}

// NPMRegistryVersion matches npm's publication identity: complete SemVer with
// prerelease identifiers retained and build metadata removed.
func NPMRegistryVersion(version string) (string, error) {
	if !validNPMVersion(version) {
		return "", fmt.Errorf("packed npm version must be a complete SemVer version: %w", errs.ErrInvalidConfig)
	}

	return strings.TrimPrefix(semver.Canonical("v"+version), "v"), nil
}

func validNPMVersion(version string) bool {
	base, _, _ := strings.Cut(version, "+")
	base, _, _ = strings.Cut(base, "-")

	return strings.Count(base, ".") == 2 && !strings.HasPrefix(version, "v") && semver.IsValid("v"+version)
}
