// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish

import (
	"encoding/xml"
	"fmt"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/version"
)

// ValidateMavenVersion keeps snapshot publication distinct from immutable releases.
func ValidateMavenVersion(value string, snapshot bool) error {
	base := value

	if snapshot {
		var ok bool

		base, ok = strings.CutSuffix(value, "-SNAPSHOT")
		if !ok {
			return fmt.Errorf("snapshot publication requires a -SNAPSHOT version: %w", errs.ErrValidation)
		}
	}

	if !version.IsStableSemverTag("v" + base) {
		return fmt.Errorf("publication requires MAJOR.MINOR.PATCH, with -SNAPSHOT only in snapshot mode: %w", errs.ErrValidation)
	}

	return nil
}

// ValidateMavenReactorVersions checks the resolved effective POM of every selected
// reactor project. A snapshot flag or a root project's version alone is not proof
// that the reactor cannot deploy an immutable release.
func ValidateMavenReactorVersions(body []byte, snapshot bool, expected string) error {
	type project struct {
		Version string `xml:"version"`
	}

	var model struct {
		XMLName  xml.Name  `xml:""`
		Version  string    `xml:"version"`
		Projects []project `xml:"project"`
	}
	if err := xml.Unmarshal(body, &model); err != nil {
		return fmt.Errorf("invalid Maven effective POM: %w", errs.ErrMalformedInput)
	}

	switch model.XMLName.Local {
	case "project":
		model.Projects = []project{{Version: model.Version}}
	case "projects":
	default:
		return fmt.Errorf("expected Maven project or reactor effective POM: %w", errs.ErrMalformedInput)
	}

	if len(model.Projects) == 0 {
		return fmt.Errorf("maven reactor has no resolved projects: %w", errs.ErrMissingInput)
	}

	for index, item := range model.Projects {
		if err := ValidateMavenVersion(item.Version, snapshot); err != nil {
			return fmt.Errorf("maven reactor project %d: %w", index+1, err)
		}

		if !snapshot && item.Version != expected {
			return fmt.Errorf("maven reactor project %d does not match the authorized release version: %w", index+1, errs.ErrPermissionDenied)
		}
	}

	return nil
}
