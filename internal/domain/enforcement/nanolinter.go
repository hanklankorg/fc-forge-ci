// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package enforcement

import (
	"encoding/json"
	"fmt"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

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
		var result struct {
			Level      string `json:"level"`
			Properties struct {
				Source  string `json:"source"`
				CheckID string `json:"check_id"`
				Class   string `json:"class"`
				Status  string `json:"status"`
			} `json:"properties"`
			Suppressions []struct {
				Status string `json:"status"`
			} `json:"suppressions"`
		}
		if err := json.Unmarshal(raw, &result); err != nil || (result.Properties.Source != "curated" && result.Properties.Source != "external") || (result.Properties.Source == "curated" && result.Properties.CheckID == "") {
			return NanolinterReport{}, fmt.Errorf("invalid Nanolinter finding: %w", errs.ErrMalformedInput)
		}
		if result.Properties.Class == "security" || result.Properties.Source != "curated" {
			securityResults = append(securityResults, raw)
		}
		approved := false
		for _, suppression := range result.Suppressions {
			if suppression.Status == "accepted" {
				approved = true
			}
		}
		if approved {
			continue
		}
		if result.Properties.Source != "curated" || result.Properties.CheckID == "" {
			report.LintFindings++
			report.VulnerabilityFindings++
			continue
		}
		switch result.Properties.Status {
		case "pass", "skip", "n/a":
			continue
		case "fail":
		default:
			return NanolinterReport{}, fmt.Errorf("unknown Nanolinter check status: %w", errs.ErrMalformedInput)
		}
		switch result.Properties.CheckID {
		case "osv", "trivy-fs", "go-vulncheck", "rust-audit":
			report.VulnerabilityFindings++
		case "secrets", "sast":
			report.LintFindings++
		default:
			// Known quality classes cannot contain dependency findings. An
			// unrecognized security check stays subject to both policies.
			report.LintFindings++
			switch result.Properties.Class {
			case "diagnostic", "formatter", "policy":
			default:
				report.VulnerabilityFindings++
			}
		}
	}
	// Preserve every SARIF field and rule. Only the result scope changes, just
	// as Nanolinter's sarif-security reporter filters the full report.
	var full map[string]json.RawMessage
	_ = json.Unmarshal(body, &full)
	var runs []map[string]json.RawMessage
	_ = json.Unmarshal(full["runs"], &runs)
	results, err := json.Marshal(securityResults)
	if err != nil {
		return NanolinterReport{}, err
	}
	runs[0]["results"] = results
	full["runs"], err = json.Marshal(runs)
	if err != nil {
		return NanolinterReport{}, err
	}
	report.SecuritySARIF, err = json.MarshalIndent(full, "", "  ")
	return report, err
}
