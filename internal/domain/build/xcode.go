// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/summary"
)

// XcodeVersionInfo is the (marketing-version, build-number) pair the
// xcode build pipeline emits. Both default to "unknown" when missing.
type XcodeVersionInfo struct {
	Version string
	Build   string
}

// xcodePbxKeyValue matches "<KEY> = <value>;" pairs in
// project.pbxproj. The bash uses `awk -F' = '` then `tr -d ';'` —
// this regex captures the same shape and tolerates leading whitespace.
var xcodePbxKeyValue = regexp.MustCompile(`^\s*([A-Z_][A-Z0-9_]*)\s*=\s*([^;]+);`)

// ParseXcodeVersionFromPbxproj extracts MARKETING_VERSION and
// CURRENT_PROJECT_VERSION from a project.pbxproj body. Both default to
// "unknown" when not found, mirroring the bash fallback.
//
// The pbxproj format is undocumented but stable enough that grep-style
// extraction works in practice; this helper preserves the bash shape
// while typing the result.
func ParseXcodeVersionFromPbxproj(body string) XcodeVersionInfo {
	out := XcodeVersionInfo{Version: "unknown", Build: "unknown"} //nolint:goconst // test fixture / generic identifier — extracting would explode setup boilerplate.

	for line := range strings.SplitSeq(body, "\n") {
		m := xcodePbxKeyValue.FindStringSubmatch(line)
		if m == nil {
			continue
		}

		key := m[1]
		val := strings.TrimSpace(strings.Trim(strings.TrimSpace(m[2]), `"`))

		switch key {
		case "MARKETING_VERSION":
			if out.Version == "unknown" {
				out.Version = val
			}
		case "CURRENT_PROJECT_VERSION":
			if out.Build == "unknown" {
				out.Build = val
			}
		}

		if out.Version != "unknown" && out.Build != "unknown" {
			return out
		}
	}

	return out
}

// XcodeSummaryInput drives RenderXcodeSummary.
type XcodeSummaryInput struct {
	XcodeVersion  string
	Scheme        string
	Configuration string
	Destination   string
	Signing       bool
	Version       string
	BuildNumber   string
	IPAName       string
}

// RenderXcodeSummary returns the markdown block written by.
func RenderXcodeSummary(in XcodeSummaryInput, now time.Time) string {
	var builder strings.Builder

	_, _ = fmt.Fprintf(&builder, "## Xcode Build Summary 📱\n\n")
	_, _ = fmt.Fprintf(&builder, "### Configuration\n")
	_, _ = fmt.Fprintf(&builder, "| Setting | Value |\n")
	_, _ = fmt.Fprintf(&builder, "|---------|-------|\n")
	_, _ = fmt.Fprintf(&builder, "| **Xcode** | %s |\n", summary.LiteralText(in.XcodeVersion))
	_, _ = fmt.Fprintf(&builder, "| **Scheme** | %s |\n", summary.LiteralText(in.Scheme))
	_, _ = fmt.Fprintf(&builder, "| **Configuration** | %s |\n", summary.LiteralText(in.Configuration))
	_, _ = fmt.Fprintf(&builder, "| **Destination** | %s |\n", summary.LiteralText(in.Destination))
	_, _ = fmt.Fprintf(&builder, "| **Signing** | %s |\n", boolStatus(in.Signing))

	if in.Version != "" && in.Version != "unknown" {
		_, _ = fmt.Fprintf(&builder, "| **Version** | %s (%s) |\n", summary.LiteralText(in.Version), summary.LiteralText(in.BuildNumber))
	}

	_, _ = fmt.Fprintf(&builder, "\n### Artifacts Generated\n")

	if in.Signing {
		_, _ = fmt.Fprintf(&builder, "✓ IPA: %s\n", summary.InlineCode(in.IPAName))
	} else {
		_, _ = fmt.Fprintf(&builder, "✓ Archive: %s\n", summary.InlineCode(in.IPAName+"-archive"))
	}

	_, _ = fmt.Fprintf(&builder, "\n*Build completed at %s*\n", now.UTC().Format("2006-01-02 15:04:05 UTC"))

	return builder.String()
}
