// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package testenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// minBashMajor is the oldest Bash the repository's scripts support: they use
// mapfile, associative arrays and case conversion. Apple's /bin/bash is 3.2.
const minBashMajor = 4

var bashVersion = regexp.MustCompile(`version (\d+)\.`)

// ToolPath exposes only the requested host tools, found on PATH, in a private
// directory. It does not change the process environment or install missing
// prerequisites.
func ToolPath(t *testing.T, names ...string) string {
	t.Helper()

	return linkTools(t, names, func(name string) string {
		binary, err := exec.LookPath(name)
		if err != nil {
			t.Fatalf("%s is required for this fixture: %v; see docs/DEVELOPMENT.md", name, err)
		}

		return binary
	})
}

// GNUPath exposes only the requested GNU-compatible tools (GNU, or uutils
// coreutils) in a private directory.
// Linux workflow fixtures need their actual tool semantics even on macOS/BSD;
// prefer the prefixed commands supplied by MacPorts and BSD package managers.
// Bash must be at least version 4. It does not change the process environment
// or install missing prerequisites.
func GNUPath(t *testing.T, names ...string) string {
	t.Helper()

	return linkTools(t, names, func(name string) string {
		binary, err := exec.LookPath("g" + name)
		if err != nil {
			binary, err = exec.LookPath(name)
		}

		if err != nil {
			t.Fatalf("GNU %s is required for this Linux shell fixture: %v; see docs/DEVELOPMENT.md", name, err)
		}

		// The binary is a PATH lookup of a test-named GNU tool; only --version runs.
		// nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command
		output, err := exec.CommandContext(t.Context(), binary, "--version").CombinedOutput() //nolint:gosec // test-owned selection of installed host utilities, version probe only.
		// uutils coreutils, the default on recent Ubuntu, follows the GNU
		// interface; BSD tools, which these fixtures cannot use, print neither.
		if err != nil || (!strings.Contains(string(output), "GNU") && !strings.Contains(string(output), "uutils coreutils")) {
			t.Fatalf("%s is not a working GNU %s; see docs/DEVELOPMENT.md", binary, name)
		}

		if name == "bash" {
			major := 0
			if match := bashVersion.FindSubmatch(output); match != nil {
				major, _ = strconv.Atoi(string(match[1]))
			}

			if major < minBashMajor {
				t.Fatalf("%s is not GNU Bash %d or later; see docs/DEVELOPMENT.md", binary, minBashMajor)
			}
		}

		return binary
	})
}

// linkTools symlinks each resolved tool into a fresh private directory.
func linkTools(t *testing.T, names []string, resolve func(string) string) string {
	t.Helper()

	dir := t.TempDir()

	for _, name := range names {
		if err := os.Symlink(resolve(name), filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}

	return dir
}
