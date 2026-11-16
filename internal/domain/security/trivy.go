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

// TrivyDBSourceArgs names where Trivy fetches its vulnerability and Java
// databases: the vendor's own registry, ghcr.io, first. Trivy's default
// puts the mirror.gcr.io pull-through cache first, and the cache has
// answered a layer with 404. Trivy v0.76 moves on to the next repository
// only on a registry error that is temporary (HTTP 408, 429, 500, 502,
// 503 or 504, or error codes that are all TOOMANYREQUESTS, UNAVAILABLE,
// UNKNOWN or BLOB_UPLOAD_INVALID) or that names BLOB_UNKNOWN. Any other
// 404, a denied or unauthorised pull, a DNS failure, a refused
// connection or a dial or I/O timeout stops the scan. So the mirror
// covers registry (HTTP 408/504) timeouts, rate limits, server errors
// and BLOB_UNKNOWN only, and the runner must reach ghcr.io.
// The pull sends the job's registry login for ghcr.io, if it has one
// (DOCKER_CONFIG and REGISTRY_AUTH_FILE are kept), so a stale or
// under-scoped one fails it.
func TrivyDBSourceArgs() []string {
	return []string{
		"--db-repository", "ghcr.io/aquasecurity/trivy-db:2,mirror.gcr.io/aquasec/trivy-db:2",
		"--java-db-repository", "ghcr.io/aquasecurity/trivy-java-db:1,mirror.gcr.io/aquasec/trivy-java-db:1",
	}
}

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
	PkgPath          string   `json:"PkgPath,omitempty"`
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
	ID string
	// Target identifies the affected image file or package database.
	Target    string
	Package   string
	Installed string
	// Fixed is the version that fixes it, empty while there is none.
	Fixed    string
	Severity string
}

// TrivyFindings are a report's findings split by whether a fix exists.
//
// The application decides which severities block, including unfixed CRITICAL
// findings. Classification preserves the affected target so remediation can
// identify the binary or package database rather than just a shared library.
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
			finding := TrivyFinding{ID: v.VulnerabilityID, Target: cmp.Or(v.PkgPath, result.Target), Package: v.PkgName, Installed: v.InstalledVersion,
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

// severityOrder lists Trivy's severities, the most severe first.
var severityOrder = []string{"CRITICAL", "HIGH", "MEDIUM", "LOW", "UNKNOWN"}

func compareFindings(left, right TrivyFinding) int {
	return cmp.Or(
		cmp.Compare(rankOf(left.Severity), rankOf(right.Severity)),
		strings.Compare(left.ID, right.ID),
		strings.Compare(left.Package, right.Package),
		strings.Compare(left.Installed, right.Installed),
		strings.Compare(left.Target, right.Target),
	)
}

// rankOf is a severity's place in severityOrder; an unknown one sorts last.
func rankOf(severity string) int {
	if rank := slices.Index(severityOrder, severity); rank >= 0 {
		return rank
	}

	return len(severityOrder)
}
