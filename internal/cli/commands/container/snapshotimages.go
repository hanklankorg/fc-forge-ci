// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	gitadapter "github.com/diggsweden/forge-ci/internal/adapters/git"
	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	"github.com/diggsweden/forge-ci/internal/adapters/trivy"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	appsnapshot "github.com/diggsweden/forge-ci/internal/app/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/cli/regflags"
	"github.com/diggsweden/forge-ci/internal/cli/secretenv"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/urfave/cli/v3"
)

func snapshotImagesGroup() *cli.Command {
	return &cli.Command{Name: "snapshot-images", Usage: "plan, stage, publish and collect declared snapshot images", Commands: []*cli.Command{snapshotImagesPlanCmd(), snapshotImagesReadPlanCmd(), snapshotBuildArtifactCmd(), snapshotPrepareInputsCmd(), snapshotGatherContextCmd(), snapshotImagesContextCmd(), snapshotImagesRestoreContextCmd(), snapshotImagesBuildCmd(), snapshotImagesPublishCmd(), snapshotImagesCollectCmd()}}
}

// Native transport queries the existing validated contract, not a second JSON
// interpreter. Failures are command failures, never empty successful outputs.
func snapshotImagesReadPlanCmd() *cli.Command {
	return &cli.Command{Name: "read-plan", Usage: "read typed snapshot transport fields, one value per line", Flags: []cli.Flag{
		&cli.StringFlag{Name: "plan", Required: true}, &cli.StringFlag{Name: "field", Required: true}, &cli.StringFlag{Name: "name"},
	}, Action: func(ctx context.Context, cmd *cli.Command) error {
		p, err := appsnapshot.ReadPlan(cmd.String("plan"))
		if err != nil {
			return err
		}
		values := []string{}
		switch cmd.String("field") {
		case "source-sha":
			values = append(values, p.SourceSHA)
		case "source-branch":
			values = append(values, p.SourceBranch)
		case "artifact-names":
			for _, artifact := range p.Artifacts {
				values = append(values, artifact.Name)
			}
		case "image-names":
			for _, image := range p.Images {
				values = append(values, image.Name)
			}
		case "archs", "scan", "sbom":
			image, err := p.Image(cmd.String("name"))
			if err != nil {
				return err
			}
			switch cmd.String("field") {
			case "archs":
				for _, platform := range image.Platforms {
					values = append(values, strings.TrimPrefix(platform, "linux/"))
				}
			case "scan":
				values = append(values, fmt.Sprint(image.Scan))
			case "sbom":
				values = append(values, fmt.Sprint(image.SBOM))
			}
		default:
			return fmt.Errorf("unknown snapshot transport field %q", cmd.String("field"))
		}
		for _, value := range values {
			if _, err := fmt.Fprintln(os.Stdout, value); err != nil {
				return err
			}
		}
		return nil
	}}
}

func snapshotImagesRestoreContextCmd() *cli.Command {
	return &cli.Command{Name: "restore-context", Usage: "verify a frozen producer bundle and restore only its planned dependency files and inventory into the current checkout", Flags: []cli.Flag{&cli.StringFlag{Name: "plan", Required: true}, &cli.StringFlag{Name: "input", Required: true}}, Action: func(ctx context.Context, cmd *cli.Command) error {
		p, err := appsnapshot.ReadPlan(cmd.String("plan"))
		if err != nil {
			return err
		}
		return appsnapshot.RestoreContext(ctx, gitadapter.New(), p, cmd.String("plan"), cmd.String("input"))
	}}
}

func snapshotImagesContextCmd() *cli.Command {
	return &cli.Command{Name: "context", Usage: "record source-bound artifact identities, platforms and file digests after trusted artifact downloads", Flags: []cli.Flag{&cli.StringFlag{Name: "plan", Required: true}, &cli.StringFlag{Name: "output", Value: snapshotimage.DefaultDependenciesFile}}, Action: func(ctx context.Context, cmd *cli.Command) error {
		if err := snapshotOutputDistinct(cmd.String("output"), cmd.String("plan")); err != nil {
			return err
		}
		p, err := appsnapshot.ReadPlan(cmd.String("plan"))
		if err != nil {
			return err
		}
		return appsnapshot.RecordContext(ctx, gitadapter.New(), p, cmd.String("output"))
	}}
}

func snapshotImagesPlanCmd() *cli.Command {
	return &cli.Command{Name: "plan", Usage: "pin source and plan declared native snapshot images; no implicit root image", Flags: []cli.Flag{
		&cli.StringFlag{Name: "file", Value: appsnapshot.DefaultConfigFile},
		&cli.StringFlag{Name: "branch", Required: true, Usage: "logical source branch (also for detached/manual checkouts)"},
		&cli.StringFlag{Name: "source-sha", Required: true},
		&cli.StringFlag{Name: "image-repository", Required: true, Usage: "full base OCI repository; configured image-name overrides its repository using the existing name rules"},
		&cli.StringFlag{Name: "output", Required: true},
		&cli.BoolFlag{Name: "publish", Value: true, Usage: "enable declared images (false emits an empty disabled plan)"},
	}, Action: func(ctx context.Context, cmd *cli.Command) error {
		if err := snapshotOutputDistinct(cmd.String("output"), cmd.String("file")); err != nil {
			return err
		}
		p, err := appsnapshot.Plan(ctx, gitadapter.New(), appsnapshot.PlanInput{File: cmd.String("file"), Branch: cmd.String("branch"), SourceSHA: cmd.String("source-sha"), ImageRepository: cmd.String("image-repository"), Publish: cmd.Bool("publish")})
		if err != nil {
			return err
		}
		if err = appsnapshot.WriteJSON(cmd.String("output"), p); err != nil {
			return err
		}
		return deps.FromCmd(ctx, cmd, func(dep *deps.Deps) error { return appsnapshot.EmitPlan(ctx, dep.OutputSink, p) })
	}}
}

func snapshotPlanFlags(named bool) []cli.Flag {
	flags := []cli.Flag{&cli.StringFlag{Name: "plan", Required: true}, &cli.StringFlag{Name: "metadata-dir", Required: true, Usage: "architecture-artifact root (<name>-arch-<arch>/layout and metadata)"}}
	if named {
		flags = append(flags, &cli.StringFlag{Name: "name", Required: true})
	}
	return flags
}

func snapshotImagesBuildCmd() *cli.Command {
	return &cli.Command{Name: "build", Usage: "stage one planned architecture natively, after dependency validation; no uploads", Flags: append(append(append(snapshotPlanFlags(true), signerImageSourceFlags()...), imageBuilderFlags()...), &cli.StringFlag{Name: "arch", Required: true}, &cli.StringFlag{Name: "dependencies-file", Value: snapshotimage.DefaultDependenciesFile}), Action: func(ctx context.Context, cmd *cli.Command) error {
		p, err := appsnapshot.ReadPlan(cmd.String("plan"))
		if err != nil {
			return err
		}
		builder, err := imageBuilderFromCmd(cmd)
		if err != nil {
			return err
		}
		return appsnapshot.Build(ctx, gitadapter.New(), signerImageTool{builder}, ociregistry.NewAnonymous(), os.Stderr, deps.Annotator(cmd), p, appsnapshot.BuildInput{Name: cmd.String("name"), Arch: cmd.String("arch"), HostArch: runtime.GOARCH, MetadataDir: cmd.String("metadata-dir"), AuthFile: cmd.String(flagAuthFile), ServerURL: cmd.String(flagServerURL), Repository: cmd.String(flagRepository), DependenciesFile: cmd.String("dependencies-file")})
	}}
}

func snapshotImagesPublishCmd() *cli.Command {
	return &cli.Command{Name: "publish", Usage: "verify and scan the complete planned platform set before upload; write one bound receipt", Flags: append(append(snapshotPlanFlags(true), signerImageSourceFlags()...), regflags.Username(), regflags.PasswordFile(), &cli.StringFlag{Name: "output", Required: true}, &cli.StringFlag{Name: "evidence-dir", Value: "snapshot-evidence", Usage: "retained scan and CycloneDX reports"}), Action: func(ctx context.Context, cmd *cli.Command) error {
		if err := snapshotOutputDistinct(cmd.String("output"), cmd.String("plan"), cmd.String(flagAuthFile), cmd.String(regflags.FlagPasswordFile)); err != nil {
			return err
		}
		p, err := appsnapshot.ReadPlan(cmd.String("plan"))
		if err != nil {
			return err
		}
		im, err := p.Image(cmd.String("name"))
		if err != nil {
			return err
		}
		return snapshotImageAuth(cmd, im, func(authFile string) error {
			return deps.FromCmd(ctx, cmd, func(dep *deps.Deps) error {
				r, err := appsnapshot.Publish(ctx, ociregistry.WithAuthFile(authFile), trivy.New(secretenv.Names()...), signerSyft(""), dep.OutputSink, dep.SummarySink, os.Stderr, deps.Annotator(cmd), p, appsnapshot.PublishInput{Name: im.Name, MetadataDir: cmd.String("metadata-dir"), AuthFile: authFile, ServerURL: cmd.String(flagServerURL), Repository: cmd.String(flagRepository), EvidenceDir: cmd.String("evidence-dir")})
				if err != nil {
					return err
				}
				return appsnapshot.WriteJSON(cmd.String("output"), r)
			})
		})
	}}
}

// Use the shared registry conventions, without logging credentials or importing
// production signing material. Explicit auth files are never rewritten.
func snapshotImageAuth(cmd *cli.Command, im snapshotimage.Image, run func(string) error) error {
	if auth := cmd.String(flagAuthFile); auth != "" {
		if cmd.String(regflags.FlagPasswordFile) != "" || cmd.String(regflags.FlagUsername) != "" {
			return fmt.Errorf("auth-file and registry username/password flags are mutually exclusive")
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
			return fmt.Errorf("snapshot publication requires --auth-file or registry username/password-file credentials")
		}
		if err = appcontainer.RegistryLogin(os.Stderr, appcontainer.RegistryLoginInput{Registry: registry, Username: user, Password: password, AuthFile: authFile}); err != nil {
			return err
		}
		return run(authFile)
	})
}

func snapshotImagesCollectCmd() *cli.Command {
	return &cli.Command{Name: "collect", Usage: "require exactly every planned image receipt and emit immutable digest references", Flags: []cli.Flag{&cli.StringFlag{Name: "plan", Required: true}, &cli.StringFlag{Name: "receipts-dir", Required: true}, &cli.StringFlag{Name: "output", Required: true}}, Action: func(ctx context.Context, cmd *cli.Command) error {
		if err := snapshotOutputDistinct(cmd.String("output"), cmd.String("plan")); err != nil {
			return err
		}
		p, err := appsnapshot.ReadPlan(cmd.String("plan"))
		if err != nil {
			return err
		}
		return deps.FromCmd(ctx, cmd, func(dep *deps.Deps) error {
			return appsnapshot.Collect(ctx, dep.OutputSink, p, cmd.String("receipts-dir"), cmd.String("output"))
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
			return fmt.Errorf("snapshot output must not overwrite input %s", input)
		}
	}
	return nil
}
