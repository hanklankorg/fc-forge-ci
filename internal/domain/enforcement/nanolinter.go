// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package enforcement

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	// Unavailable is set when a check produced no verdict: it failed at run
	// time, or a security check was skipped. Enforcement decides on findings;
	// whether a check ran is not a finding and is never waived.
	Unavailable error

	gates map[Check]*nanoGate
}

// nanoGate records what the checks of one gate did besides finding things.
type nanoGate struct {
	incomplete    []string
	notApplicable []string
	ran           bool
}

// Outcome names what a gate with no findings must not call clean: "incomplete"
// when one of its checks gave no verdict, "not-applicable" when its security
// checks do not apply to this repository and none of its checks ran. The detail
// lists those checks. Both are empty for a gate whose checks all ran.
func (report *NanolinterReport) Outcome(check Check) (string, string) {
	gate := report.gates[check]
	if gate == nil {
		return "", ""
	}

	var detail []string
	if len(gate.incomplete) > 0 {
		detail = append(detail, "could-not-complete="+strings.Join(gate.incomplete, ","))
	}

	if len(gate.notApplicable) > 0 {
		detail = append(detail, "not-applicable="+strings.Join(gate.notApplicable, ","))
	}

	outcome := ""

	switch {
	case len(gate.incomplete) > 0:
		outcome = "incomplete"
	case len(gate.notApplicable) > 0 && !gate.ran:
		outcome = "not-applicable"
	}

	return outcome, strings.Join(detail, " ")
}

// ParseNanolinter requires the full SARIF of this invocation, not a pre-existing
// consumer artifact. Unknown/external finding classes fall under both gates:
// disabling one class cannot turn an unclassified vulnerability into a waiver.
// A check without a verdict does not stop the count: the findings of the other
// checks and the security SARIF are returned with Unavailable set.
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

	report := NanolinterReport{gates: map[Check]*nanoGate{Lint: {}, Vulnerabilities: {}}}

	securityResults := []json.RawMessage{}

	for _, raw := range document.Runs[0].Results {
		result, err := parseNanoResult(raw)
		if err != nil {
			return NanolinterReport{}, err
		}

		if result.Properties.Class == "security" || result.Properties.Source != nanoCurated {
			securityResults = append(securityResults, raw)
		}

		report.count(result)
	}

	var err error

	report.SecuritySARIF, err = securitySARIF(body, securityResults)

	return report, err
}

func (report *NanolinterReport) count(result nanoResult) {
	var gates []*nanoGate

	lint, vulnerabilities := result.gates()
	if lint {
		gates = append(gates, report.gates[Lint])
	}

	if vulnerabilities {
		gates = append(gates, report.gates[Vulnerabilities])
	}

	unavailable := result.runtimeError()
	if unavailable == nil && result.security() && result.Properties.Status == "skip" {
		unavailable = fmt.Errorf("nanolinter security check %s was skipped: %w", result.Properties.CheckID, errs.ErrPrerequisite)
	}

	for _, gate := range gates {
		switch {
		case unavailable != nil:
			gate.incomplete = append(gate.incomplete, result.Properties.CheckID)
		case result.security() && result.Properties.Status == "n/a":
			gate.notApplicable = append(gate.notApplicable, result.Properties.CheckID)
		default:
			gate.ran = true
		}
	}

	if unavailable != nil {
		report.Unavailable = errors.Join(report.Unavailable, unavailable)

		return
	}

	lintFindings, vulnerabilityFindings := result.findings()
	report.LintFindings += lintFindings
	report.VulnerabilityFindings += vulnerabilityFindings
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

	return result, nil
}

func (result nanoResult) runtimeError() error {
	if result.Properties.Status != failedStatus {
		return nil
	}

	switch result.Properties.FailureCategory {
	case "", "format_findings", "policy_failure", "security_findings", "code_findings":
	default:
		return fmt.Errorf("nanolinter check %s could not complete (%s): %w", result.Properties.CheckID, result.Properties.FailureCategory, errs.ErrPrerequisite)
	}
	// Older reports may return 1 for a scanner's operational failure.
	details := strings.ToLower(strings.TrimSpace(result.Properties.Details))
	for _, prefix := range []string{"tool failed", "tool not found", "tool missing", "runtime missing", "internal failure"} {
		if strings.HasPrefix(details, prefix) {
			return fmt.Errorf("nanolinter check %s could not complete: %w", result.Properties.CheckID, errs.ErrPrerequisite)
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

	if result.Properties.Source == nanoCurated && result.Properties.Status != failedStatus {
		return 0, 0
	}

	lint, vulnerabilities := result.gates()

	return count(lint), count(vulnerabilities)
}

func count(gate bool) int {
	if gate {
		return 1
	}

	return 0
}

var (
	dependencyChecks = []string{"osv", "trivy-fs", "go-vulncheck", "rust-audit"}
	sourceChecks     = []string{"secrets", "sast"}
)

// gates names the gates a result falls under: lint, vulnerabilities or both.
func (result nanoResult) gates() (bool, bool) {
	if result.Properties.Source != nanoCurated {
		return true, true
	}

	switch {
	case slices.Contains(dependencyChecks, result.Properties.CheckID):
		return false, true
	case slices.Contains(sourceChecks, result.Properties.CheckID):
		return true, false
	}

	// Unknown security/aggregate classes remain subject to both gates.
	switch result.Properties.Class {
	case "diagnostic", "formatter", "policy":
		return true, false
	default:
		return true, true
	}
}

// security holds for the checks whose results the security SARIF carries.
func (result nanoResult) security() bool {
	return result.Properties.Source == nanoCurated && (result.Properties.Class == "security" ||
		slices.Contains(dependencyChecks, result.Properties.CheckID) || slices.Contains(sourceChecks, result.Properties.CheckID))
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
