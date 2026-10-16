// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"io"
	"os"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/buildah"
	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/cli/cienv"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/cli/regflags"
)

func signerImageGroup() *cli.Command {
	return &cli.Command{
		Name:  "multiarch-image",
		Usage: "build a multi-arch OCI image in two phases (per-arch build, then manifest assemble)",
		Description: `Two-phase multi-arch image build for CI that fans architectures out
across parallel jobs: each job runs build-arch (build+push one architecture,
record its digest), then one job runs assemble (gather the per-arch digests into
a multi-arch manifest). Use this when arch builds must run as separate jobs;
container build-push-oci-image builds every platform in a single job instead.

The repository suffix, tag prefix, metadata name, Containerfile, and image title
are all parameters, so this serves any multi-arch image (a signer image, a base
image, an app image) — the caller supplies the specifics.

Use --image-repository to publish to a registry separate from the source forge.
Pass the same destination to both phases; source URL and revision labels still
identify --server-url, --repository and --source-sha.

For a whole-matrix barrier, use build-arch --stage, transfer each metadata
directory INCLUDING its layout, then run assemble --from-layouts only after ALL
build/scan jobs succeed. The complete local set is verified before any image
upload. Without these flags the existing immediate-publish behavior is retained.`,
		Commands: []*cli.Command{
			signerImageBuildArchCmd(),
			signerImageAssembleCmd(),
		},
	}
}

// multiarchImageParamFlags are the caller-supplied specifics that make the
// two-phase build general: the repository suffix, the source-SHA tag prefix, and
// the metadata basename shared by build-arch (write) and assemble (read). Empty
// suffix/prefix and a neutral "image" name are the forge/consumer-neutral
// defaults; a caller (e.g. a signer image) overrides them.
func multiarchImageParamFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "repository-suffix", Sources: cli.EnvVars("IMAGE_REPOSITORY_SUFFIX"), Usage: "suffix for the derived image repository (e.g. -signer); unused with --image-repository"},
		&cli.StringFlag{Name: "image-repository", Sources: cli.EnvVars("IMAGE_REPOSITORY"), Usage: "full destination OCI repository without a tag or digest; overrides the derived destination, preserving source labels"},
		&cli.StringFlag{Name: "tag-prefix", Sources: cli.EnvVars("IMAGE_TAG_PREFIX"), Usage: "prefix prepended to the source-SHA image and manifest tags (e.g. signer-)"},
		&cli.StringFlag{Name: "name", Value: "image", Sources: cli.EnvVars("IMAGE_METADATA_NAME"), Usage: "basename for the per-arch/manifest metadata files and dirs shared across the two phases"},
	}
}

func signerImageBuildArchCmd() *cli.Command {
	return &cli.Command{
		Name:  "build-arch",
		Usage: "build and push one architecture of the multi-arch image",
		Description: `Builds one image architecture, pushes its temporary architecture
tag, computes the registry-served manifest digest, and writes <name>-<arch>.json
for the manifest assembly job.`,
		Flags: append(append(signerImageCommonFlags(), multiarchImageParamFlags()...),
			&cli.BoolFlag{Name: "stage", Usage: "retain the OCI layout with architecture metadata; defer ALL uploads to assemble --from-layouts after every build job succeeds"},
			&cli.StringFlag{Name: flagArch, Sources: cli.EnvVars("IMAGE_ARCH"), Usage: "target architecture: amd64 or arm64"},
			&cli.StringFlag{Name: flagContainerfile, Value: "Containerfile", Sources: cli.EnvVars("CONTAINERFILE"), Usage: "Containerfile to build"},
			&cli.StringFlag{Name: flagContext, Value: ".", Sources: cli.EnvVars("BUILD_CONTEXT"), Usage: "directory for the build context"},
			&cli.StringFlag{Name: flagTitle, Sources: cli.EnvVars("IMAGE_TITLE"), Usage: "org.opencontainers.image.title label value for the built image"},
			&cli.StringFlag{Name: "metadata-dir", Sources: cli.EnvVars("IMAGE_METADATA_DIR"), Usage: "directory for <name>-<arch>.json (default <name>-arch-<arch>)"},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			input := appcontainer.SignerImageBuildArchInput{
				AuthFile:         cmd.String(flagAuthFile),
				Arch:             cmd.String(flagArch),
				SourceSHA:        cmd.String(flagSourceSHA),
				ServerURL:        cmd.String(flagServerURL),
				Repository:       cmd.String(flagRepository),
				RepositorySuffix: cmd.String("repository-suffix"),
				ImageRepository:  cmd.String("image-repository"),
				TagPrefix:        cmd.String("tag-prefix"),
				Name:             cmd.String("name"),
				Title:            cmd.String("title"),
				Containerfile:    cmd.String(flagContainerfile),
				Context:          cmd.String(flagContext),
				MetadataDir:      cmd.String("metadata-dir"),
				RetryAttempts:    cmd.Int(flagRetryAttempts),
				RetryDelay:       time.Duration(cmd.Int(flagRetryDelaySeconds)) * time.Second,
			}
			if cmd.Bool("stage") {
				_, err := appcontainer.StageSignerImageArch(ctx, signerImageTool{buildah.New()}, ociregistry.NewAnonymous(), os.Stderr, input)

				return err
			}

			_, err := appcontainer.BuildSignerImageArch(ctx, signerImageTool{buildah.New()}, os.Stderr, input)

			return err
		},
	}
}

func signerImageAssembleCmd() *cli.Command {
	return &cli.Command{
		Name:  "assemble",
		Usage: "assemble and push the multi-arch manifest from the per-arch builds",
		Description: `Reads <name>-arch-<arch>/<name>-<arch>.json metadata, validates
that every source image is digest-pinned under the expected repository, assembles
the multi-arch manifest, and emits image-ref, image-digest, and image-tag
outputs.`,
		Flags: append(append(signerImageCommonFlags(), multiarchImageParamFlags()...),
			&cli.BoolFlag{Name: "from-layouts", Usage: "verify all staged architecture layouts before any upload, then publish and assemble; requires a successful whole-matrix dependency"},
			&cli.StringFlag{Name: "archs", Value: "amd64\narm64", Sources: cli.EnvVars("IMAGE_ARCHS"), Usage: "newline-separated architectures to include"},
			&cli.StringFlag{Name: "metadata-dir", Sources: cli.EnvVars("IMAGE_METADATA_DIR"), Usage: "directory for <name>.json (default <name>-dist)"},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return deps.FromCmd(ctx, cmd, func(dep *deps.Deps) error {
				input := appcontainer.SignerImageAssembleInput{
					AuthFile:         cmd.String(flagAuthFile),
					SourceSHA:        cmd.String(flagSourceSHA),
					ServerURL:        cmd.String(flagServerURL),
					Repository:       cmd.String(flagRepository),
					RepositorySuffix: cmd.String("repository-suffix"),
					ImageRepository:  cmd.String("image-repository"),
					TagPrefix:        cmd.String("tag-prefix"),
					Name:             cmd.String("name"),
					Archs:            splitSignerLines(cmd.String("archs")),
					MetadataDir:      cmd.String("metadata-dir"),
					RetryAttempts:    cmd.Int(flagRetryAttempts),
					RetryDelay:       time.Duration(cmd.Int(flagRetryDelaySeconds)) * time.Second,
				}

				registry := ociregistry.WithAuthFile(cmd.String(flagAuthFile))
				if cmd.Bool("from-layouts") {
					_, err := appcontainer.PublishAndAssembleSignerImage(ctx, registry, dep.OutputSink, dep.SummarySink, os.Stderr, input)

					return err
				}

				_, err := appcontainer.AssembleSignerImageManifest(ctx, registry, dep.OutputSink, dep.SummarySink, os.Stderr, input)

				return err
			})
		},
	}
}

// signerImageTool builds/exports with Buildah and publishes/reads through the
// existing OCI registry adapter, using the same explicit auth file.
type signerImageTool struct{ *buildah.Adapter }

func (signerImageTool) PushLayout(ctx context.Context, authFile, layoutDir, image string) (string, error) {
	return ociregistry.WithAuthFile(authFile).PushLayout(ctx, layoutDir, image)
}

// RawManifest returns the manifest document the registry serves for image.
func (signerImageTool) RawManifest(ctx context.Context, authFile, image string, _ io.Writer) ([]byte, error) {
	return ociregistry.WithAuthFile(authFile).Manifest(ctx, image)
}

func signerImageCommonFlags() []cli.Flag {
	return []cli.Flag{
		regflags.AuthFile(regflags.AuthFileOpts{Env: "FORGE_CI_IMAGE_AUTH_FILE", Usage: "registry auth file used by buildah and the registry client"}),
		&cli.StringFlag{Name: flagSourceSHA, Sources: cli.EnvVars("SOURCE_SHA"), Usage: "git commit SHA used in image tags and the OCI revision label"},
		&cli.StringFlag{Name: flagServerURL, Sources: cienv.ServerURL(), Usage: "forge server URL used to derive the registry/repository"},
		&cli.StringFlag{Name: flagRepository, Sources: cienv.Repository(), Usage: "owner/repo used to derive the image repository"},
		&cli.IntFlag{Name: flagRetryAttempts, Value: 3, Sources: cli.EnvVars("IMAGE_RETRY_ATTEMPTS"), Usage: "registry push attempts"},
		&cli.IntFlag{Name: flagRetryDelaySeconds, Value: 10, Sources: cli.EnvVars("IMAGE_RETRY_DELAY_SECONDS"), Usage: "base delay between registry push retries"},
	}
}

func splitSignerLines(raw string) []string {
	var out []string

	for line := range strings.SplitSeq(raw, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}

	return out
}
