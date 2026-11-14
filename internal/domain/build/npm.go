// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build

import (
	"fmt"
	"strings"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/summary"
)

// NPMSummaryInput drives RenderNPMSummary.
type NPMSummaryInput struct {
	PackageName string
	Version     string
	NodeVersion string
	SkipTests   bool
}

// RenderNPMSummary returns the markdown block written by.
func RenderNPMSummary(in NPMSummaryInput, now time.Time) string {
	var builder strings.Builder

	_, _ = fmt.Fprintf(&builder, "## NPM Build Summary 🔨\n")
	_, _ = fmt.Fprintf(&builder, "\n")
	_, _ = fmt.Fprintf(&builder, "- **Package:** %s\n", summary.InlineCode(in.PackageName+"@"+in.Version))
	_, _ = fmt.Fprintf(&builder, "- **Node.js:** %s\n", summary.LiteralText(in.NodeVersion))

	if in.SkipTests {
		_, _ = fmt.Fprintf(&builder, "- **Tests:** ⊘ Skipped\n")
	} else {
		_, _ = fmt.Fprintf(&builder, "- **Tests:** ✓ Executed\n")
	}

	_, _ = fmt.Fprintf(&builder, "\n")
	_, _ = fmt.Fprintf(&builder, "*Build completed at %s*\n", now.UTC().Format("2006-01-02 15:04:05 UTC"))

	return builder.String()
}
