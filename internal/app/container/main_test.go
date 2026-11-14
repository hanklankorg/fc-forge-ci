// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// githubActionFakeTools is the directory of the one executable these tests
// run, the fake engine of the Kaniko entrypoint test.
var githubActionFakeTools string

// TestMain writes that executable before any test runs. A child forked while
// a file is open for writing inherits the descriptor until it execs, and
// executing the file meanwhile fails with "text file busy"; parallel tests
// fork, so no test may write what it or another then executes. A test picks
// the fake's behaviour through its environment.
func TestMain(m *testing.M) {
	os.Exit(runWithFakeTools(m))
}

func runWithFakeTools(m *testing.M) int {
	tools, err := os.MkdirTemp("", "container-test-tools-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	defer func() { _ = os.RemoveAll(tools) }()

	if err = os.WriteFile(filepath.Join(tools, "forge-ci"), []byte(githubActionFakeEngine), 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	githubActionFakeTools = tools

	return m.Run()
}
