// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package enforcement

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// TestProtocol is selected by engine code, not by a consumer policy value.
type TestProtocol string

// Native result protocols supported by the application-test gate.
const (
	GoJSON TestProtocol = "go-json"
	Maven  TestProtocol = "maven"
	Cargo  TestProtocol = "cargo"
	Node   TestProtocol = "node"
	Gradle TestProtocol = "gradle"
	XCTest TestProtocol = "xctest"
)

var (
	ansi             = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	mavenSummary     = regexp.MustCompile(`Tests run:\s*[0-9]+,\s*Failures:\s*([0-9]+),\s*Errors:\s*([0-9]+),\s*Skipped:`)
	mavenTotals      = regexp.MustCompile(`(?m)^[ \t]*(?:\[(?:INFO|ERROR)\]\s*)?Tests run:\s*[0-9]+,\s*Failures:\s*([0-9]+),\s*Errors:\s*([0-9]+),\s*Skipped:\s*[0-9]+[ \t]*$`)
	mavenGoalFailure = regexp.MustCompile(`(?m)^\[ERROR\] Failed to execute goal[^\r\n]*`)
	cargoSummary     = regexp.MustCompile(`test result: FAILED\.\s*[0-9]+ passed;\s*([0-9]+) failed;`)
	nodeSummary      = regexp.MustCompile(`(?m)^# fail ([0-9]+)\s*$`)
	gradleSummary    = regexp.MustCompile(`([0-9]+) tests completed, ([0-9]+) failed`)
	gradleFailedTask = regexp.MustCompile(`(?m)^> Task (:[^\s]+) FAILED\s*$`)
	gradleTaskCause  = regexp.MustCompile(`^Execution failed for task '(:[^'\r\n]+)'\.$`)
	gradleBuildEnd   = regexp.MustCompile(`(?m)^BUILD FAILED in [^\r\n]+\r?$`)
	xctestSummary    = regexp.MustCompile(`Executed [0-9]+ tests?, with ([0-9]+) failures? \(([0-9]+) unexpected\)`)
)

// FailedTests recognizes a fresh invocation's complete native test summary.
// Unknown output and compiler/setup errors are not findings and fail closed.
// This classifies tool results; it cannot prove that consumer-written tests are
// honest or comprehensive, which remains a source-review responsibility.
func FailedTests(protocol TestProtocol, body string) int {
	body = ansi.ReplaceAllString(body, "")

	switch protocol {
	case GoJSON:
		return failedGoTests(body)
	case Maven:
		return failedMavenTests(body)
	case Cargo:
		return failedCargoTests(body)
	case Node:
		return failedNodeTests(body)
	case Gradle:
		return failedGradleTests(body)
	case XCTest:
		return failedXCTests(body)
	default:
		return 0
	}
}

func failedMavenTests(body string) int {
	matches := mavenSummary.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return 0
	}

	for _, match := range matches {
		if number(match[2]) != 0 {
			return 0
		}
	}
	goals := mavenGoalFailure.FindAllString(body, -1)
	if len(goals) == 0 {
		return 0
	}
	for _, goal := range goals {
		if (!strings.Contains(goal, "maven-surefire-plugin:") && !strings.Contains(goal, "maven-failsafe-plugin:")) ||
			!strings.Contains(goal, "There are test failures") {
			return 0
		}
	}
	count := 0
	for _, total := range mavenTotals.FindAllStringSubmatch(body, -1) {
		count += number(total[1])
	}
	return count
}

func failedCargoTests(body string) int {
	if strings.Contains(body, "could not compile") || strings.Contains(body, "failed to run custom build command") {
		return 0
	}

	count := 0
	for _, match := range cargoSummary.FindAllStringSubmatch(body, -1) {
		count += number(match[1])
	}

	return count
}

func failedNodeTests(body string) int {
	// File/import/syntax failures also produce '# fail'; an assertion diagnostic
	// is required before that summary is eligible for a policy continuation.
	if !strings.Contains(body, "ERR_ASSERTION") {
		return 0
	}

	for _, failure := range []string{"ERR_MODULE_NOT_FOUND", "MODULE_NOT_FOUND", "ERR_TEST_FAILURE", "SyntaxError"} {
		if strings.Contains(body, failure) {
			return 0
		}
	}

	matches := nodeSummary.FindAllStringSubmatch(body, -1)
	if len(matches) != 1 {
		return 0
	}

	return number(matches[0][1])
}

func failedGradleTests(body string) int {
	failedTasks := gradleFailedTask.FindAllStringSubmatch(body, -1)
	if len(failedTasks) == 0 {
		return 0
	}

	failed := map[string]bool{}
	for _, task := range failedTasks {
		name := task[1][strings.LastIndex(task[1], ":")+1:]
		if name != "test" && (!strings.HasPrefix(name, "test") || !strings.HasSuffix(name, "UnitTest")) {
			return 0
		}
		failed[task[1]] = true
	}
	if !gradleAssertionTerminals(body, failed) {
		return 0
	}

	count := 0
	for _, match := range gradleSummary.FindAllStringSubmatch(body, -1) {
		count += number(match[2])
	}

	return count
}

// Every terminal cause must identify a failed test task and assertion findings.
// A summary printed before a daemon crash or another runtime failure is not a
// completed assertion-only invocation, even when the process also exits 1.
func gradleAssertionTerminals(body string, failed map[string]bool) bool {
	if !gradleBuildEnd.MatchString(body) || strings.Contains(strings.ToLower(body), "daemon disappeared") || strings.Contains(strings.ToLower(body), "daemon has disappeared") {
		return false
	}
	sections := strings.Split(body, "* What went wrong:")
	if len(sections) == 1 {
		return false
	}
	seen := map[string]bool{}
	for _, section := range sections[1:] {
		cause, _, complete := strings.Cut(section, "* Try:")
		if !complete {
			return false
		}
		lines := strings.Split(strings.TrimSpace(cause), "\n")
		if len(lines) != 2 {
			return false
		}
		task := gradleTaskCause.FindStringSubmatch(strings.TrimSpace(lines[0]))
		if len(task) != 2 || !failed[task[1]] {
			return false
		}
		detail := strings.TrimSpace(lines[1])
		if detail != "> There were failing tests." && !strings.HasPrefix(detail, "> There were failing tests. See the report at: ") {
			return false
		}
		seen[task[1]] = true
	}
	return len(seen) == len(failed)
}

func failedXCTests(body string) int {
	if !strings.Contains(body, "** TEST FAILED **") {
		return 0
	}

	matches := xctestSummary.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return 0
	}

	last := matches[len(matches)-1]
	if number(last[2]) != 0 {
		return 0
	}

	return number(last[1])
}

func failedGoTests(body string) int {
	failed := map[string]bool{}
	packages := map[string]bool{}
	count := 0

	for line := range strings.SplitSeq(strings.TrimSpace(body), "\n") {
		var event struct {
			Action  string `json:"Action"`
			Package string `json:"Package"`
			Test    string `json:"Test"`
			Output  string `json:"Output"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return 0
		}

		if event.Action == "build-fail" || strings.Contains(event.Output, "[build failed]") {
			return 0
		}

		if event.Action == failedStatus {
			if event.Test != "" {
				failed[event.Package] = true
				count++
			} else {
				packages[event.Package] = true
			}
		}
	}

	if len(packages) == 0 {
		return 0
	}

	for name := range packages {
		if !failed[name] {
			return 0
		}
	}

	return count
}

func number(raw string) int {
	value, _ := strconv.Atoi(raw)

	return value
}
