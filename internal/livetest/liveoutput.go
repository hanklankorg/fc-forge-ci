// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const (
	liveScenarioPrefix = "FORGE_CI_LIVE_SCENARIO "
	livePackageBegin   = "FORGE_CI_LIVE_PACKAGE_BEGIN "
	livePackageEnd     = "FORGE_CI_LIVE_PACKAGE_END "
)

// RegisterLiveScenario identifies a real live-test entrypoint, including its
// subtests. Registration alone is not execution: a passing leaf is still required.
// Keep this explicit in scenarios; guard helpers are also used by offline tests.
func RegisterLiveScenario(t interface{ Name() string }) {
	fmt.Printf("%s%s\n", liveScenarioPrefix, t.Name())
}

// LiveRunResult distinguishes real scenario execution from Go's package-level
// PASS when the selected tests or all of their subtests skipped.
type LiveRunResult struct {
	ExitCode int
	Verdict  string
}

// ClassifyLiveRun preserves process failures and requires a successful leaf
// scenario before calling a clean process exit a passing live run.
func ClassifyLiveRun(log io.Reader, exitCode int) (LiveRunResult, error) {
	if exitCode == 130 {
		return LiveRunResult{130, "interrupted"}, nil
	}
	if exitCode != 0 {
		return LiveRunResult{exitCode, "failed"}, nil
	}
	var active string
	var results map[string]string
	var scenarios map[string]bool
	var runs map[string]bool
	passed, failed := false, false
	lineResult := regexp.MustCompile(`^\s*--- (PASS|FAIL|SKIP): (\S+)`)
	// testing writes RUN records at column zero, even for nested subtests.
	// t.Log's quoted/multiline output is indented and cannot introduce a run.
	lineRun := regexp.MustCompile(`^=== RUN[ \t]+(\S+)[ \t]*$`)
	scanner := bufio.NewScanner(log)
	scanner.Buffer(make([]byte, 4096), 16<<20)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, livePackageBegin):
			if active != "" || strings.TrimPrefix(line, livePackageBegin) == "" {
				failed = true
			}
			active = strings.TrimPrefix(line, livePackageBegin)
			results, scenarios = map[string]string{}, map[string]bool{}
			runs = map[string]bool{}
		case strings.HasPrefix(line, livePackageEnd):
			if active == "" || strings.TrimPrefix(line, livePackageEnd) != active {
				failed = true
			} else if passingLiveLeaf(results, scenarios, runs) {
				passed = true
			}
			active = ""
		case strings.HasPrefix(line, liveScenarioPrefix):
			name := strings.TrimPrefix(line, liveScenarioPrefix)
			if active != "" && runs[name] {
				scenarios[name] = true
			}
		default:
			if match := lineRun.FindStringSubmatch(line); match != nil && active != "" {
				if runs[match[1]] {
					failed = true
				}
				runs[match[1]] = true
			}
			if match := lineResult.FindStringSubmatch(line); match != nil {
				if match[1] == "FAIL" {
					failed = true
				}
				if active != "" {
					if _, duplicate := results[match[2]]; duplicate {
						failed = true
					}
					results[match[2]] = match[1]
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return LiveRunResult{1, "failed"}, fmt.Errorf("read live scenario results: %w", err)
	}
	if failed || active != "" {
		return LiveRunResult{1, "failed"}, nil
	}
	if passed {
		return LiveRunResult{0, "passed"}, nil
	}
	return LiveRunResult{1, "skipped: no scenario ran; the log says why"}, nil
}

func passingLiveLeaf(results map[string]string, scenarios, runs map[string]bool) bool {
	for name := range runs {
		if results[name] != "PASS" {
			continue
		}
		leaf, ancestorsPassed := true, true
		// A child result without its RUN record cannot qualify, but must
		// still stop a parent PASS from masquerading as a passing leaf.
		for other := range results {
			if strings.HasPrefix(other, name+"/") {
				leaf = false
			}
		}
		for other := range runs {
			if strings.HasPrefix(other, name+"/") {
				leaf = false
			}
			// Only actual RUN identities are ancestors. A slash in a t.Run
			// name does not imply an intermediate test was ever executed.
			if strings.HasPrefix(name, other+"/") && results[other] != "PASS" {
				ancestorsPassed = false
			}
		}
		if leaf && ancestorsPassed {
			for scenario := range scenarios {
				if results[scenario] == "PASS" && (name == scenario || strings.HasPrefix(name, scenario+"/")) {
					return true
				}
			}
		}
	}
	return false
}
