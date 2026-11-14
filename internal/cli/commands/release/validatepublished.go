// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/cli/cienv"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
)

func validatePublishedCmd() *cli.Command {
	flags := append([]cli.Flag{
		&cli.StringFlag{Name: flagTag, Required: true, Sources: cienv.Tag(), Usage: "tag of the published release (e.g. v1.2.3)"},
		&cli.StringFlag{Name: flagRepository, Required: true, Sources: cienv.Repository(), Usage: "\"owner/repo\" release repository"},
		&cli.BoolFlag{Name: "draft", Sources: cli.EnvVars("RELEASE_DRAFT", "DRAFT"), Usage: "the release was published as a draft and may still be one; the token must be able to read drafts"},
		&cli.StringSliceFlag{Name: flagAsset, Usage: "planned release file (repeatable); defaults to the assets of the finalized --manifest"},
		&cli.StringFlag{Name: flagAssembly, Sources: cli.EnvVars("RELEASE_ASSEMBLY"), Usage: "release assembly manifest whose assets were published (--strategy=recreate)"},
	}, releaseFilesFlags()...)

	return &cli.Command{
		Name:  "validate-published",
		Usage: "read the published release back from the forge and compare its files with the planned ones",
		Description: `Run after "release publish", with the input that named its files: the
finalized --manifest, the --assembly, or the --asset values. The planned
files are the ones publish uploads from that input, signatures and the
provenance statement included; no separate list is kept.

The command only reads, so the token needs no more than read access to the
repository's releases, unless the release is a draft. It fails, naming each
file, when a planned file is not on the release or has another size there
(GitLab reports no size, so only the name is compared), or another SHA-256
digest where the forge reports one (GitHub; Forgejo and GitLab report
none), when GitHub reports no SHA-256 digest for it, when the tag has no
release, or on GitHub two, and when the release is still a draft without
--draft. Only that one release is read.

A file on the release that the plan does not name is a warning, not a
failure: "release attachments upload" and a repository's own jobs add files
to a published release.

A draft is compared like any other release. A forge hides a draft from a
token that cannot write to the repository's releases, so read a draft back
with the token that published it: with --draft a release that is not
visible fails the command, as without it.

EXAMPLES:
   # The files of the finalized release file manifest
   forge-ci release validate-published --tag=v1.2.3 --repository=examplescope/myapp \
       --manifest=dist/release-files.json

   # The files of an assembly manifest
   forge-ci release validate-published --tag=v1.2.3 --repository=examplescope/myapp \
       --assembly=.forge-ci/release-assembly.json`,
		Flags: flags,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return deps.FromCmd(ctx, cmd, func(d *deps.Deps) error {
				reader, err := d.RequireReleaseReader()
				if err != nil {
					return err
				}

				return apprelease.ValidatePublishedRelease(ctx, reader, os.Stderr, deps.Annotator(cmd), validatePublishedInput(cmd))
			})
		},
	}
}

func validatePublishedInput(cmd *cli.Command) apprelease.ValidatePublishedInput {
	return apprelease.ValidatePublishedInput{
		Tag:          cmd.String(flagTag),
		Repository:   cmd.String(flagRepository),
		Draft:        cmd.Bool("draft"),
		AssemblyFile: cmd.String(flagAssembly),
		Assets:       cmd.StringSlice(flagAsset),
		Files:        releaseFilesInput(cmd),
	}
}
