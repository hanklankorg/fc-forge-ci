// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
)

func multiarchImageVerifyCmd() *cli.Command {
	return &cli.Command{
		DisableSliceFlagSeparator: true,
		Name:                      "verify",
		Usage:                     "verify an existing digest-pinned index against the complete architecture metadata set without publishing",
		Flags: append(append(signerImageSourceFlags(), multiarchImageParamFlags()...),
			&cli.StringFlag{Name: "ref", Required: true, Usage: "tag-free digest-pinned index reference"},
			multiarchImageArchsFlag(),
			&cli.StringSliceFlag{Name: flagRequireLabel, Usage: "required child config label as key=value (repeatable)"},
			&cli.StringFlag{Name: flagManifestOutput, Usage: "optional file for exact verified index bytes; '-' is not supported; no registry writes"},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return deps.FromCmd(ctx, cmd, func(dep *deps.Deps) error {
				in := appcontainer.SignerImageAssembleInput{
					AuthFile: cmd.String(flagAuthFile), SourceSHA: cmd.String(flagSourceSHA),
					ServerURL: cmd.String(flagServerURL), Repository: cmd.String(flagRepository),
					RepositorySuffix: cmd.String("repository-suffix"), ImageRepository: cmd.String(flagImageRepository),
					TagPrefix: cmd.String("tag-prefix"), TagName: cmd.String("tag"), Name: cmd.String(flagName),
					Archs: splitSignerLines(cmd.String(flagArchs)), RequiredLabels: cmd.StringSlice(flagRequireLabel),
					ManifestOutput: cmd.String(flagManifestOutput),
				}
				if err := appcontainer.VerifyMultiarchImageManifest(ctx, ociregistry.WithAuthFile(in.AuthFile), dep.OutputSink, cmd.String("ref"), in); err != nil {
					return err
				}

				_, err := fmt.Fprintln(os.Stdout, cmd.String("ref"))

				return err
			})
		},
	}
}
