// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package ci

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// timeoutBound is the duration a context deadline is given.
var timeoutBound = regexp.MustCompile(`context\.WithTimeout\([^,]+,\s*([^)]+)\)`)

// These tests drive the real scripts under a whole test suite's load, where
// a run that takes well under a second on an idle machine can take many. A
// deadline sized to the idle machine kills a healthy script and reads as a
// failure: the provider-lane test held its lane under 20 seconds and was
// killed. Every deadline here is liveCommandBound, which only catches a hang.
func TestScriptDeadlinesOnlyCatchAHang(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}

	bounds := 0

	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}

		for _, match := range timeoutBound.FindAllStringSubmatch(string(body), -1) {
			bounds++

			if bound := strings.TrimSpace(match[1]); bound != "liveCommandBound" {
				t.Errorf("%s bounds a script with %s; use liveCommandBound, which only catches a hang", file, bound)
			}
		}
	}

	if bounds == 0 {
		t.Fatal("no script test sets a deadline; the guard has drifted")
	}
}
