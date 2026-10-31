// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package enforcement

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

const (
	nanoCurated  = "curated"
	failedStatus = "fail"
)

type nanoResult struct {
	Properties struct {
		Source          string `json:"source"`
		CheckID         string `json:"check_id"`
		Class           string `json:"class"`
		Status          string `json:"status"`
		FailureCategory string `json:"failure_reason_category"`
		Details         string `json:"details"`
	} `json:"properties"`
	Suppressions []struct {
		Status string `json:"status"`
	} `json:"suppressions"`
}

// NanolinterReport keeps lint and vulnerability decisions independent while
// retaining the security SARIF consumed by existing provider upload steps.
type NanolinterReport struct {
	LintFindings          int
	VulnerabilityFindings int
	SecuritySARIF         []byte
}

// ParseNanolinter requires the full SARIF of this invocation, not a pre-existing
// consumer artifact. Unknown/external finding classes fall under both gates:
// disabling one class cannot turn an unclassified vulnerability into a waiver.
func ParseNanolinter(body []byte) (NanolinterReport, error) {
	var document struct {
		Version string `json:"version"`
		Runs    []struct {
			Tool struct {
				Driver struct {
					Name string `json:"name"`
				} `json:"driver"`
			} `json:"tool"`
			Results []json.RawMessage `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(body, &document); err != nil || document.Version != "2.1.0" || len(document.Runs) != 1 || document.Runs[0].Tool.Driver.Name != "nanolinter" || document.Runs[0].Results == nil {
		return NanolinterReport{}, fmt.Errorf("expected this invocation's full Nanolinter SARIF: %w", errs.ErrMalformedInput)
	}

	var report NanolinterReport

	securityResults := []json.RawMessage{}

	for _, raw := range document.Runs[0].Results {
		result, err := parseNanoResult(raw)
		if err != nil {
			return NanolinterReport{}, err
		}

		if result.Properties.Class == "security" || result.Properties.Source != nanoCurated {
			securityResults = append(securityResults, raw)
		}

		lint, vulnerabilities := result.findings()
		report.LintFindings += lint
		report.VulnerabilityFindings += vulnerabilities
	}

	var err error

	report.SecuritySARIF, err = securitySARIF(body, securityResults)

	return report, err
}

func parseNanoResult(raw []byte) (nanoResult, error) {
	var result nanoResult
	if err := json.Unmarshal(raw, &result); err != nil || (result.Properties.Source != nanoCurated && result.Properties.Source != "external") || (result.Properties.Source == nanoCurated && result.Properties.CheckID == "") {
		return nanoResult{}, fmt.Errorf("invalid nanolinter finding: %w", errs.ErrMalformedInput)
	}

	if result.Properties.Source == nanoCurated {
		switch result.Properties.Status {
		case "pass", "skip", "n/a", failedStatus:
		default:
			return nanoResult{}, fmt.Errorf("unknown nanolinter check status: %w", errs.ErrMalformedInput)
		}
	}

	if err := result.runtimeError(); err != nil {
		return nanoResult{}, err
	}

	return result, nil
}

func (result nanoResult) runtimeError() error {
	if result.Properties.Status != failedStatus {
		return nil
	}

	switch result.Properties.FailureCategory {
	case "", "format_findings", "policy_failure", "security_findings", "code_findings":
	default:
		return fmt.Errorf("nanolinter check %s could not complete (%s): %w", result.Properties.CheckID, result.Properties.FailureCategory, errs.ErrDependencyUnavailable)
	}
	// Older reports may return 1 for a scanner's operational failure.
	details := strings.ToLower(strings.TrimSpace(result.Properties.Details))
	for _, prefix := range []string{"tool failed", "tool not found", "tool missing", "runtime missing", "internal failure"} {
		if strings.HasPrefix(details, prefix) {
			return fmt.Errorf("nanolinter check %s could not complete: %w", result.Properties.CheckID, errs.ErrDependencyUnavailable)
		}
	}

	return nil
}

func (result nanoResult) findings() (int, int) {
	for _, suppression := range result.Suppressions {
		if suppression.Status == "accepted" {
			return 0, 0
		}
	}

	if result.Properties.Source != nanoCurated {
		return 1, 1
	}

	if result.Properties.Status != failedStatus {
		return 0, 0
	}

	switch result.Properties.CheckID {
	case "osv", "trivy-fs", "go-vulncheck", "rust-audit":
		return 0, 1
	case "secrets", "sast":
		return 1, 0
	default:
		// Unknown security/aggregate classes remain subject to both gates.
		switch result.Properties.Class {
		case "diagnostic", "formatter", "policy":
			return 1, 0
		default:
			return 1, 1
		}
	}
}

func securitySARIF(body []byte, securityResults []json.RawMessage) ([]byte, error) {
	// Preserve every SARIF field and rule. Only the result scope changes, just
	// as Nanolinter's sarif-security reporter filters the full report.
	var full map[string]json.RawMessage
	if err := json.Unmarshal(body, &full); err != nil {
		return nil, err
	}

	var runs []map[string]json.RawMessage
	if err := json.Unmarshal(full["runs"], &runs); err != nil {
		return nil, err
	}

	results, err := json.Marshal(securityResults)
	if err != nil {
		return nil, err
	}

	runs[0]["results"] = results

	full["runs"], err = json.Marshal(runs)
	if err != nil {
		return nil, err
	}

	return json.MarshalIndent(full, "", "  ")
}
