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
	results := map[string]string{}
	lineResult := regexp.MustCompile(`^\s*--- (PASS|FAIL|SKIP): (\S+)`)
	scanner := bufio.NewScanner(log)
	scanner.Buffer(make([]byte, 4096), 16<<20)
	for scanner.Scan() {
		if match := lineResult.FindStringSubmatch(scanner.Text()); match != nil {
			results[match[2]] = match[1]
		}
	}
	if err := scanner.Err(); err != nil {
		return LiveRunResult{1, "failed"}, fmt.Errorf("read live scenario results: %w", err)
	}
	for _, result := range results {
		if result == "FAIL" {
			return LiveRunResult{1, "failed"}, nil
		}
	}
	for name, result := range results {
		if result != "PASS" {
			continue
		}
		leaf := true
		for other := range results {
			if strings.HasPrefix(other, name+"/") {
				leaf = false
				break
			}
		}
		if leaf {
			return LiveRunResult{0, "passed"}, nil
		}
	}
	return LiveRunResult{1, "skipped: no scenario ran; the log says why"}, nil
}
