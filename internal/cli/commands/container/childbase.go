// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	appbaseimages "github.com/diggsweden/forge-ci/internal/app/baseimages"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/cli/regflags"
	"github.com/diggsweden/forge-ci/internal/docslink"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
)

const flagParentBaseJSON = "parent-base-json"

func childBaseGroup() *cli.Command {
	plan := nativeBasePlanCmd(readChildBasePlanFile)
	plan.Usage = "admit one cold child BASE, or several siblings, against one explicit caller-verified parent"
	build := nativeImagesBuildCmd(readChildBasePlanFile)
	build.Usage = "build the stage's child BASE images of one architecture using secret-free prepared parent inputs"
	build.Description = "Requires --inputs-dir from the existing release-images fetch-build-inputs boundary. No registry fetch credentials or parent authentication run in the builder."

	// The plan takes the builder only to refuse kaniko before a parent is
	// fetched; the build already selects its builder with the same flag.
	plan.Flags = append(plan.Flags, &cli.StringFlag{Name: flagImageBuilder, Sources: cli.EnvVars(envImageBuilder), Usage: "image builder the build jobs will use; kaniko is refused here, before any parent is fetched"})

	for _, command := range []*cli.Command{plan, build} {
		command.Flags = append(command.Flags, parentBaseFlag())
	}

	fetch, publish := childBaseFetchCmd(), childBasePublishCmd()
	for _, command := range []*cli.Command{plan, fetch, build, publish} {
		command.Flags = append(command.Flags, stageFlavorsFlag())
	}

	return &cli.Command{
		Name:        "child-base",
		Usage:       "one cold child BASE stage: one image, or siblings on one parent; no graph scheduling, warm/cache or expected-parent promotion",
		Description: "The protected caller must independently verify the parent with Cosign, its trust key, source/workflow and input ID before supplying parent-base-json. The producer only binds that assertion to its registry input and unsigned lineage; it never authenticates the parent or traverses a graph.",
		Commands:    []*cli.Command{plan, fetch, build, publish},
	}
}

func parentBaseFlag() cli.Flag {
	return &cli.StringFlag{Name: flagParentBaseJSON, Required: true, Usage: "explicit caller-verified immediate parent as {ref: digest-pinned-ref, input_id: opaque-sha256-ID}; not producer authentication"}
}

func childBaseParent(cmd *cli.Command) (appbaseimages.BaseImageParent, error) {
	var parent appbaseimages.BaseImageParent

	decoder := json.NewDecoder(strings.NewReader(cmd.String(flagParentBaseJSON)))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&parent); err != nil {
		return parent, fmt.Errorf("invalid explicit parent projection: %w: %w", err, errs.ErrInvalidConfig)
	}

	if err := decoder.Decode(new(any)); err != io.EOF {
		return parent, fmt.Errorf("explicit parent projection must contain one object: %w", errs.ErrInvalidConfig)
	}

	return parent, nil
}

func readChildBasePlanFile(cmd *cli.Command) (pipeline.ReleaseImagesPlan, error) {
	// The build job holds no registry credential: the verified parent layout
	// is imported into Buildah's local store, and kaniko has none.
	if strings.EqualFold(strings.TrimSpace(cmd.String(flagImageBuilder)), imageBuilderKaniko) {
		return pipeline.ReleaseImagesPlan{}, fmt.Errorf("a child of an authenticated parent builds with Buildah, not kaniko: the build job holds no registry credential and kaniko has no local store to import the verified parent into. Select buildah (CI_IMAGE_BUILDER) for this repository and build on a runner that permits it; an organisation whose own runners are kaniko-only can borrow one (examples/runner-lender, %s): %w", docslink.URL("runners.md"), errs.ErrUsage)
	}

	parent, err := childBaseParent(cmd)
	if err != nil {
		return pipeline.ReleaseImagesPlan{}, err
	}

	return readBaseStagePlanFile(cmd, func(plan pipeline.ReleaseImagesPlan, repository string) error {
		return appbaseimages.ValidateChildBasePlan(plan, repository, parent)
	})
}

// childBaseFetchCmd fetches the parent of every build of the stage in one
// call, for a layer that runs one credentialed job a stage instead of one a
// build: release-images fetch-build-inputs once per planned build.
func childBaseFetchCmd() *cli.Command {
	return &cli.Command{
		Name:  "fetch",
		Usage: "fetch the pinned parent of every build of the stage, without running consumer code",
		Description: `Admits the plan against --parent-base-json, then fetches each build's
registry inputs into <inputs-dir>/<name>-arch-<arch>, the directory child-base
build --inputs-dir reads. --inputs-dir must not hold a build's directory yet.`,
		Flags: append(releaseImagesJobFlags(), parentBaseFlag(),
			regflags.AuthFile(regflags.AuthFileOpts{Usage: "required explicit registry auth file that may read the parent"}),
			&cli.StringFlag{Name: flagInputsDir, Required: true, Usage: "relative run-scoped directory the builds' inputs are fetched under"}),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.String(flagAuthFile) == "" || !filepath.IsLocal(cmd.String(flagInputsDir)) {
				return fmt.Errorf("the parent fetch requires --auth-file and a safe relative --inputs-dir: %w", errs.ErrUsage)
			}

			plan, err := readChildBasePlanFile(cmd)
			if err != nil {
				return err
			}

			for _, build := range plan.Builds {
				input := releaseImageBuildInput(cmd, build)
				input.AuthFile = cmd.String(flagAuthFile)

				directory := filepath.Join(cmd.String(flagInputsDir), build.Name+"-arch-"+build.Arch)
				if err := appcontainer.FetchReleaseBuildInputs(ctx, ociregistry.WithAuthFile(input.AuthFile), os.Stderr, input, build.RegistryInputs, directory); err != nil {
					return err
				}
			}

			return nil
		},
	}
}

func childBasePublishCmd() *cli.Command {
	return &cli.Command{
		Name:  subCmdPublish,
		Usage: "scan/gate every child layout of the stage and publish unsigned canonical evidence retaining parent lineage",
		Flags: append(baseStagePublishFlags(), parentBaseFlag()),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			plan, err := readChildBasePlanFile(cmd)
			if err != nil {
				return err
			}

			parent, err := childBaseParent(cmd)
			if err != nil {
				return err
			}

			input, err := baseStagePublicationInput(cmd, plan)
			if err != nil {
				return err
			}

			return appbaseimages.PublishChildBase(ctx, ociregistry.WithAuthFile(cmd.String(flagAuthFile)), os.Stderr,
				appbaseimages.PublishChildBaseInput{PublishRootBaseInput: input, ParentBase: parent})
		},
	}
}
