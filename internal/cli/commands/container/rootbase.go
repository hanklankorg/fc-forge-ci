// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	"github.com/diggsweden/forge-ci/internal/adapters/syft"
	"github.com/diggsweden/forge-ci/internal/adapters/trivy"
	appbaseimages "github.com/diggsweden/forge-ci/internal/app/baseimages"
	appsnapshot "github.com/diggsweden/forge-ci/internal/app/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/cli/commonflags"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
)

// This is not a general BASE plan switch: every verb independently admits one
// cold root and the ordinary release-images boundary continues to refuse BASE.
func rootBaseGroup() *cli.Command {
	build := nativeImagesBuildCmd(readRootBasePlanFile)
	build.Usage = "build one admitted root base's native architecture without registry writes"
	build.Description = "Builds a plan admitted by root-base plan using the existing native builder. Parents, mixed/multiple images and warm caches are unsupported."

	plan, publish := rootBasePlanCmd(), rootBasePublishCmd()
	for _, command := range []*cli.Command{plan, build, publish} {
		command.Flags = append(command.Flags, stageFlavorsFlag())
	}

	return &cli.Command{
		Name:     "root-base",
		Usage:    "cold single-root native BASE boundary; no parent, mixed or warm graph support",
		Commands: []*cli.Command{plan, build, publish},
	}
}

const flagStageFlavors = "stage-flavors"

// stageFlavorsFlag names the flavors a caller's policy gives the stage. The
// plan comes from the project's own planner, so a caller that names the stage
// itself holds every verb's plan to exactly those flavors.
func stageFlavorsFlag() cli.Flag {
	return &cli.StringFlag{Name: flagStageFlavors, Usage: "the stage's flavors, separated by commas; a plan with another set of flavors is refused"}
}

// requireStageFlavors refuses a plan whose flavors are not exactly the ones
// the caller named, when it named any.
func requireStageFlavors(plan pipeline.ReleaseImagesPlan, named string) error {
	if named == "" {
		return nil
	}

	want := strings.Split(named, ",")
	slices.Sort(want)

	got := make([]string, 0, len(plan.Images))
	for _, image := range plan.Images {
		got = append(got, image.Flavor)
	}

	slices.Sort(got)

	if !slices.Equal(got, want) {
		return fmt.Errorf("the plan's flavors are %q and the stage's are %q; a stage's plan holds exactly its flavors: %w", got, want, errs.ErrInvalidConfig)
	}

	return nil
}

func readRootBasePlanFile(cmd *cli.Command) (pipeline.ReleaseImagesPlan, error) {
	return readBaseStagePlanFile(cmd, appbaseimages.ValidateRootBasePlan)
}

func readBaseStagePlanFile(cmd *cli.Command, validate func(pipeline.ReleaseImagesPlan, string) error) (pipeline.ReleaseImagesPlan, error) {
	body, err := cliio.ReadRegularFile(cmd.String(flagPlan))
	if err != nil {
		return pipeline.ReleaseImagesPlan{}, err
	}

	plan, err := pipeline.ReadImageBuildPlan(string(body), cmd.String(flagImageRepository), cmd.String(flagReleaseTag))
	if err != nil {
		return pipeline.ReleaseImagesPlan{}, err
	}

	if err := validate(plan, cmd.String(flagImageRepository)); err != nil {
		return plan, err
	}

	return plan, requireStageFlavors(plan, cmd.String(flagStageFlavors))
}

func rootBasePlanCmd() *cli.Command {
	return nativeBasePlanCmd(readRootBasePlanFile)
}

func nativeBasePlanCmd(readPlan func(*cli.Command) (pipeline.ReleaseImagesPlan, error)) *cli.Command {
	runnerInput := commonflags.RunnerInput()
	runnerInput.Usage = "runner input of the calling layer as it passed it, name=value, such as build-runs-on-linux-arm64=[\"arm64\"] (repeatable, at least one); " +
		"a planned build whose image build runner input has no value is refused"

	return &cli.Command{
		Name:  subCmdPlan,
		Usage: "admit a resolved consumer base stage plan, freeze it for native jobs and emit its builds and layouts outputs",
		Description: `The layer passes its runner inputs with --runner-input, as release-images
plan takes them; a build of a platform whose image build runner input has no
value is refused here, before the plan is written. A base stage is never
planned unchecked: without any --runner-input the plan is refused.`,
		// A --runner-input value is a JSON array, whose commas separate nothing.
		DisableSliceFlagSeparator: true,
		Flags: append(releaseImagesJobFlags(),
			&cli.StringFlag{Name: flagOutput, Required: true, Usage: "admitted plan output file"},
			runnerInput,
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			plan, err := readPlan(cmd)
			if err != nil {
				return err
			}

			// The builds output is the matrix a layer schedules its native
			// build jobs from, as release-images plan emits it. layouts names
			// each build's layout as <name>-<arch>, on one line with a space
			// between them, for the job that collects them. A name is held
			// to the flavor rule and has no space. One line is what every
			// forge's output takes: a GitLab dotenv report has no value of
			// several lines.
			builds, err := json.Marshal(plan.Builds)
			if err != nil {
				return err
			}

			var layouts []string

			for _, image := range plan.Images {
				for arch := range strings.FieldsSeq(image.Archs) {
					layouts = append(layouts, image.Name+"-"+arch)
				}
			}

			return deps.FromCmd(ctx, cmd, func(dep *deps.Deps) error {
				runnerInputs, err := commonflags.RunnerInputs(cmd, dep.Platform)
				if err != nil {
					return err
				}

				if runnerInputs == nil {
					return fmt.Errorf("a base stage plan needs the layer's runner inputs: pass --%s for each: %w", commonflags.FlagRunnerInput, errs.ErrUsage)
				}

				if err := runnerInputs.Admit(plan.RunnerNeeds()); err != nil {
					return err
				}

				if err := appsnapshot.WriteJSON(cmd.String(flagOutput), plan); err != nil {
					return err
				}

				if err := dep.OutputSink.Set(ctx, "builds", string(builds)); err != nil {
					return err
				}

				return dep.OutputSink.Set(ctx, "layouts", strings.Join(layouts, " "))
			})
		},
	}
}

func rootBasePublishCmd() *cli.Command {
	return &cli.Command{
		Name:        subCmdPublish,
		Usage:       "scan/gate all root layouts, publish unsigned candidates and write a canonical payload",
		Description: "Output is a new workspace containing dist/base-images.json and premade-sboms/. Transport and verify the entire workspace with release dist-digest/validate-dist --canonical-payload before trusted base-images import/sign/promote. No final tags or signatures are produced here.",
		Flags:       baseStagePublishFlags(),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			plan, err := readRootBasePlanFile(cmd)
			if err != nil {
				return err
			}

			input, err := baseStagePublicationInput(cmd, plan)
			if err != nil {
				return err
			}

			return appbaseimages.PublishRootBase(ctx, ociregistry.WithAuthFile(cmd.String(flagAuthFile)), os.Stderr, input)
		},
	}
}

func baseStagePublishFlags() []cli.Flag {
	return append(releaseImagesJobFlags(),
		ledgerAuthFileFlag("staging registry auth file; never signing/publication credentials"),
		&cli.StringFlag{Name: flagLayoutsDir, Required: true, Usage: "complete admitted native layout set"},
		&cli.StringFlag{Name: flagOutputDir, Required: true, Usage: "new canonical payload directory with an existing parent"},
	)
}

func baseStagePublicationInput(cmd *cli.Command, plan pipeline.ReleaseImagesPlan) (appbaseimages.PublishRootBaseInput, error) {
	if cmd.String(flagAuthFile) == "" {
		return appbaseimages.PublishRootBaseInput{}, fmt.Errorf("base stage publication requires --auth-file: %w", errs.ErrUsage)
	}

	scanner := trivy.New()

	return appbaseimages.PublishRootBaseInput{
		Plan: plan, ExpectedRepository: cmd.String(flagImageRepository),
		LayoutsDir: cmd.String(flagLayoutsDir), OutputDir: cmd.String(flagOutputDir), AuthFile: cmd.String(flagAuthFile),
		SourceSHA: cmd.String(flagReleaseSHA), ServerURL: cmd.String(flagServerURL), Repository: cmd.String(flagRepository),
		RetryAttempts: cmd.Int(flagRetryAttempts), Trivy: scanner, Syft: syft.New(), Gate: publicationGate(scanner, deps.Annotator(cmd)),
	}, nil
}
