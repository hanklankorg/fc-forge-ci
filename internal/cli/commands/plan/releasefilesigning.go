// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package plan

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	appconfig "github.com/diggsweden/forge-ci/internal/app/config"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
)

// The output the Forgejo and GitLab releases' signing steps are conditioned
// on, and its two values.
const (
	outputReleaseFileSigning = "release-file-signing"
	releaseFileSigningGPG    = "gpg"
	releaseFileSigningCosign = "cosign"
)

// releaseFileSigningCmd reads, for the Forgejo and GitLab releases, which key
// signs the release's checksums and SBOMs.
func releaseFileSigningCmd() *cli.Command {
	return &cli.Command{
		Name:  "release-file-signing",
		Usage: "say whether the Forgejo or GitLab release signs its checksums and SBOMs with GPG or with its Cosign key",
		Description: `Reads the declaration and sets the output release-file-signing: gpg
   when sign.method or sign.files-method is gpg, and cosign otherwise, also
   without a sign block and without the file. The Forgejo and GitLab releases
   sign the checksums and SBOMs the way that output says. The provenance
   statement, the evidence and the images are signed with the Cosign key
   either way.

   Without --file and without .forge-ci/artifacts.yml the answer is cosign; a
   file named by --file must exist.

EXAMPLE:
   forge-ci plan release-file-signing --file .forge-ci/artifacts.yml`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: flagFile, Usage: usageReleaseFile},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return deps.FromCmd(ctx, cmd, func(dep *deps.Deps) error {
				file := cmd.String(flagFile)
				if file == "" {
					file = defaultGitLabArtifactsConfig
				}

				plans, err := appconfig.LoadReleaseStagePlans(appconfig.EmitConfigPlanInput{Path: file}, deps.Annotator(cmd), cmd.String(flagFile) == "")
				if err != nil {
					return err
				}

				signing := releaseFileSigningCosign
				if plans.Sign.DeclaresGPG() {
					signing = releaseFileSigningGPG
				}

				_, _ = fmt.Fprintf(os.Stderr, "the release's checksums and SBOMs are signed with %s\n", signing)

				return dep.OutputSink.Set(ctx, outputReleaseFileSigning, signing)
			})
		},
	}
}
