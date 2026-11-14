// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"os"

	gitadapter "github.com/diggsweden/forge-ci/internal/adapters/git"
	appenforcement "github.com/diggsweden/forge-ci/internal/app/enforcement"
	appsnapshot "github.com/diggsweden/forge-ci/internal/app/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/safeexec"
	"github.com/urfave/cli/v3"
)

type snapshotSelfBuild struct{}

func (snapshotSelfBuild) Run(ctx context.Context, in appsnapshot.ArtifactBuildRequest) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}

	cmd := safeexec.Command(ctx, executable, selfBuildArgs(ctx, in)...)
	cmd.Dir = in.Dir
	cmd.Env = in.Env
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	return safeexec.WrapError(cmd.Run(), "forge-ci", "artifact build")
}

// selfBuildArgs gives the child build this invocation's administrator
// settings, not the embedded defaults: the settings are flags, which a child
// does not inherit.
func selfBuildArgs(ctx context.Context, in appsnapshot.ArtifactBuildRequest) []string {
	return append(appenforcement.AdminArgs(ctx), in.Args...)
}

func snapshotBuildArtifactCmd() *cli.Command {
	return &cli.Command{Name: "build-artifact", Usage: "build one frozen artifact with the existing ecosystem CLI, or prepare its already-built CI upload", Flags: []cli.Flag{&cli.StringFlag{Name: flagPlan, Required: true}, &cli.StringFlag{Name: flagName, Required: true}, &cli.StringFlag{Name: flagOutputDir, Value: snapshotimage.DefaultProducerDir}, &cli.StringFlag{Name: "from-artifact", Usage: "exact downloaded artifact directory; skips the ecosystem build"}}, Action: func(ctx context.Context, cmd *cli.Command) error {
		p, err := appsnapshot.ReadPlan(cmd.String(flagPlan))
		if err != nil {
			return err
		}

		return appsnapshot.BuildArtifact(ctx, gitadapter.New(), snapshotSelfBuild{}, p, appsnapshot.BuildArtifactInput{Name: cmd.String(flagName), OutputDir: cmd.String(flagOutputDir), FromArtifact: cmd.String("from-artifact"), Environment: os.Environ()})
	}}
}

func snapshotBuildFilesCmd() *cli.Command {
	return &cli.Command{Name: "build-files", Usage: "build one planned file output with the existing ecosystem CLI and write its unsigned bundle, the built files and their receipt; no uploads", Flags: []cli.Flag{&cli.StringFlag{Name: flagPlan, Required: true}, &cli.StringFlag{Name: flagName, Required: true}, &cli.StringFlag{Name: flagOutputDir, Value: snapshotimage.DefaultFilesDir}}, Action: func(ctx context.Context, cmd *cli.Command) error {
		p, err := appsnapshot.ReadPlan(cmd.String(flagPlan))
		if err != nil {
			return err
		}

		return appsnapshot.BuildFiles(ctx, gitadapter.New(), snapshotSelfBuild{}, p, appsnapshot.BuildFilesInput{Name: cmd.String(flagName), OutputDir: cmd.String(flagOutputDir), Environment: os.Environ()})
	}}
}

func snapshotPrepareInputsCmd() *cli.Command {
	return &cli.Command{Name: "prepare-inputs", Usage: "prepare exact already-built artifact downloads into bound producer outputs; no source rebuild", Flags: []cli.Flag{&cli.StringFlag{Name: flagPlan, Required: true}, &cli.StringFlag{Name: "artifact-dir", Value: snapshotimage.DefaultDownloadDir}, &cli.StringFlag{Name: flagOutputDir, Value: snapshotimage.DefaultProducerDir}}, Action: func(ctx context.Context, cmd *cli.Command) error {
		p, err := appsnapshot.ReadPlan(cmd.String(flagPlan))
		if err != nil {
			return err
		}

		return appsnapshot.PrepareInputs(ctx, gitadapter.New(), p, cmd.String("artifact-dir"), cmd.String(flagOutputDir))
	}}
}

func snapshotGatherContextCmd() *cli.Command {
	return &cli.Command{Name: "gather-context", Usage: "verify the complete producer set, install bound inputs and emit the frozen context bundle", Flags: []cli.Flag{&cli.StringFlag{Name: flagPlan, Required: true}, &cli.StringFlag{Name: "input-dir", Value: snapshotimage.DefaultProducerDir}, &cli.StringFlag{Name: flagOutput, Required: true}}, Action: func(ctx context.Context, cmd *cli.Command) error {
		p, err := appsnapshot.ReadPlan(cmd.String(flagPlan))
		if err != nil {
			return err
		}

		return appsnapshot.GatherContext(ctx, gitadapter.New(), p, cmd.String(flagPlan), cmd.String("input-dir"), cmd.String(flagOutput))
	}}
}
