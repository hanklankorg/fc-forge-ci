// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	"github.com/diggsweden/forge-ci/internal/adapters/syft"
	"github.com/diggsweden/forge-ci/internal/adapters/trivy"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	appsnapshot "github.com/diggsweden/forge-ci/internal/app/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/cli/cienv"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
)

// The release-images job verbs a forge without a build matrix runs: one job
// per architecture builds every planned image of it, and one job publishes
// them all. They carry out what the Forgejo release-images workflow's build
// and publish steps do, with the same engine use cases, from the plan file
// `release-images plan --output` wrote.
const (
	flagReleaseSHA = "release-sha"
	flagReleaseTag = "release-tag"
	flagLayoutsDir = "layouts-dir"
	flagSourceDir  = "source-dir"
	flagCreated    = "created"
)

func releaseImagesBuildCmd() *cli.Command {
	return nativeImagesBuildCmd(readReleaseImagesPlanFile)
}

// Both native boundaries share build execution, but retain distinct admission.
func nativeImagesBuildCmd(readPlan func(*cli.Command) (pipeline.ReleaseImagesPlan, error)) *cli.Command {
	return &cli.Command{
		Name:  subCmdBuild,
		Usage: "build every planned release image of one architecture to staged layouts, with no registry write",
		Description: `Reads the plan file release-images plan --output wrote and builds each
build of --arch on this native runner with the selected image builder, as
multiarch-image build-arch --stage does, into <metadata-dir>/<name>-arch-<arch>.
Nothing is pushed: release-images publish uploads the layouts once every
architecture is built. The images carry the source, revision, title and
version labels, the url, documentation, ref.name (the final tag) and created
labels, then the labels the plan declares. An architecture the plan does not
build is no error: the job has nothing to do.

EXAMPLE:
   forge-ci container release-images build --plan plan.json --arch amd64 --image-repository registry.example.com/group/app --release-tag v1.2.3 --release-sha "$(git rev-parse HEAD)" --created 2026-01-02T03:04:05Z --source-dir consumer --metadata-dir staged`,
		Flags: append(append(releaseImagesJobFlags(),
			&cli.StringFlag{Name: flagArch, Required: true, Usage: "architecture to build, the host's own: amd64 or arm64"},
			&cli.StringFlag{Name: flagSourceDir, Value: ".", Usage: "release checkout the plan's Containerfile and context paths are relative to"},
			&cli.StringFlag{Name: flagCreated, Required: true, Usage: "org.opencontainers.image.created value: the release commit's time, so the label names the source, not the build"},
			&cli.StringFlag{Name: flagMetadataDir, Required: true, Usage: "directory the staged <name>-arch-<arch> layouts are written under; keep it outside the build context"},
			&cli.StringFlag{Name: flagInputsDir, Usage: "secret-free prepared inputs root, with a <name>-arch-<arch> fetch-build-inputs directory for every build"},
			&cli.StringFlag{Name: flagImage, Usage: "build only this planned image of --arch, for a job per image; an image the plan does not build on --arch is refused"},
		), imageBuilderFlags()...),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			plan, err := readPlan(cmd)
			if err != nil {
				return err
			}

			builder, err := imageBuilderFromCmd(cmd)
			if err != nil {
				return err
			}

			arch := cmd.String(flagArch)
			builds := releaseImageBuildsOf(plan, arch)

			if name := cmd.String(flagImage); name != "" {
				builds = slices.DeleteFunc(builds, func(build pipeline.ReleaseImageBuild) bool { return build.Name != name })
				if len(builds) == 0 {
					return fmt.Errorf("the plan builds no image %q on %s: %w", name, arch, errs.ErrUsage)
				}
			}

			if root := cmd.String(flagInputsDir); root != "" && !filepath.IsLocal(root) {
				return fmt.Errorf("inputs-dir must be a safe relative directory: %w", errs.ErrUsage)
			}
			// Check the whole native share before the first build or cache import.
			for _, build := range builds {
				if err := validateReleaseImageBuilder(builder, build, cmd.String(flagInputsDir) != ""); err != nil {
					return err
				}
			}

			for _, build := range builds {
				if cmd.String(flagInputsDir) != "" {
					if err := stagePreparedReleaseImageBuild(ctx, cmd, builder, build, filepath.Join(cmd.String(flagInputsDir), build.Name+"-arch-"+build.Arch)); err != nil {
						return fmt.Errorf("release image %s (%s): %w", build.Name, build.Arch, err)
					}

					continue
				}

				if _, err := appcontainer.StageSignerImageArch(ctx, signerImageTool{builder}, ociregistry.NewAnonymous(), os.Stderr, releaseImageBuildInput(cmd, build)); err != nil {
					return fmt.Errorf("release image %s (%s): %w", build.Name, build.Arch, err)
				}
			}

			_, _ = fmt.Fprintf(os.Stderr, "release images: %d %s layout(s) staged\n", len(builds), arch)

			return nil
		},
	}
}

// releaseImageBuildsOf is the planned builds of arch, in plan order: the work
// of that architecture's build job, none when the plan builds no image of it.
func releaseImageBuildsOf(plan pipeline.ReleaseImagesPlan, arch string) []pipeline.ReleaseImageBuild {
	var builds []pipeline.ReleaseImageBuild

	for _, build := range plan.Builds {
		if build.Arch == arch {
			builds = append(builds, build)
		}
	}

	return builds
}

// releaseImageBuildInput is the build-arch --stage call the Forgejo workflow's
// build step makes for one planned build.
func releaseImageBuildInput(cmd *cli.Command, build pipeline.ReleaseImageBuild) appcontainer.SignerImageBuildArchInput {
	source := cmd.String(flagSourceDir)
	sourceURL := strings.TrimRight(cmd.String(flagServerURL), "/") + "/" + cmd.String(flagRepository)

	labels := slices.Concat([]string{
		"org.opencontainers.image.version=" + cmd.String(flagReleaseTag),
		"org.opencontainers.image.url=" + sourceURL,
		"org.opencontainers.image.documentation=" + sourceURL + "#readme",
		"org.opencontainers.image.ref.name=" + build.FinalTagName,
		"org.opencontainers.image.created=" + cmd.String(flagCreated),
	}, splitSignerLines(build.Labels))

	var cacheLabels []string
	if build.CacheTag != "" {
		cacheLabels = splitSignerLines(build.RequiredLabels)
	}

	return appcontainer.SignerImageBuildArchInput{
		Arch:              build.Arch,
		HostArch:          runtime.GOARCH,
		SourceSHA:         cmp.Or(build.Revision, cmd.String(flagReleaseSHA)),
		ServerURL:         cmd.String(flagServerURL),
		Repository:        cmd.String(flagRepository),
		ImageRepository:   build.ImageRepository,
		TagName:           build.CandidateTag,
		Name:              build.Name,
		Title:             build.Name,
		Containerfile:     filepath.Join(source, build.ContainerFile),
		Context:           filepath.Join(source, build.Context),
		Target:            build.Target,
		BuildArgs:         splitSignerLines(build.BuildArgs),
		Labels:            labels,
		CacheTag:          build.CacheTag,
		CacheLabels:       cacheLabels,
		MetadataDir:       filepath.Join(cmd.String(flagMetadataDir), build.Name+"-arch-"+build.Arch),
		RetryAttempts:     cmd.Int(flagRetryAttempts),
		BuilderMaterialID: build.BuilderMaterialID,
	}
}

func releaseImagesPublishCmd() *cli.Command {
	return &cli.Command{
		Name:  subCmdPublish,
		Usage: "scan, assemble and push every planned release image from its staged layouts, and write the release-images ledger",
		// --layout-source takes arch=PATH, and a path may hold a comma.
		DisableSliceFlagSeparator: true,
		Description: `Reads the plan file and admits --layouts-dir: it must hold exactly the
planned <name>-arch-<arch> layouts, with no other entry and no link, and is
only read. Alternatively, repeat --layout-source arch=PATH to admit each native
job's own root and read layouts directly, without a merged copy. These source
modes are exclusive. Then it verifies and scans every staged architecture layout of
every image, and only when all of them pass pushes each index under its
staging candidate tag, as multiarch-image assemble --from-layouts does, and
records it as ledger add does. The ledger
release-images.json, the Trivy results and the index manifests are written to
--output-dir, for the signer to import. Every image is scanned; the
administrator policy decides whether findings block.

EXAMPLE:
   forge-ci container release-images publish --plan plan.json --image-repository registry.example.com/group/app --release-tag v1.2.3 --release-sha "$(git rev-parse HEAD)" --layouts-dir layouts --output-dir images --auth-file auth.json`,
		Flags: append(releaseImagesJobFlags(),
			ledgerAuthFileFlag("registry auth file that may push to --image-repository"),
			&cli.StringFlag{Name: flagLayoutsDir, Usage: "directory holding the <name>-arch-<arch> layouts; exclusive with --layout-source"},
			&cli.StringSliceFlag{Name: "layout-source", Usage: "native job artifact root as amd64=PATH or arm64=PATH (repeatable); admit/read without copying"},
			&cli.StringFlag{Name: flagOutputDir, Required: true, Usage: "directory for release-images.json and the scan evidence; must not exist yet"},
			&cli.BoolFlag{Name: "prepublication-sboms", Usage: "retain release-image platform inventories and pin signer-ready index SBOM bytes"},
			&cli.StringFlag{Name: "sbom-url-prefix", Usage: "public release download directory for inventory URLs; defaults to the source forge's release downloads with --prepublication-sboms"},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			plan, err := readReleaseImagesPlanFile(cmd)
			if err != nil {
				return err
			}

			if cmd.String(flagAuthFile) == "" {
				return fmt.Errorf("release-images publish pushes images: pass --auth-file: %w", errs.ErrUsage)
			}

			sources, err := appcontainer.ParseReleaseImageLayoutSources(cmd.StringSlice("layout-source"))
			if err != nil {
				return err
			}

			if (len(sources) == 0) == (cmd.String(flagLayoutsDir) == "") {
				return fmt.Errorf("use exactly one of --layouts-dir or --layout-source: %w", errs.ErrUsage)
			}

			return deps.FromCmd(ctx, cmd, func(dep *deps.Deps) error {
				scanner := trivy.New()

				prefix := cmd.String("sbom-url-prefix")
				if cmd.Bool("prepublication-sboms") && prefix == "" {
					urls := dep.WebURLBuilder()
					if urls == nil {
						return fmt.Errorf("prepublication SBOMs require a source forge or explicit sbom-url-prefix: %w", errs.ErrUsage)
					}

					prefix = urls.ReleaseDownloadURLPrefix(cmd.String(flagServerURL), cmd.String(flagRepository), cmd.String(flagReleaseTag))
				}

				return appcontainer.PublishReleaseImages(ctx, ociregistry.WithAuthFile(cmd.String(flagAuthFile)), dep.SummarySink, os.Stderr, appcontainer.PublishReleaseImagesInput{
					Plan:                plan,
					LayoutsDir:          cmd.String(flagLayoutsDir),
					LayoutSources:       sources,
					OutputDir:           cmd.String(flagOutputDir),
					AuthFile:            cmd.String(flagAuthFile),
					ReleaseTag:          cmd.String(flagReleaseTag),
					ReleaseSHA:          cmd.String(flagReleaseSHA),
					ServerURL:           cmd.String(flagServerURL),
					Repository:          cmd.String(flagRepository),
					RetryAttempts:       cmd.Int(flagRetryAttempts),
					Trivy:               scanner,
					Syft:                syft.New(),
					PrepublicationSBOMs: cmd.Bool("prepublication-sboms"),
					SBOMURLPrefix:       prefix,
					Gate:                publicationGate(scanner, deps.Annotator(cmd)),
				})
			})
		},
	}
}

func releaseImagesJobFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: flagPlan, Required: true, Usage: "plan file release-images plan --output wrote"},
		&cli.StringFlag{Name: flagImageRepository, Required: true, Usage: "the one repository the plan publishes to; the plan is checked against it"},
		&cli.StringFlag{Name: flagReleaseTag, Required: true, Usage: "release tag the plan was made for, e.g. v1.2.3"},
		&cli.StringFlag{Name: flagReleaseSHA, Required: true, Usage: "release commit the images are built from and labelled with, unless the plan names a revision"},
		&cli.StringFlag{Name: flagServerURL, Sources: cienv.ServerURL(), Usage: "forge server URL the source labels name"},
		&cli.StringFlag{Name: flagRepository, Sources: cienv.Repository(), Usage: "owner/repo the source labels name"},
		&cli.IntFlag{Name: flagRetryAttempts, Value: 3, Usage: "registry attempts"},
	}
}

// readReleaseImagesPlanFile reads the plan file and admits it again, against
// the destination and release tag this job was given, before any build.
func readReleaseImagesPlanFile(cmd *cli.Command) (pipeline.ReleaseImagesPlan, error) {
	body, err := cliio.ReadRegularFile(cmd.String(flagPlan))
	if err != nil {
		return pipeline.ReleaseImagesPlan{}, err
	}

	plan, err := pipeline.ReadImageBuildPlan(string(body), cmd.String(flagImageRepository), cmd.String(flagReleaseTag))
	if err != nil {
		return pipeline.ReleaseImagesPlan{}, err
	}

	return plan, appcontainer.ValidateNativeReleaseImagesPlan(plan)
}

// writeReleaseImagesPlanFile writes the plan for the job verbs. Its builds
// were admitted against the layer's runner inputs when it was resolved.
func writeReleaseImagesPlanFile(path string, plan pipeline.ReleaseImagesPlan) error {
	if err := appcontainer.ValidateNativeReleaseImagesPlan(plan); err != nil {
		return err
	}

	return appsnapshot.WriteJSON(path, plan)
}
