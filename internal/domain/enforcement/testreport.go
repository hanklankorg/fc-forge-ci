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
	cargoSummary     = regexp.MustCompile(`test result: FAILED\.\s*[0-9]+ passed;\s*([0-9]+) failed;`)
	nodeSummary      = regexp.MustCompile(`(?m)^# fail ([0-9]+)\s*$`)
	gradleSummary    = regexp.MustCompile(`([0-9]+) tests completed, ([0-9]+) failed`)
	gradleFailedTask = regexp.MustCompile(`(?m)^> Task (:[^\s]+) FAILED\s*$`)
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
		matches := mavenSummary.FindAllStringSubmatch(body, -1)
		if len(matches) == 0 {
			return 0
		}
		for _, match := range matches {
			if number(match[2]) != 0 {
				return 0
			}
		}
		last := matches[len(matches)-1]
		if number(last[2]) != 0 {
			return 0
		}
		return number(last[1])
	case Cargo:
		if strings.Contains(body, "could not compile") || strings.Contains(body, "failed to run custom build command") {
			return 0
		}
		count := 0
		for _, match := range cargoSummary.FindAllStringSubmatch(body, -1) {
			count += number(match[1])
		}
		return count
	case Node:
		// Node's file/import/syntax failures also produce '# fail'. Only a
		// native assertion diagnostic distinguishes them from test findings.
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
	case Gradle:
		failedTasks := gradleFailedTask.FindAllStringSubmatch(body, -1)
		if len(failedTasks) == 0 {
			return 0
		}
		for _, task := range failedTasks {
			name := task[1][strings.LastIndex(task[1], ":")+1:]
			if name != "test" && !(strings.HasPrefix(name, "test") && strings.HasSuffix(name, "UnitTest")) {
				return 0
			}
		}
		count := 0
		for _, match := range gradleSummary.FindAllStringSubmatch(body, -1) {
			count += number(match[2])
		}
		return count
	case XCTest:
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
	default:
		return 0
	}
}

func failedGoTests(body string) int {
	failed := map[string]bool{}
	packages := map[string]bool{}
	count := 0
	for line := range strings.SplitSeq(strings.TrimSpace(body), "\n") {
		var event struct{ Action, Package, Test, Output string }
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return 0
		}
		if event.Action == "build-fail" || strings.Contains(event.Output, "[build failed]") {
			return 0
		}
		if event.Action == "fail" {
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

func number(raw string) int { value, _ := strconv.Atoi(raw); return value }
