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
	policy := version.ReleasePolicyStablePrerelease

	if !snapshot && strings.HasSuffix(value, "-SNAPSHOT") {
		return fmt.Errorf("immutable publication cannot use a -SNAPSHOT version: %w", errs.ErrValidation)
	}

	if snapshot {
		policy = version.ReleasePolicyStable

		var ok bool

		base, ok = strings.CutSuffix(value, "-SNAPSHOT")
		if !ok {
			return fmt.Errorf("snapshot publication requires a -SNAPSHOT version: %w", errs.ErrValidation)
		}
	}

	if _, err := version.ValidateReleaseTag("v"+base, policy); err != nil {
		return fmt.Errorf("publication requires MAJOR.MINOR.PATCH, with -SNAPSHOT only in snapshot mode: %w", errs.ErrValidation)
	}

	return nil
}

// ValidateMavenReactorVersions checks the resolved effective POM of every selected
// reactor project. A snapshot flag or a root project's version alone is not proof
// that the reactor cannot deploy an immutable release.
func ValidateMavenReactorVersions(body []byte, snapshot bool, expected string) error {
	projects, err := effectiveProjects(body)
	if err != nil {
		return err
	}

	for index, item := range projects {
		if err := ValidateMavenVersion(item.Version, snapshot); err != nil {
			return fmt.Errorf("maven reactor project %d: %w", index+1, err)
		}

		if !snapshot && item.Version != expected {
			return fmt.Errorf("maven reactor project %d does not match the authorized release version: %w", index+1, errs.ErrPermissionDenied)
		}
	}

	return nil
}

// MavenReactorCoordinates returns the groupId:artifactId of every project of
// a resolved effective POM, in reactor order: what a deploy of that reactor
// may publish.
func MavenReactorCoordinates(body []byte) ([]string, error) {
	projects, err := effectiveProjects(body)
	if err != nil {
		return nil, err
	}

	coordinates := make([]string, 0, len(projects))

	for index, item := range projects {
		if item.GroupID == "" || item.ArtifactID == "" {
			return nil, fmt.Errorf("maven reactor project %d has no groupId or artifactId: %w", index+1, errs.ErrMalformedInput)
		}

		coordinates = append(coordinates, item.GroupID+":"+item.ArtifactID)
	}

	return coordinates, nil
}

// effectiveProject is one project of a resolved effective POM.
type effectiveProject struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
	Version    string `xml:"version"`
}

// effectiveProjects returns the projects of a resolved effective POM: the
// one project, or every project of a reactor.
func effectiveProjects(body []byte) ([]effectiveProject, error) {
	var model struct {
		effectiveProject

		XMLName  xml.Name           `xml:""`
		Projects []effectiveProject `xml:"project"`
	}
	if err := xml.Unmarshal(body, &model); err != nil {
		return nil, fmt.Errorf("invalid Maven effective POM: %w", errs.ErrMalformedInput)
	}

	switch model.XMLName.Local {
	case "project":
		model.Projects = []effectiveProject{model.effectiveProject}
	case "projects":
	default:
		return nil, fmt.Errorf("expected Maven project or reactor effective POM: %w", errs.ErrMalformedInput)
	}

	if len(model.Projects) == 0 {
		return nil, fmt.Errorf("maven reactor has no resolved projects: %w", errs.ErrMissingInput)
	}

	return model.Projects, nil
}
