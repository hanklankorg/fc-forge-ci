// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package sbom

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	appsbom "github.com/diggsweden/forge-ci/internal/app/sbom"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
)

func indexCmd() *cli.Command {
	return &cli.Command{
		Name:  "index",
		Usage: "compose a checksum-bound index SBOM from unchanged platform SBOMs without network access",
		Description: `Reads a version-1 platform manifest with platform, image_ref,
file, url and sha256 fields. Input files are relative to --root. The index bytes
must match --image-ref and contain exactly the declared platform/image pairs.
The output is CycloneDX 1.6 with checksum-bound references to the native BOMs.
Register every referenced file as release evidence before publication, and pass
sbom-sha256 to the image ledger so the signer attests this exact document.
This checks document headers and byte bindings, not full CycloneDX schema validity
or the accuracy of the producer's package inventory. URLs are never fetched.`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "image-ref", Required: true, Usage: "tag-free digest-pinned index reference"},
			&cli.StringFlag{Name: "root", Value: ".", Usage: "root directory of all input files"},
			&cli.StringFlag{Name: "index-file", Required: true, Usage: "raw OCI index JSON, relative to root"},
			&cli.StringFlag{Name: "platform-sboms", Required: true, Usage: "version-1 platform SBOM record manifest, relative to root"},
			&cli.StringFlag{Name: "output", Required: true, Usage: "output CycloneDX file, relative to the working directory; '-' is not supported"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return deps.FromCmd(ctx, cmd, func(dep *deps.Deps) error {
				result, err := appsbom.ComposeIndex(appsbom.IndexBOMInput{
					IndexRef: cmd.String("image-ref"), Root: cmd.String("root"), IndexFile: cmd.String("index-file"),
					PartsFile: cmd.String("platform-sboms"), Output: cmd.String("output"),
				})
				if err != nil {
					return err
				}

				for _, item := range []struct{ key, value string }{
					{"sbom-path", result.Path}, {"sbom-sha256", result.SHA256},
				} {
					if dep.OutputSink != nil {
						if err = dep.OutputSink.Set(ctx, item.key, item.value); err != nil {
							return err
						}
					}
				}

				if dep.OutputSink != nil {
					if err = dep.OutputSink.SetMultiline(ctx, "evidence-files", result.EvidenceFiles); err != nil {
						return err
					}
				}

				_, err = fmt.Fprintln(os.Stdout, result.SHA256)

				return err
			})
		},
	}
}
