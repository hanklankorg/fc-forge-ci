// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package platform

import (
	"context"
	"fmt"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/archive"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// extractArchiveCmd wires `forge-ci platform extract-archive`: unpack a
// downloaded, already verified tool archive with the engine's hardened
// extractor, so an install needs neither xz nor a particular tar on the
// runner.
func extractArchiveCmd() *cli.Command {
	return &cli.Command{
		Name:  "extract-archive",
		Usage: "unpack a .tar.gz or .tar.xz into a directory, dropping its top directory",
		Description: `As tar -xf <archive> --strip-components=1, with the engine's own
decompressor: absolute, parent-traversing and escaping entries, devices and
oversized archives are refused, and nothing reaches the directory until every
entry is checked. Verify the archive's checksum first.

EXAMPLE:
   forge-ci platform extract-archive --archive tool.tar.xz --destination bin/tool`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "archive", Required: true, Usage: "the .tar.gz, .tgz or .tar.xz to unpack"},
			&cli.StringFlag{Name: "destination", Required: true, Usage: "directory the entries are written to"},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			file, dest := cmd.String("archive"), cmd.String("destination")

			switch {
			case strings.HasSuffix(file, ".tar.xz"):
				return archive.UntarXzStripOne(file, dest)
			case strings.HasSuffix(file, ".tar.gz"), strings.HasSuffix(file, ".tgz"):
				return archive.UntarStripOne(file, dest)
			default:
				return fmt.Errorf("archive %s is not a .tar.gz, .tgz or .tar.xz: %w", file, errs.ErrUsage)
			}
		},
	}
}
