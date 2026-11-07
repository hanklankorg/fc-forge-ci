// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/cosign"
	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/cli/signflags"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
)

func runtimePublicationSignCmd() *cli.Command {
	return &cli.Command{
		Name: "sign", Usage: "verify retained runtime evidence, recursively sign its index and attest native SBOMs and source provenance",
		Description: "Run from the downloaded evidence directory containing image.json, sboms.sha256, sbom-<arch>.json and per-architecture metadata. All native SBOMs and the exact index are validated before the first signing mutation; no SBOM is generated here.",
		Flags: append(append(append(signerImageSourceFlags(), multiarchImageParamFlags()...), signflags.Cosign(signflags.CosignOpts{MethodNote: cosignMethodNoteGPG})...),
			multiarchImageArchsFlag(),
			&cli.StringFlag{Name: flagFlavor, Required: true, Usage: "product runtime target recorded in the source provenance"},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			method, err := domainrelease.ParseSignMethod(cmd.String("method"))
			if err != nil {
				return err
			}

			endpoints := signflags.ReadEndpoints(cmd)
			prov := provenanceFromEnv("")
			prov.Flavor = cmd.String(flagFlavor)

			return withReleaseImageDockerConfig(cmd.String(flagAuthFile), func() error {
				return appcontainer.SignRuntimePublication(ctx,
					cosign.ForKeyRef(cmd.String("key"), "DOCKER_CONFIG"),
					ociregistry.WithAuthFile(cmd.String(flagAuthFile)), os.Stderr,
					appcontainer.RuntimePublicationSignInput{
						Manifest: runtimeManifestInput(cmd),
						Signing: appcontainer.SignImageInput{
							Method: method, KeyRef: cmd.String("key"), OIDCIssuer: cmd.String("oidc-issuer"),
							FulcioURL: endpoints.FulcioURL, RekorURL: endpoints.RekorURL, TrustedRootPath: endpoints.TrustedRootPath,
						},
						Provenance: prov,
					})
			})
		},
	}
}
