// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/buildah"
	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/cli/regflags"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/listval"
)

const (
	flagImageRef  = "image-ref"
	flagLayoutDir = "layout-dir"
)

func layoutGroup() *cli.Command {
	return &cli.Command{Name: "layout", Usage: "record and publish OCI artifacts across a build/scan job barrier", Commands: []*cli.Command{layoutRecordCmd(), layoutPublishCmd(), layoutExtractFilesCmd(), layoutFetchInputCmd(), layoutVerifyInputCmd(), layoutImportInputCmd()}}
}

func layoutImportInputCmd() *cli.Command {
	return &cli.Command{
		Name: "import-input", Usage: "verify and import a retained parent into native Buildah storage without registry credentials",
		Flags: append(layoutRegistryInputFlags(), imageBuilderFlags()...),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			builder, err := imageBuilderFromCmd(cmd)
			if err != nil {
				return err
			}

			loader, ok := builder.(*buildah.Adapter)
			if !ok {
				return fmt.Errorf("input layout import requires Buildah: %w", errs.ErrUsage)
			}

			local, err := appcontainer.ImportRegistryInput(ctx, ociregistry.NewAnonymous(), loader, os.Stderr, cmd.String(flagImageRef), cmd.String(flagPlatform), cmd.String(flagLayoutDir))
			if err != nil {
				return err
			}

			return deps.FromCmd(ctx, cmd, func(dep *deps.Deps) error { return dep.OutputSink.Set(ctx, "image-ref", local) })
		},
	}
}

func layoutRegistryInputFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: flagImageRef, Required: true, Usage: "digest-pinned parent image from the admitted build policy"},
		&cli.StringFlag{Name: flagPlatform, Required: true, Usage: "linux/amd64 or linux/arm64"},
		&cli.StringFlag{Name: flagLayoutDir, Required: true, Usage: "single-platform input layout directory"},
	}
}

func layoutFetchInputCmd() *cli.Command {
	return &cli.Command{
		Name: "fetch-input", Usage: "fetch a pinned private parent into a run-scoped artifact without running consumer code",
		Flags: append(layoutRegistryInputFlags(), regflags.AuthFile(regflags.AuthFileOpts{})),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			registry := ociregistry.NewAnonymous()
			if authFile := cmd.String(flagAuthFile); authFile != "" {
				registry = ociregistry.WithAuthFile(authFile)
			}

			return appcontainer.FetchRegistryInput(ctx, registry, cmd.String(flagImageRef), cmd.String(flagPlatform), cmd.String(flagLayoutDir))
		},
	}
}

func layoutVerifyInputCmd() *cli.Command {
	return &cli.Command{
		Name: "verify-input", Usage: "verify downloaded parent bytes against independently pinned source and platform, offline",
		Flags: layoutRegistryInputFlags(),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			_, err := appcontainer.VerifyRegistryInput(ctx, ociregistry.NewAnonymous(), cmd.String(flagImageRef), cmd.String(flagPlatform), cmd.String(flagLayoutDir))

			return err
		},
	}
}

func layoutExtractFilesCmd() *cli.Command {
	return &cli.Command{
		Name: "extract-files", Usage: "copy selected executables from the retained image filesystem without rebuilding",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: flagLayoutDir, Required: true, Usage: "verified single-image OCI layout directory"},
			&cli.StringFlag{Name: flagOutputDir, Required: true, Usage: "directory receiving unique executable basenames; existing files are never overwritten"},
			&cli.StringFlag{Name: "paths", Required: true, Sources: cli.EnvVars("EXTRACT_BINARY_PATHS"), Usage: "comma/space-separated regular-file paths inside the image"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return appcontainer.ExtractLayoutFiles(ctx, ociregistry.NewAnonymous(), os.Stderr, cmd.String(flagLayoutDir), cmd.String(flagOutputDir), listval.Tokens(cmd.String("paths")))
		},
	}
}

func layoutIdentityFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: flagImageRef, Required: true, Sources: cli.EnvVars("IMAGE_REF"), Usage: "expected explicit image repository (record also accepts a tag)"},
		&cli.StringFlag{Name: flagSourceSHA, Required: true, Sources: cli.EnvVars("SOURCE_SHA"), Usage: "full source commit; must match the OCI revision label"},
	}
}

func layoutRecordCmd() *cli.Command {
	return &cli.Command{
		Name: "record", Usage: "verify a built/scanned layout and record its digest, platform, source and destination",
		Description: "Run after the configured build/scan succeeds, then upload the complete directory as one run-scoped artifact. Verifies local blobs without pulling or publishing. Uses artifact limits: 5 GiB per file, 10 GiB and 10,000 files per layout. Download limits also apply across the complete selected set; the instance may impose smaller limits.",
		Flags: append(layoutIdentityFlags(),
			&cli.StringFlag{Name: flagLayoutDir, Required: true, Usage: "directory produced by container build --mode oci"},
			&cli.StringFlag{Name: flagPlatform, Required: true, Sources: cli.EnvVars("PLATFORM"), Usage: "expected linux/amd64 or linux/arm64"}),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return deps.FromCmd(ctx, cmd, func(dep *deps.Deps) error {
				record, err := appcontainer.RecordImageLayout(ctx, ociregistry.NewAnonymous(), appcontainer.LayoutRecordInput{
					Directory: cmd.String(flagLayoutDir), ImageRef: cmd.String(flagImageRef), SourceSHA: cmd.String(flagSourceSHA), Platform: cmd.String(flagPlatform),
				})
				if err != nil {
					return err
				}

				return dep.OutputSink.Set(ctx, flagDigest, record.Digest)
			})
		},
	}
}

func layoutPublishCmd() *cli.Command {
	return &cli.Command{
		Name: subCmdPublish, Usage: "verify the complete staged platform set before uploading any image",
		Description: "Run only in a job depending on ALL selected build/scan jobs. Download the current run's artifacts as separate subdirectories, without merging their files. Checks every local blob and binding before the first registry write; emits platform-digests JSON after all uploads and read-backs succeed. Bare repositories publish tagless by digest. Partial upload failures may leave unreferenced images; retries reuse the same layouts.",
		Flags: append(layoutIdentityFlags(),
			&cli.StringFlag{Name: "layouts-dir", Required: true, Usage: "parent containing exactly one artifact directory per selected platform"},
			&cli.StringFlag{Name: "platforms", Required: true, Sources: cli.EnvVars("PLATFORMS"), Usage: "complete comma/space-separated expected platform set"},
			regflags.AuthFile(regflags.AuthFileOpts{}), regflags.TLSVerify(regflags.TLSVerifyOpts{}),
			&cli.IntFlag{Name: flagRetryAttempts, Value: 3, Usage: "registry attempts"},
			&cli.IntFlag{Name: flagRetryDelaySeconds, Value: 10, Usage: "base registry retry delay"}),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return deps.FromCmd(ctx, cmd, func(dep *deps.Deps) error {
				platforms := listval.Tokens(cmd.String("platforms"))

				inputs, err := appcontainer.LayoutSetInputs(cmd.String("layouts-dir"), cmd.String("image-ref"), cmd.String(flagSourceSHA), platforms)
				if err != nil {
					return err
				}

				auth := regflags.Resolve(cmd)

				digests, err := appcontainer.PublishImageLayouts(ctx, ociregistry.WithAuthFileAndTLS(auth.AuthFile, auth.TLSVerify != tlsVerifyDisabled), os.Stderr, inputs, cmd.Int(flagRetryAttempts), time.Duration(cmd.Int(flagRetryDelaySeconds))*time.Second)
				if err != nil {
					return err
				}

				body, err := json.Marshal(digests)
				if err != nil {
					return err
				}

				return dep.OutputSink.Set(ctx, "platform-digests", string(body))
			})
		},
	}
}
