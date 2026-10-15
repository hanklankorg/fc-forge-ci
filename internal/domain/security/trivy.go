// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package security holds pure security-report transforms.
//
// Trivy emits its native JSON. GitLab CI ingests container-scanning and
// SAST reports in a different schema (security-report-schemas v15.x). The transforms in this package are JSON-in / JSON-out, no I/O,
// no subprocess.
package security

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// Default file paths for the container-scan outputs. Single source of
// truth: the CLI flag defaults and the app-layer empty-string fallbacks
// reference these constants.
const (
	DefaultTrivyContainerJSONFile   = "trivy-results.json"
	DefaultTrivyContainerSARIFFile  = "trivy-results.sarif"
	DefaultTrivyGitLabContainerFile = "gl-container-scanning-report.json"
)

// ParseTrivyReport decodes consumed fields and requires a Results array (possibly
// empty) or a nonblank ArtifactName. Omitted and null Results decode alike.
// Unknown fields are allowed; this is not full native-schema validation.
func ParseTrivyReport(body []byte) (*TrivyReport, error) {
	var report *TrivyReport
	if err := json.Unmarshal(body, &report); err != nil {
		return nil, fmt.Errorf("parse Trivy report: %w: %w", err, errs.ErrMalformedInput)
	}

	if report == nil {
		return nil, fmt.Errorf("trivy report must be an object, not null: %w", errs.ErrMalformedInput)
	}

	if report.Results == nil && strings.TrimSpace(report.ArtifactName) == "" {
		return nil, fmt.Errorf("trivy report requires a Results array or nonblank ArtifactName: %w", errs.ErrMalformedInput)
	}

	return report, nil
}

// TrivyReport is the subset of Trivy's JSON output we care about.
// Fields not used by the transforms are omitted; encoding/json ignores
// extra keys silently.
type TrivyReport struct {
	ArtifactName string         `json:"ArtifactName,omitempty"`
	Metadata     *TrivyMetadata `json:"Metadata,omitempty"`
	Results      []TrivyResult  `json:"Results,omitempty"`
}

// TrivyMetadata carries OS / image fingerprint context for container scans.
type TrivyMetadata struct {
	OS *TrivyOS `json:"OS,omitempty"`
}

// TrivyOS is the OS family/name pair we surface as operating_system.
type TrivyOS struct {
	Family string `json:"Family,omitempty"`
	Name   string `json:"Name,omitempty"`
}

// TrivyResult is one scan target (a package-lock.json, a layer, etc.).
type TrivyResult struct {
	Target          string               `json:"Target,omitempty"`
	Class           string               `json:"Class,omitempty"`
	Vulnerabilities []TrivyVulnerability `json:"Vulnerabilities,omitempty"`
}

// TrivyVulnerability is one finding.
type TrivyVulnerability struct {
	VulnerabilityID  string   `json:"VulnerabilityID,omitempty"`
	PkgName          string   `json:"PkgName,omitempty"`
	InstalledVersion string   `json:"InstalledVersion,omitempty"`
	FixedVersion     string   `json:"FixedVersion,omitempty"`
	Title            string   `json:"Title,omitempty"`
	Description      string   `json:"Description,omitempty"`
	Severity         string   `json:"Severity,omitempty"`
	PrimaryURL       string   `json:"PrimaryURL,omitempty"`
	References       []string `json:"References,omitempty"`
}

// OSDescription returns "<Family> <Name>" trimmed, or "" when no OS info.
func (r *TrivyReport) OSDescription() string {
	if r == nil || r.Metadata == nil || r.Metadata.OS == nil {
		return ""
	}

	os := r.Metadata.OS
	switch {
	case os.Family != "" && os.Name != "":
		return os.Family + " " + os.Name
	case os.Family != "":
		return os.Family
	default:
		return os.Name
	}
}

// TrivyFinding is one vulnerability of one installed package.
type TrivyFinding struct {
	ID        string
	Package   string
	Installed string
	// Fixed is the version that fixes it, empty while there is none.
	Fixed    string
	Severity string
}

// TrivyFindings are a report's findings split by whether a fix exists.
//
// A release gate blocks on Fixable only. An Unfixed finding has no fixed
// package yet, so neither the consumer nor a rebuild can act on it: blocking
// on it stops every release of an affected base until upstream ships a fix,
// and teaches people to turn the scan off. Unfixed findings are still
// reported, in the scan reports and as a warning.
type TrivyFindings struct {
	Fixable []TrivyFinding
	Unfixed []TrivyFinding
}

// ClassifyTrivyFindings reads a Trivy JSON report into findings, each
// vulnerability of each package once, the most severe first. A finding is
// fixable when Trivy names a fixed version, the rule `trivy --ignore-unfixed`
// applies.
func ClassifyTrivyFindings(body []byte) (TrivyFindings, error) {
	if len(body) == 0 {
		return TrivyFindings{}, nil
	}

	doc, err := ParseTrivyReport(body)
	if err != nil {
		return TrivyFindings{}, err
	}

	var findings TrivyFindings

	seen := map[TrivyFinding]bool{}

	for _, result := range doc.Results {
		for _, v := range result.Vulnerabilities {
			finding := TrivyFinding{ID: v.VulnerabilityID, Package: v.PkgName, Installed: v.InstalledVersion,
				Fixed: v.FixedVersion, Severity: strings.ToUpper(v.Severity)}
			if finding.ID == "" || seen[finding] {
				continue
			}

			seen[finding] = true

			if finding.Fixed != "" {
				findings.Fixable = append(findings.Fixable, finding)
			} else {
				findings.Unfixed = append(findings.Unfixed, finding)
			}
		}
	}

	slices.SortFunc(findings.Fixable, compareFindings)
	slices.SortFunc(findings.Unfixed, compareFindings)

	return findings, nil
}

// severityRank orders Trivy's severities, the most severe first.
var severityRank = map[string]int{"CRITICAL": 0, "HIGH": 1, "MEDIUM": 2, "LOW": 3, "UNKNOWN": 4} //nolint:gochecknoglobals // read-only table.

func compareFindings(a, b TrivyFinding) int {
	return cmp.Or(
		cmp.Compare(rankOf(a.Severity), rankOf(b.Severity)),
		strings.Compare(a.ID, b.ID),
		strings.Compare(a.Package, b.Package),
		strings.Compare(a.Installed, b.Installed),
	)
}

func rankOf(severity string) int {
	if rank, ok := severityRank[severity]; ok {
		return rank
	}

	return len(severityRank)
}
