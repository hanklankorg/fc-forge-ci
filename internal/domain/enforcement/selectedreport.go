// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package enforcement

import (
	"encoding/json"
	"regexp"
	"strings"
)

// SelectedReport requires completed, nonempty native test execution, including
// on exit zero. It is fed only the current invocation, never a checkout file.
// As with TestOutput, tests themselves remain subject to source review.
type SelectedReport struct {
	protocol             TestProtocol
	output               *TestOutput
	packages             map[string]bool
	activeTests          map[string]bool
	failedPackages       map[string]bool
	tests                int
	failures             int
	running              int
	rejected             bool
	cargoFailureTerminal bool
	cargoSummaries       int
}

var selectedCargoSummary = regexp.MustCompile(`^test result: (ok|FAILED)\. ([0-9]+) passed; ([0-9]+) failed; ([0-9]+) ignored; ([0-9]+) measured; ([0-9]+) filtered out; finished in .+$`)
var selectedCargoRunning = regexp.MustCompile(`^running ([0-9]+) tests?$`)
var selectedCargoFailure = regexp.MustCompile("^error: test failed, to rerun pass `[^`]+`$")

const (
	selectedGoPass = "pass"
	selectedGoSkip = "skip"
)

// NewSelectedReport supports only engine-owned Go and Cargo selections.
func NewSelectedReport(protocol TestProtocol) *SelectedReport {
	report := &SelectedReport{protocol: protocol, packages: map[string]bool{}, activeTests: map[string]bool{}, failedPackages: map[string]bool{}, running: -1}
	report.output = &TestOutput{classifier: report}

	return report
}

// Output supplies the bounded, stream-separated output capture.
func (report *SelectedReport) Output() *TestOutput { return report.output }

// Valid finalizes the capture and checks native completion against exit status.
func (report *SelectedReport) Valid(success bool) bool {
	_ = report.output.FailedTests()
	if report.output.Overlong() || report.rejected || report.tests == 0 || report.running != -1 || len(report.activeTests) != 0 {
		return false
	}

	for _, completed := range report.packages {
		if !completed {
			return false
		}
	}

	if report.protocol == Cargo && report.cargoFailureTerminal != (report.failures > 0) {
		return false
	}

	if report.protocol == Cargo && report.cargoSummaries != 1 {
		return false
	}

	return success == (report.failures == 0)
}

func (report *SelectedReport) failed() int { return report.failures }

func (report *SelectedReport) observe(stream testStream, line string) {
	switch report.protocol {
	case GoJSON:
		report.goLine(stream, line)
	case Cargo:
		report.cargoLine(stream, line)
	default:
		report.rejected = true
	}
}

func (report *SelectedReport) goLine(stream testStream, line string) {
	if stream == stderrStream {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "go: downloading ") {
			report.rejected = true
		}

		return
	}

	var event struct {
		Action  string `json:"Action"`
		Package string `json:"Package"`
		Test    string `json:"Test"`
		Output  string `json:"Output"`
	}
	if json.Unmarshal([]byte(line), &event) != nil || event.Package == "" || crashed(event.Output) || event.Action == "build-fail" {
		report.rejected = true

		return
	}

	switch event.Action {
	case "start", "run", "pause", "cont", selectedGoPass, "bench", failedStatus, "output", selectedGoSkip:
	default:
		report.rejected = true
	}

	report.goPackageEvent(event.Package, event.Test, event.Action)
}

func (report *SelectedReport) goPackageEvent(packageName, testName, action string) {
	if action == "start" && testName == "" {
		if _, exists := report.packages[packageName]; exists {
			report.rejected = true
		}

		report.packages[packageName] = false

		return
	}

	if completed, exists := report.packages[packageName]; !exists || completed {
		report.rejected = true
	}

	if testName == "" {
		if (action == selectedGoPass || action == selectedGoSkip) && report.failedPackages[packageName] {
			report.rejected = true
		}

		if action == selectedGoPass || action == failedStatus || action == selectedGoSkip {
			report.packages[packageName] = true
		}

		if action == failedStatus {
			report.failures++
		}

		return
	}

	report.goTestEvent(packageName, testName, action)
}

func (report *SelectedReport) goTestEvent(packageName, testName, action string) {
	key := packageName + "\x00" + testName
	if action == "run" {
		if report.activeTests[key] {
			report.rejected = true
		}

		report.activeTests[key] = true
	}

	if action == failedStatus {
		report.failedPackages[packageName] = true
	}

	if action == selectedGoPass || action == failedStatus || action == selectedGoSkip {
		if !report.activeTests[key] {
			report.rejected = true
		}

		delete(report.activeTests, key)

		if action != selectedGoSkip {
			report.tests++
		}
	}
}

func (report *SelectedReport) cargoLine(stream testStream, line string) {
	trimmed := strings.TrimSpace(line)

	failureTerminal := stream == stderrStream && selectedCargoFailure.MatchString(trimmed)
	if stream == stderrStream && trimmed != "" {
		if report.cargoFailureTerminal {
			report.rejected = true
		}

		if failureTerminal {
			report.cargoFailureTerminal = true
		}
	}

	if strings.Contains(line, "could not compile") || strings.Contains(line, "failed to run custom build command") ||
		strings.Contains(line, "process didn't exit successfully") ||
		strings.Contains(line, "error:") && !failureTerminal {
		report.rejected = true
	}

	if stream != stdoutStream {
		return
	}

	report.cargoHarnessLine(line)
}

func (report *SelectedReport) cargoHarnessLine(line string) {
	if match := selectedCargoRunning.FindStringSubmatch(line); match != nil {
		if report.running != -1 {
			report.rejected = true
		}

		report.running = number(match[1])
	}

	if match := selectedCargoSummary.FindStringSubmatch(line); match != nil {
		report.cargoSummaries++

		passed, failed, ignored, measured := number(match[2]), number(match[3]), number(match[4]), number(match[5])
		if report.running != passed+failed+ignored+measured || (match[1] == "ok") != (failed == 0) {
			report.rejected = true
		}

		report.running = -1
		report.tests += passed + failed
		report.failures += failed
	} else if strings.Contains(line, "test result:") {
		report.rejected = true
	}
}
