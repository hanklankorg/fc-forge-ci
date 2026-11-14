// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	gitadapter "github.com/diggsweden/forge-ci/internal/adapters/git"
	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	"github.com/diggsweden/forge-ci/internal/adapters/syft"
	"github.com/diggsweden/forge-ci/internal/adapters/trivy"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	appsnapshot "github.com/diggsweden/forge-ci/internal/app/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/cli/commonflags"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/cli/regflags"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/urfave/cli/v3"
)

func snapshotImagesGroup() *cli.Command {
	return &cli.Command{Name: "snapshot-images", Usage: "plan, stage, publish and collect declared snapshot images", Commands: []*cli.Command{snapshotImagesPlanCmd(), snapshotImagesReadPlanCmd(), snapshotBuildArtifactCmd(), snapshotBuildFilesCmd(), snapshotPrepareInputsCmd(), snapshotGatherContextCmd(), snapshotImagesContextCmd(), snapshotImagesRestoreContextCmd(), snapshotImagesBuildCmd(), snapshotImagesPublishCmd(), snapshotImagesCollectCmd()}}
}

// Fields the read-plan transport query understands.
const (
	snapshotFieldSourceSHA     = "source-sha"
	snapshotFieldSourceBranch  = "source-branch"
	snapshotFieldArtifactNames = "artifact-names"
	snapshotFieldImageNames    = "image-names"
	snapshotFieldArchs         = "archs"
	snapshotFieldScan          = "scan"
	snapshotFieldSBOM          = "sbom"
	// snapshotFieldBuildSBOM: whether the build of the producer or file
	// output called name writes its Build SBOM.
	snapshotFieldBuildSBOM = "build-sbom"
)

var (
	errUnknownSnapshotField     = errors.New("unknown snapshot transport field")
	errSnapshotAuthExclusive    = errors.New("auth-file and registry username/password flags are mutually exclusive")
	errSnapshotAuthRequired     = errors.New("snapshot publication requires --auth-file or registry username/password-file credentials")
	errSnapshotOutputOverwrites = errors.New("snapshot output must not overwrite input")
)

// Native transport queries the existing validated contract, not a second JSON
// interpreter. Failures are command failures, never empty successful outputs.
func snapshotImagesReadPlanCmd() *cli.Command {
	return &cli.Command{Name: "read-plan", Usage: "read typed snapshot transport fields, one value per line", Flags: []cli.Flag{
		&cli.StringFlag{Name: flagPlan, Required: true}, &cli.StringFlag{Name: "field", Required: true}, &cli.StringFlag{Name: flagName},
	}, Action: func(_ context.Context, cmd *cli.Command) error {
		plan, err := appsnapshot.ReadPlan(cmd.String(flagPlan))
		if err != nil {
			return err
		}

		values, err := snapshotPlanFieldValues(plan, cmd.String("field"), cmd.String(flagName))
		if err != nil {
			return err
		}

		for _, value := range values {
			if _, err := fmt.Fprintln(os.Stdout, value); err != nil {
				return err
			}
		}

		return nil
	}}
}

// snapshotPlanFieldValues returns the values of one transport field of plan;
// per-image fields resolve the image called name, build-sbom the producer or
// file output of that name.
func snapshotPlanFieldValues(plan snapshotimage.Plan, field, name string) ([]string, error) {
	values := []string{}

	switch field {
	case snapshotFieldSourceSHA:
		values = append(values, plan.SourceSHA)
	case snapshotFieldSourceBranch:
		values = append(values, plan.SourceBranch)
	case snapshotFieldArtifactNames:
		for _, artifact := range plan.Artifacts {
			values = append(values, artifact.Name)
		}
	case snapshotFieldImageNames:
		for _, image := range plan.Images {
			values = append(values, image.Name)
		}
	case snapshotFieldArchs, snapshotFieldScan, snapshotFieldSBOM:
		image, err := plan.Image(name)
		if err != nil {
			return nil, err
		}

		return snapshotImageFieldValues(image, field), nil
	case snapshotFieldBuildSBOM:
		enabled, err := appsnapshot.BuildSBOM(plan, name)
		if err != nil {
			return nil, err
		}

		values = append(values, strconv.FormatBool(enabled))
	default:
		return nil, fmt.Errorf("%w %q", errUnknownSnapshotField, field)
	}

	return values, nil
}

// snapshotImageFieldValues returns the values of one per-image transport field.
func snapshotImageFieldValues(image snapshotimage.Image, field string) []string {
	values := []string{}

	switch field {
	case snapshotFieldArchs:
		for _, platform := range image.Platforms {
			values = append(values, strings.TrimPrefix(platform, "linux/"))
		}
	case snapshotFieldScan:
		values = append(values, strconv.FormatBool(image.Scan))
	case snapshotFieldSBOM:
		values = append(values, strconv.FormatBool(image.SBOM))
	}

	return values
}

func snapshotImagesRestoreContextCmd() *cli.Command {
	return &cli.Command{Name: "restore-context", Usage: "verify a frozen producer bundle and restore only its planned dependency files and inventory into the current checkout", Flags: []cli.Flag{&cli.StringFlag{Name: flagPlan, Required: true}, &cli.StringFlag{Name: "input", Required: true}}, Action: func(ctx context.Context, cmd *cli.Command) error {
		p, err := appsnapshot.ReadPlan(cmd.String(flagPlan))
		if err != nil {
			return err
		}

		return appsnapshot.RestoreContext(ctx, gitadapter.New(), p, cmd.String(flagPlan), cmd.String("input"))
	}}
}

func snapshotImagesContextCmd() *cli.Command {
	return &cli.Command{Name: "context", Usage: "record source-bound artifact identities, platforms and file digests after trusted artifact downloads", Flags: []cli.Flag{&cli.StringFlag{Name: flagPlan, Required: true}, &cli.StringFlag{Name: flagOutput, Value: snapshotimage.DefaultDependenciesFile}}, Action: func(ctx context.Context, cmd *cli.Command) error {
		if err := snapshotOutputDistinct(cmd.String(flagOutput), cmd.String(flagPlan)); err != nil {
			return err
		}

		p, err := appsnapshot.ReadPlan(cmd.String(flagPlan))
		if err != nil {
			return err
		}

		return appsnapshot.RecordContext(ctx, gitadapter.New(), p, cmd.String(flagOutput))
	}}
}

func snapshotImagesPlanCmd() *cli.Command {
	return &cli.Command{Name: subCmdPlan, Usage: "pin source and plan declared native snapshot images; no implicit root image", Flags: []cli.Flag{
		&cli.StringFlag{Name: flagFile, Value: appsnapshot.DefaultConfigFile},
		&cli.StringFlag{Name: "branch", Required: true, Usage: "logical source branch (also for detached/manual checkouts)"},
		&cli.StringFlag{Name: "source-sha", Required: true},
		&cli.StringFlag{Name: flagImageRepository, Usage: "full base OCI repository, required when the plan publishes an image; configured image-name overrides its repository using the existing name rules"},
		&cli.StringFlag{Name: flagOutput, Required: true},
		&cli.BoolFlag{Name: flagPublish, Value: true, Usage: "enable declared images (false plans no image)"},
		&cli.BoolFlag{Name: "publish-files", Usage: "keep the built files of every declared artifact its ecosystem builds outside an image as unsigned file outputs"},
		commonflags.RequiredRunnerInput(),
		// A --runner-input value is a JSON array, whose commas separate nothing.
	}, DisableSliceFlagSeparator: true, Action: func(ctx context.Context, cmd *cli.Command) error {
		if err := snapshotOutputDistinct(cmd.String(flagOutput), cmd.String(flagFile)); err != nil {
			return err
		}

		snapshotPlan, err := appsnapshot.Plan(ctx, gitadapter.New(), appsnapshot.PlanInput{File: cmd.String(flagFile), Branch: cmd.String("branch"), SourceSHA: cmd.String("source-sha"), ImageRepository: cmd.String(flagImageRepository), Publish: cmd.Bool(flagPublish), PublishFiles: cmd.Bool("publish-files")})
		if err != nil {
			return err
		}

		return deps.FromCmd(ctx, cmd, func(dep *deps.Deps) error {
			// A planned build with no runner is refused before the plan is
			// written, so no job starts on it.
			runnerInputs, err := commonflags.RequiredRunnerInputs(cmd, dep.Platform, "a snapshot image plan")
			if err != nil {
				return err
			}

			if err := runnerInputs.Admit(snapshotPlan.RunnerNeeds()); err != nil {
				return err
			}

			if err := appsnapshot.WriteJSON(cmd.String(flagOutput), snapshotPlan); err != nil {
				return err
			}

			return appsnapshot.EmitPlan(ctx, dep.OutputSink, snapshotPlan)
		})
	}}
}

func snapshotPlanFlags(named bool) []cli.Flag {
	flags := []cli.Flag{&cli.StringFlag{Name: flagPlan, Required: true}, &cli.StringFlag{Name: flagMetadataDir, Required: true, Usage: "architecture-artifact root (<name>-arch-<arch>/layout and metadata)"}}
	if named {
		flags = append(flags, &cli.StringFlag{Name: flagName, Required: true})
	}

	return flags
}

func snapshotImagesBuildCmd() *cli.Command {
	return &cli.Command{Name: "build", Usage: "stage one planned architecture natively, after dependency validation; no uploads", Flags: append(append(append(snapshotPlanFlags(true), signerImageSourceFlags()...), imageBuilderFlags()...), &cli.StringFlag{Name: "arch", Required: true}, &cli.StringFlag{Name: "dependencies-file", Value: snapshotimage.DefaultDependenciesFile}), Action: func(ctx context.Context, cmd *cli.Command) error {
		p, err := appsnapshot.ReadPlan(cmd.String(flagPlan))
		if err != nil {
			return err
		}

		builder, err := imageBuilderFromCmd(cmd)
		if err != nil {
			return err
		}

		return appsnapshot.Build(ctx, gitadapter.New(), signerImageTool{builder}, ociregistry.NewAnonymous(), os.Stderr, deps.Annotator(cmd), p, appsnapshot.BuildInput{Name: cmd.String(flagName), Arch: cmd.String("arch"), HostArch: runtime.GOARCH, MetadataDir: cmd.String(flagMetadataDir), AuthFile: cmd.String(flagAuthFile), ServerURL: cmd.String(flagServerURL), Repository: cmd.String(flagRepository), DependenciesFile: cmd.String("dependencies-file")})
	}}
}

func snapshotImagesPublishCmd() *cli.Command {
	return &cli.Command{Name: subCmdPublish, Usage: "verify and scan the complete planned platform set before upload; write one bound receipt", Flags: append(append(snapshotPlanFlags(true), signerImageSourceFlags()...), regflags.Username(), regflags.PasswordFile(), &cli.StringFlag{Name: flagOutput, Required: true}, &cli.StringFlag{Name: "evidence-dir", Value: "snapshot-evidence", Usage: "retained scan and CycloneDX reports"}), Action: func(ctx context.Context, cmd *cli.Command) error {
		if err := snapshotOutputDistinct(cmd.String(flagOutput), cmd.String(flagPlan), cmd.String(flagAuthFile), cmd.String(regflags.FlagPasswordFile)); err != nil {
			return err
		}

		p, err := appsnapshot.ReadPlan(cmd.String(flagPlan))
		if err != nil {
			return err
		}

		im, err := p.Image(cmd.String(flagName))
		if err != nil {
			return err
		}

		return snapshotImageAuth(cmd, im, func(authFile string) error {
			return deps.FromCmd(ctx, cmd, func(dep *deps.Deps) error {
				r, err := appsnapshot.Publish(ctx, ociregistry.WithAuthFile(authFile), trivy.New(), syft.New(), dep.OutputSink, dep.SummarySink, os.Stderr, deps.Annotator(cmd), p, appsnapshot.PublishInput{Name: im.Name, MetadataDir: cmd.String(flagMetadataDir), AuthFile: authFile, ServerURL: cmd.String(flagServerURL), Repository: cmd.String(flagRepository), EvidenceDir: cmd.String("evidence-dir")})
				if err != nil {
					return err
				}

				return appsnapshot.WriteJSON(cmd.String(flagOutput), r)
			})
		})
	}}
}

// Use the shared registry conventions, without logging credentials or importing
// production signing material. Explicit auth files are never rewritten.
func snapshotImageAuth(cmd *cli.Command, im snapshotimage.Image, run func(string) error) error {
	if auth := cmd.String(flagAuthFile); auth != "" {
		if cmd.String(regflags.FlagPasswordFile) != "" || cmd.String(regflags.FlagUsername) != "" {
			return errSnapshotAuthExclusive
		}

		return run(auth)
	}

	return withReleaseImagesDockerConfig(func(authFile string) error {
		registry, err := appcontainer.OptionalRegistryHost(im.ImageRepository)
		if err != nil {
			return err
		}

		password, err := regflags.FileOrEnv(cmd.String(regflags.FlagPasswordFile), releaseImagesDefaultEnvVar, "REGISTRY_PASSWORD")
		if err != nil {
			return err
		}

		user, password, err := registryLoginCredentials(registry, strings.TrimSpace(cmd.String(regflags.FlagUsername)), password)
		if err != nil {
			return err
		}

		if user == "" || password == "" {
			return errSnapshotAuthRequired
		}

		if err = appcontainer.RegistryLogin(os.Stderr, appcontainer.RegistryLoginInput{Registry: registry, Username: user, Password: password, AuthFile: authFile}); err != nil {
			return err
		}

		return run(authFile)
	})
}

func snapshotImagesCollectCmd() *cli.Command {
	return &cli.Command{Name: "collect", Usage: "require exactly every planned image receipt and emit immutable digest references", Flags: []cli.Flag{&cli.StringFlag{Name: flagPlan, Required: true}, &cli.StringFlag{Name: "receipts-dir", Required: true}, &cli.StringFlag{Name: flagOutput, Required: true}}, Action: func(ctx context.Context, cmd *cli.Command) error {
		if err := snapshotOutputDistinct(cmd.String(flagOutput), cmd.String(flagPlan)); err != nil {
			return err
		}

		p, err := appsnapshot.ReadPlan(cmd.String(flagPlan))
		if err != nil {
			return err
		}

		return deps.FromCmd(ctx, cmd, func(dep *deps.Deps) error {
			return appsnapshot.Collect(ctx, dep.OutputSink, p, cmd.String("receipts-dir"), cmd.String(flagOutput))
		})
	}}
}

func snapshotOutputDistinct(output string, inputs ...string) error {
	out, err := filepath.Abs(output)
	if err != nil {
		return err
	}

	for _, input := range inputs {
		if input == "" {
			continue
		}

		in, err := filepath.Abs(input)
		if err != nil {
			return err
		}

		if out == in {
			return fmt.Errorf("%w %s", errSnapshotOutputOverwrites, input)
		}
	}

	return nil
}
