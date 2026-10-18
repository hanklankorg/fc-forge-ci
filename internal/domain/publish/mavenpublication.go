// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish

import (
	"encoding/xml"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// MavenPublication is a resolved exported POM, not the executable project model.
type MavenPublication struct {
	XMLName     xml.Name `xml:"project"`
	GroupID     string   `xml:"groupId"`
	ArtifactID  string   `xml:"artifactId"`
	Version     string   `xml:"version"`
	Packaging   string   `xml:"packaging"`
	Name        string   `xml:"name"`
	Description string   `xml:"description"`
	URL         string   `xml:"url"`
	Licenses    []struct {
		Name string `xml:"name"`
		URL  string `xml:"url"`
	} `xml:"licenses>license"`
	Developers []struct {
		ID   string `xml:"id"`
		Name string `xml:"name"`
	} `xml:"developers>developer"`
	SCM struct {
		URL string `xml:"url"`
	} `xml:"scm"`
}

//nolint:gochecknoglobals // immutable coordinate grammar; no paths or expressions.
var mavenCoordinate = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

// Directory is the Maven repository directory of this resolved publication.
func (p MavenPublication) Directory() string {
	return path.Join(strings.ReplaceAll(p.GroupID, ".", "/"), p.ArtifactID, p.Version)
}

// Stem is the filename prefix used by all artifacts of this publication.
func (p MavenPublication) Stem() string { return p.ArtifactID + "-" + p.Version }

// ParseMavenPublication accepts the first supported scope: immutable JVM jar
// and POM publications. Snapshot/timestamp layouts must never be guessed.
func ParseMavenPublication(body []byte, expectedVersion string, central bool) (MavenPublication, error) {
	return parseMavenPublication(body, expectedVersion, central, false)
}

// ParseMavenSnapshot validates resolved snapshot coordinates. An empty expected
// version permits independently versioned reactor modules, but never a release.
func ParseMavenSnapshot(body []byte, expectedVersion string, centralMetadata bool) (MavenPublication, error) {
	return parseMavenPublication(body, expectedVersion, centralMetadata, true)
}

func parseMavenPublication(body []byte, expectedVersion string, central, snapshot bool) (MavenPublication, error) {
	var publication MavenPublication
	if err := xml.Unmarshal(body, &publication); err != nil {
		return publication, fmt.Errorf("exported POM is invalid: %w", errs.ErrMalformedInput)
	}

	if publication.Packaging == "" {
		publication.Packaging = "jar"
	}

	if snapshot && expectedVersion == "" {
		expectedVersion = publication.Version
	}

	if err := publication.validateCoordinates(expectedVersion, snapshot); err != nil {
		return publication, err
	}

	if central {
		return publication, publication.validateCentralMetadata()
	}

	return publication, nil
}

func (p MavenPublication) validateCoordinates(expectedVersion string, snapshot bool) error {
	if err := ValidateMavenVersion(expectedVersion, snapshot); err != nil {
		return err
	}

	if !mavenCoordinate.MatchString(p.GroupID) || !mavenCoordinate.MatchString(p.ArtifactID) || strings.Contains(p.GroupID, "..") || p.Version != expectedVersion {
		return fmt.Errorf("exported POM must have resolved coordinates and version %q: %w", expectedVersion, errs.ErrValidation)
	}

	if p.Packaging != "jar" && p.Packaging != "pom" && p.Packaging != "aar" {
		return fmt.Errorf("artifact-first Maven publication supports jar, aar and pom packaging, got %q: %w", p.Packaging, errs.ErrUnsupported)
	}

	return nil
}

func (p MavenPublication) validateCentralMetadata() error {
	for _, required := range []string{p.Name, p.Description, p.URL, p.SCM.URL} {
		if strings.TrimSpace(required) == "" {
			return fmt.Errorf("central publication POM requires name, description, URL and SCM metadata: %w", errs.ErrValidation)
		}
	}

	if len(p.Licenses) == 0 || len(p.Developers) == 0 {
		return fmt.Errorf("central publication POM requires license and developer metadata: %w", errs.ErrValidation)
	}

	return p.validateCentralPeopleAndLicenses()
}

func (p MavenPublication) validateCentralPeopleAndLicenses() error {
	for _, license := range p.Licenses {
		if strings.TrimSpace(license.Name) == "" || strings.TrimSpace(license.URL) == "" {
			return fmt.Errorf("central license metadata requires a name and URL: %w", errs.ErrValidation)
		}
	}

	for _, developer := range p.Developers {
		if strings.TrimSpace(developer.ID) == "" && strings.TrimSpace(developer.Name) == "" {
			return fmt.Errorf("central developer metadata requires an ID or name: %w", errs.ErrValidation)
		}
	}

	return nil
}
