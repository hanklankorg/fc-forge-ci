// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package enforcement

import (
	"context"
	"io"
	"slices"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// NanolinterVerifyInput describes the standard PR lint invocation without
// shell command construction. Empty RequiredLints intentionally selects no floor.
type NanolinterVerifyInput struct {
	RequiredLints string
	DisableChecks string
	Offline       bool
	SARIFFile     string
	// BaselineRef is the git revision of a pull request's target branch, from
	// PullRequestBaseline; empty on every other event.
	BaselineRef string
}

// PullRequestBaseline returns the revision the dependency-advisory checks of
// a pull request are compared against, origin/<target branch>, and "" on
// every other event: a push, a release or a snapshot reports everything. The
// provider names a base branch for a pull or merge request only.
func PullRequestBaseline(event *provider.EventContext) string {
	if event == nil || event.BaseBranch == "" {
		return ""
	}

	return "origin/" + event.BaseBranch
}

// WithBaseline appends --baseline-ref to a nanolinter verify command, which
// may be wrapped in a container runtime: the flag is among the last arguments
// either way. An empty ref leaves the command as it is.
func WithBaseline(args []string, ref string) []string {
	if ref == "" || len(args) == 0 {
		return args
	}

	return append(slices.Clone(args), "--baseline-ref", ref)
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

	return Nanolinter(ctx, runner, out, stderr, WithBaseline(args, in.BaselineRef), in.SARIFFile)
}
