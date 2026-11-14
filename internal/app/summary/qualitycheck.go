// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package summary

import (
	"context"
	"fmt"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/ci"
	domainsummary "github.com/diggsweden/forge-ci/internal/domain/summary"
)

// QualityCheck is one row in the quality-check status table.
type QualityCheck struct {
	Name    string
	Enabled bool
	Result  domainsummary.Result
}

// QualityCheckStatus appends the PR-quality-check status block to the
// step summary. Always exits successfully — the underlying job is a
// summary-only collector that surfaces the matrix outcome but never
// fails the pipeline.
// Empty input retains the historical vacuous pass. An enabled skipped or
// unconfirmed check does not establish that all enabled checks passed.
func QualityCheckStatus(ctx context.Context, sink ci.SummarySink, checks []QualityCheck) error {
	var builder strings.Builder
	builder.WriteString("## Pull Request Check Status\n\n")
	builder.WriteString("### Quality Check Results\n")
	builder.WriteString("| Check | Status |\n")
	builder.WriteString("|-------|--------|\n")

	failed := false
	unconfirmed := false

	for _, c := range checks {
		name := domainsummary.LiteralText(c.Name)

		switch {
		case !c.Enabled:
			_, _ = fmt.Fprintf(&builder, "| %s | 🔸 Disabled |\n", name)
		case c.Result == domainsummary.ResultSuccess:
			_, _ = fmt.Fprintf(&builder, "| %s | ✓ Pass |\n", name)
		case c.Result == domainsummary.ResultSkipped:
			_, _ = fmt.Fprintf(&builder, "| %s | − Skipped |\n", name)
			unconfirmed = true
		case !domainsummary.IsResult(c.Result):
			_, _ = fmt.Fprintf(&builder, "| %s | Unconfirmed |\n", name)
			unconfirmed = true
		default:
			_, _ = fmt.Fprintf(&builder, "| %s | ✗ Fail |\n", name)

			failed = true
		}
	}

	builder.WriteString("\n")

	switch {
	case failed:
		builder.WriteString("### ✗ Some checks failed\n")
		builder.WriteString("Please review the failures above and fix any issues.\n")
		builder.WriteString("Note: Individual linter failures are shown above. This status job always succeeds to provide summary.\n")
	case unconfirmed:
		builder.WriteString("### Some enabled checks are skipped or unconfirmed\n")
	default:
		builder.WriteString("### ✓ All enabled checks passed\n")
	}

	return sink.Append(ctx, builder.String())
}

// ParseQualityChecks splits "Name|enabled|result" lines into checks.
// Mirrors CLI signature.
func ParseQualityChecks(args []string) []QualityCheck {
	out := make([]QualityCheck, 0, len(args))
	for _, raw := range args {
		parts := strings.Split(raw, "|")
		if len(parts) != 3 || (parts[1] != "true" && parts[1] != "false") {
			out = append(out, QualityCheck{Name: parts[0], Enabled: true})

			continue
		}

		out = append(out, QualityCheck{
			Name:    parts[0],
			Enabled: parts[1] == "true",
			Result:  domainsummary.Result(parts[2]),
		})
	}

	return out
}
