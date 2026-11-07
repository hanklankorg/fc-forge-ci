// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package enforcement

import (
	"context"
	"io"
)

// NanolinterVerifyInput describes the standard PR lint invocation without
// shell command construction. Empty RequiredLints intentionally selects no floor.
type NanolinterVerifyInput struct {
	RequiredLints string
	DisableChecks string
	Offline       bool
	SARIFFile     string
}

// NanolinterVerify constructs the fixed verify/SARIF command and delegates to
// the existing administrator-policy gate. Findings and operational failures
// retain exactly that gate's classification and enforcement behavior.
func NanolinterVerify(ctx context.Context, runner LintRunner, out, stderr io.Writer, in NanolinterVerifyInput) error {
	args := []string{"nanolinter", "verify"}
	if in.RequiredLints != "" {
		args = append(args, "--require", in.RequiredLints)
	}

	if in.DisableChecks != "" {
		args = append(args, "--disable-check", in.DisableChecks)
	}

	if in.Offline {
		args = append(args, "--offline")
	}

	args = append(args, "--report", "sarif", "--out", "-")

	return Nanolinter(ctx, runner, out, stderr, args, in.SARIFFile)
}
