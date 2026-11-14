// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package artifact wires `forge-ci artifact upload|download` — the
// forge-neutral run-artifact store (the actions/upload-artifact and
// actions/download-artifact equivalents). The surface is identical on
// every forge; the detected provider supplies the transport, and a forge
// that cannot perform an operation fails with a typed "not supported"
// rather than silently doing the wrong thing.
//
// The transfer flags read no environment. They mirror the `with:` inputs of
// upload-artifact and download-artifact, which a step states explicitly, while
// the generic names they once read are project settings elsewhere: a job that
// sets ARTIFACT_NAME as its project name handed that name to every download in
// the job, and `--pattern` then failed with "use --name or --pattern".
// Only the run and repository, which describe the run itself, fall back to it.
package artifact

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	appartifact "github.com/diggsweden/forge-ci/internal/app/artifact"
	"github.com/diggsweden/forge-ci/internal/cli/cienv"
	"github.com/diggsweden/forge-ci/internal/cli/cmdmeta"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/runcontext"
)

// flagDir is the shared --dir flag name across the artifact subcommands.
const flagDir = "dir"

// flagBlobStore names the run-scoped, content-addressed store an OCI layout's
// blobs travel through instead of inside the artifact.
const flagBlobStore = "blob-store"

// New returns the `artifact` subgroup command tree.
func New() *cli.Command {
	return &cli.Command{
		Name:  "artifact",
		Usage: "upload/download per-run CI artifacts",
		Commands: []*cli.Command{
			uploadCmd(),
			downloadCmd(),
			digestCmd(),
		},
	}
}

func uploadCmd() *cli.Command {
	return &cli.Command{
		Name:  "upload",
		Usage: "upload files as a named artifact into the current workflow run",
		Description: cmdmeta.InCINote + `EXAMPLES (as a workflow step):
   # Upload a directory under a logical name
   forge-ci artifact upload --name dist --dir ./dist

   # Upload by glob, preserving directory structure
   forge-ci artifact upload --name reports --path "reports/**/*.xml"

   # Upload explicit files with a fixed retention
   forge-ci artifact upload --name logs --file build.log --file test.log --retention-days 7

   # Upload an OCI layout, storing each large blob once per run under its digest
   forge-ci artifact upload --name images-layout-app-amd64 --dir ./staged/app-arch-amd64 \
     --blob-store images-blob --include-hidden`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "name", Required: true, Usage: "logical artifact name"},
			&cli.StringFlag{Name: flagDir, Usage: "directory whose contents are uploaded (or use --file/--path)"},
			&cli.StringSliceFlag{Name: "file", Usage: "explicit file to upload (repeatable, flattened to basename)"},
			&cli.StringSliceFlag{Name: "path", Usage: "glob pattern preserving structure (*, **, [set], !exclude; repeatable or newline-separated) — the upload-artifact path: contract"},
			&cli.IntFlag{Name: "retention-days", Usage: "retention in days (0 = forge default)"},
			&cli.StringFlag{Name: "if-no-files", Value: "error", Usage: "when nothing matches: error | warn | ignore"},
			&cli.BoolFlag{Name: "include-hidden", Usage: "include dotfiles/hidden files (off by default, matching upload-artifact)"},
			&cli.BoolFlag{Name: "verify-readback", Usage: "download once and verify exact selected paths/content before emitting success (requires --if-no-files error; no --blob-store)"},
			&cli.StringFlag{Name: "temp-dir", Sources: cienv.TempDir(), Usage: "existing directory for the --verify-readback download (default: the system temp)"},
			&cli.StringFlag{Name: flagBlobStore, Usage: "with --dir: upload each OCI layout blob (blobs/sha256/<digest>, 1 MiB or more) as its own artifact <prefix>-<digest>-<owner>, skipped when the run already stores that digest; download with the same prefix"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return deps.FromCmd(ctx, cmd, func(dep *deps.Deps) error {
				if cmd.Bool("verify-readback") && cmd.String(flagBlobStore) != "" {
					return fmt.Errorf("--verify-readback does not support --blob-store: %w", errs.ErrUsage)
				}

				up, err := dep.RequireRunArtifactUploader()
				if err != nil {
					return err
				}

				in := provider.RunArtifactUpload{
					Name:          cmd.String("name"),
					Dir:           cmd.String(flagDir),
					Files:         cmd.StringSlice("file"),
					Paths:         cmd.StringSlice("path"),
					RetentionDays: cmd.Int("retention-days"),
					IfNoFiles:     provider.IfNoFilesPolicy(cmd.String("if-no-files")),
					IncludeHidden: cmd.Bool("include-hidden"),
				}

				if prefix := cmd.String(flagBlobStore); prefix != "" {
					ls, lsErr := dep.RequireRunArtifactLister()
					if lsErr != nil {
						return lsErr
					}

					dl, dlErr := dep.RequireRunArtifactDownloader()
					if dlErr != nil {
						return dlErr
					}

					_, err = appartifact.UploadWithBlobStore(ctx, uploadLister{up, ls, dl}, dep.OutputSink, os.Stderr,
						appartifact.BlobStoreUpload{RunArtifactUpload: in, Prefix: prefix})

					return err
				}

				if cmd.Bool("verify-readback") {
					dl, dlErr := dep.RequireRunArtifactDownloader()
					if dlErr != nil {
						return dlErr
					}

					_, err = appartifact.UploadVerified(ctx, up, dl, dep.OutputSink, os.Stderr, in, cmd.String("temp-dir"))
				} else {
					_, err = appartifact.Upload(ctx, up, dep.OutputSink, os.Stderr, in)
				}

				return err
			})
		},
	}
}

func downloadCmd() *cli.Command {
	return &cli.Command{
		Name:  "download",
		Usage: "download a named run artifact into a directory",
		Description: cmdmeta.InCINote + `EXAMPLES (as a workflow step):
   # Download one named artifact into the current directory
   forge-ci artifact download --name dist

   # Download every artifact matching a glob, flattened into one directory
   forge-ci artifact download --pattern "sbom-*" --dir ./sboms --merge-multiple

   # Download from a different run/repository (both default to the current ones)
   forge-ci artifact download --name dist --run-id 123 --repository examplescope/myapp

   # Download an OCI layout and restore its blobs from the run's blob store,
   # each verified against its digest
   forge-ci artifact download --name images-layout-app-amd64 --dir ./app-arch-amd64 \
     --blob-store images-blob`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "name", Usage: "exact artifact name to download (omit with --pattern)"},
			&cli.StringFlag{Name: "pattern", Usage: "glob over artifact names; downloads every match (alternative to --name)"},
			&cli.BoolFlag{Name: "merge-multiple", Usage: "with --pattern: flatten all matches into --dir instead of --dir/<name>/"},
			&cli.StringFlag{Name: flagDir, Value: ".", Usage: "destination directory"},
			&cli.StringFlag{Name: "run-id", Sources: cienv.RunID(), Usage: "run the artifact belongs to (default: current run)"},
			&cli.StringFlag{Name: "repository", Sources: cienv.Repository(), Usage: "owner/repo the run belongs to (default: current repo)"},
			&cli.StringFlag{Name: flagBlobStore, Usage: "with --name, current run only: restore the blobs the artifact was uploaded without from this blob store, each verified against its sha256 digest"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return deps.FromCmd(ctx, cmd, func(dep *deps.Deps) error {
				dl, err := dep.RequireRunArtifactDownloader()
				if err != nil {
					return err
				}

				in := provider.RunArtifactDownload{
					Name:          cmd.String("name"),
					Pattern:       cmd.String("pattern"),
					MergeMultiple: cmd.Bool("merge-multiple"),
					Dir:           cmd.String(flagDir),
					RunID:         cmd.String("run-id"),
					Repository:    cmd.String("repository"),
				}

				if prefix := cmd.String(flagBlobStore); prefix != "" {
					if in.RunID != "" && in.RunID != runcontext.RunID().Resolve(os.Getenv) {
						return fmt.Errorf("--blob-store reads the current run's store, not run %q: %w", in.RunID, errs.ErrUsage)
					}

					ls, lsErr := dep.RequireRunArtifactLister()
					if lsErr != nil {
						return lsErr
					}

					_, err = appartifact.DownloadWithBlobStore(ctx, downloadLister{dl, ls}, dep.OutputSink, os.Stderr,
						appartifact.BlobStoreDownload{RunArtifactDownload: in, Prefix: prefix})

					return err
				}

				_, err = appartifact.Download(ctx, dl, dep.OutputSink, os.Stderr, in)

				return err
			})
		},
	}
}

// uploadLister and downloadLister join the separately required roles the
// blob store needs; on every forge both come from the same provider.
type uploadLister struct {
	provider.RunArtifactUploader
	provider.RunArtifactLister
	provider.RunArtifactDownloader
}

type downloadLister struct {
	provider.RunArtifactDownloader
	provider.RunArtifactLister
}
