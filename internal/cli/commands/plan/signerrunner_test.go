// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package plan_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/cli"
)

// Each flag reaches its own argument of the admission: the runner is the one
// compared, and the list the one it is compared with.
func TestAdmitSignerRunnerCLI(t *testing.T) {
	const plan = `{"targets":{"xcode_ios":{"runs":true,"items":[{"name":"app","xcode_ios":{"scheme":"App"}}]}}}`

	run := func(args ...string) error {
		var out, stderr bytes.Buffer

		cmd := cli.New(cli.BuildInfo{})
		cmd.Writer, cmd.ErrWriter = &out, &stderr

		return cmd.Run(t.Context(), append([]string{"forge-ci", "--provider", "local", "--runner", "local", "plan", "admit-signer-runner"}, args...))
	}

	if err := run("--build-stage-plan-json", plan, "--runs-on-darwin-arm64", `["macos-27"]`, "--enforce-signer-runs-on-darwin-arm64", `["macos-26","macos-27"]`); err != nil {
		t.Fatalf("an allowed runner is refused: %v", err)
	}

	err := run("--build-stage-plan-json", plan, "--runs-on-darwin-arm64", `["mac-mini"]`, "--enforce-signer-runs-on-darwin-arm64", `["macos-26","macos-27"]`)
	if err == nil || !strings.Contains(err.Error(), "does not allow its label mac-mini (allowed: macos-26, macos-27)") {
		t.Fatalf("a runner outside the list is admitted or unnamed: %v", err)
	}

	// The list has no default: a layer that passes none is refused.
	if err := run("--build-stage-plan-json", plan, "--runs-on-darwin-arm64", `["mac-mini"]`); err == nil {
		t.Fatal("a call without the administrator's list is admitted")
	}
}
