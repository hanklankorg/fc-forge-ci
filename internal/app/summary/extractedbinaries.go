// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package summary

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/ci"
	domsummary "github.com/diggsweden/forge-ci/internal/domain/summary"
)

// ExtractedBinariesInput drives ExtractedBinaries.
type ExtractedBinariesInput struct {
	Dir           string
	ArtifactName  string
	DisplayName   string
	ExtractTarget string
	ExpectedNames string
	Platform      string
	Limit         int
}

// ExtractedBinaries appends metadata and a file listing capped by Limit
// (default 50 for nonpositive values). This bounds listed entries, not
// discovery, retained candidate paths or report bytes.
func ExtractedBinaries(ctx context.Context, sink ci.SummarySink, in ExtractedBinariesInput) error {
	dir := in.Dir
	if dir == "" {
		dir = "./extracted-binaries"
	}

	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}

	var builder strings.Builder

	_, _ = fmt.Fprintf(&builder, "### Extracted Binaries - %s (%s)\n\n", domsummary.LiteralText(in.DisplayName), domsummary.LiteralText(in.Platform))
	_, _ = fmt.Fprintf(&builder, "- **Stage:** %s\n", domsummary.LiteralText(in.ExtractTarget))
	_, _ = fmt.Fprintf(&builder, "- **Artifact:** %s\n", domsummary.LiteralText(in.ArtifactName))
	_, _ = fmt.Fprintf(&builder, "- **Platform:** %s\n", domsummary.LiteralText(in.Platform))

	if strings.TrimSpace(in.ExpectedNames) != "" {
		_, _ = fmt.Fprintf(&builder, "- **Binaries:** %s\n", domsummary.LiteralText(in.ExpectedNames))
	}

	_, _ = fmt.Fprintf(&builder, "\nFiles in extracted-binaries/:\n\n")

	for _, path := range firstFiles(dir, limit) {
		_, _ = fmt.Fprintf(&builder, "%s\n", domsummary.InlineCode(path))
	}

	return sink.Append(ctx, builder.String())
}

func firstFiles(root string, limit int) []string {
	var files []string

	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		// Skip per-entry errors; the function returns whichever files
		// it could read up to the caller's limit.
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // skip unreadable entry, keep walking
		}

		files = append(files, path)

		return nil
	})

	slices.Sort(files)

	if len(files) > limit {
		return files[:limit]
	}

	return files
}
