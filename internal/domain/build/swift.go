// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build

import (
	"fmt"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/summary"
)

// SwiftLintOutcome is the result of one Swift linter invocation.
type SwiftLintOutcome string

const (
	// SwiftLintPassed indicates the linter exited 0.
	SwiftLintPassed SwiftLintOutcome = "passed"
	// SwiftLintFailed indicates the linter exited non-zero AND must fail the step.
	SwiftLintFailed SwiftLintOutcome = "failed"
	// SwiftLintNoFiles indicates swift-format found no Swift files matching
	// the configured pattern. Treated as success.
	SwiftLintNoFiles SwiftLintOutcome = "no_files"
)

// RenderSwiftFormatBlock returns the markdown block written to
// $GITHUB_STEP_SUMMARY for a swift-format invocation.
func RenderSwiftFormatBlock(outcome SwiftLintOutcome, output string) string {
	var builder strings.Builder

	switch outcome {
	case SwiftLintPassed:
		_, _ = fmt.Fprintln(&builder, "## Swift Format ✓")
		_, _ = fmt.Fprintln(&builder, "")
		_, _ = fmt.Fprintln(&builder, "All Swift files are properly formatted.")
	case SwiftLintFailed:
		_, _ = fmt.Fprintln(&builder, "## Swift Format Issues 🔴")
		_, _ = fmt.Fprintln(&builder, "")
		writeFenced(&builder, output)
	case SwiftLintNoFiles:
		_, _ = fmt.Fprintln(&builder, "## Swift Format ⊘")
		_, _ = fmt.Fprintln(&builder, "")
		_, _ = fmt.Fprintln(&builder, "No Swift files matched the configured pattern; nothing to lint.")
	}

	return builder.String()
}

// RenderSwiftLintBlock returns the markdown block written to
// $GITHUB_STEP_SUMMARY for a SwiftLint invocation.
func RenderSwiftLintBlock(outcome SwiftLintOutcome, output string) string {
	var builder strings.Builder

	switch outcome {
	case SwiftLintPassed:
		_, _ = fmt.Fprintln(&builder, "## SwiftLint ✓")
		_, _ = fmt.Fprintln(&builder, "")
		_, _ = fmt.Fprintln(&builder, "No SwiftLint violations found.")
	case SwiftLintFailed:
		_, _ = fmt.Fprintln(&builder, "## SwiftLint Issues 🔴")
		_, _ = fmt.Fprintln(&builder, "")
		writeFenced(&builder, output)
	case SwiftLintNoFiles:
		// SwiftLint walks the directory tree itself — there is no
		// caller-side "no files" path. Block left empty so the caller
		// emits nothing.
	}

	return builder.String()
}

// SwiftLintSummaryInput drives RenderSwiftLintSummary.
type SwiftLintSummaryInput struct {
	SwiftFormatEnabled bool
	SwiftFormatResult  string // GHA needs result string: success | failure | skipped | cancelled
	SwiftLintEnabled   bool
	SwiftLintResult    string
}

// RenderSwiftLintSummary returns the aggregate table written by the
// summary job. Byte-for-byte compatible with the bash output so any
// pinned screenshots / changelog excerpts still match.
func RenderSwiftLintSummary(in SwiftLintSummaryInput) string {
	var builder strings.Builder

	_, _ = fmt.Fprintln(&builder, "## Swift Linting Summary")
	_, _ = fmt.Fprintln(&builder, "")
	_, _ = fmt.Fprintln(&builder, "| Linter | Status |")
	_, _ = fmt.Fprintln(&builder, "|--------|--------|")
	_, _ = fmt.Fprintf(&builder, "| swift-format | %s |\n", swiftLinterRow(in.SwiftFormatEnabled, in.SwiftFormatResult))
	_, _ = fmt.Fprintf(&builder, "| SwiftLint | %s |\n", swiftLinterRow(in.SwiftLintEnabled, in.SwiftLintResult))

	if SwiftLintAggregateFailed(in) {
		_, _ = fmt.Fprintln(&builder, "")
		_, _ = fmt.Fprintln(&builder, "### ✗ Linting failed")
		_, _ = fmt.Fprintln(&builder, "Please fix the issues above.")
	}

	return builder.String()
}

// SwiftLintAggregateFailed reports whether the summary should signal a
// failed run: at least one enabled linter's row reads Fail. That is every
// result other than success and skipped, so a cancelled or unrecognised
// result fails the run as its row says, rather than showing Fail and exiting
// zero.
func SwiftLintAggregateFailed(in SwiftLintSummaryInput) bool {
	return swiftLinterFailed(in.SwiftFormatEnabled, in.SwiftFormatResult) ||
		swiftLinterFailed(in.SwiftLintEnabled, in.SwiftLintResult)
}

func swiftLinterFailed(enabled bool, result string) bool {
	return enabled && result != "success" && result != "skipped"
}

func swiftLinterRow(enabled bool, result string) string {
	switch {
	case !enabled:
		return "🔸 Disabled"
	case swiftLinterFailed(enabled, result):
		return "✗ Fail"
	case result == "success":
		return "✓ Pass"
	default:
		return "− Skipped"
	}
}

// writeStripped writes s to b, ensuring it ends with exactly one newline.
// Mirrors `cat output.txt` behavior where the file already ends
// in a newline; without this every Markdown fence picks up a stray blank
// line.
// writeFenced writes linter output as a fenced code block whose fence is
// longer than any run of backticks inside it.
//
// The fence used to be a fixed "```", so output containing a line of three
// backticks -- a Swift string literal, a doc comment, a file path chosen by
// whoever opened the pull request -- closed the block early and everything
// after it rendered as Markdown in the step summary. CommonMark closes a fence
// only on a run at least as long as the opener, so a longer opener keeps the
// output literal without altering a byte of it.
func writeFenced(builder *strings.Builder, output string) {
	fence := summary.CodeFence(output)

	builder.WriteString(fence)
	builder.WriteByte('\n')
	builder.WriteString(strings.TrimRight(output, "\n"))
	builder.WriteByte('\n')
	builder.WriteString(fence)
	builder.WriteByte('\n')
}
