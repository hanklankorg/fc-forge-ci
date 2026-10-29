// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"os"

	gitadapter "github.com/diggsweden/forge-ci/internal/adapters/git"
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
	cmd := safeexec.Command(ctx, executable, in.Args...)
	cmd.Dir = in.Dir
	cmd.Env = in.Env
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return safeexec.WrapError(cmd.Run(), "forge-ci", "artifact build")
}

func snapshotBuildArtifactCmd() *cli.Command {
	return &cli.Command{Name: "build-artifact", Usage: "build one frozen artifact with the existing ecosystem CLI, or prepare its already-built CI upload", Flags: []cli.Flag{&cli.StringFlag{Name: "plan", Required: true}, &cli.StringFlag{Name: "name", Required: true}, &cli.StringFlag{Name: "output-dir", Value: snapshotimage.DefaultProducerDir}, &cli.StringFlag{Name: "from-artifact", Usage: "exact downloaded artifact directory; skips the ecosystem build"}}, Action: func(ctx context.Context, cmd *cli.Command) error {
		p, err := appsnapshot.ReadPlan(cmd.String("plan"))
		if err != nil {
			return err
		}
		return appsnapshot.BuildArtifact(ctx, gitadapter.New(), snapshotSelfBuild{}, p, appsnapshot.BuildArtifactInput{Name: cmd.String("name"), OutputDir: cmd.String("output-dir"), FromArtifact: cmd.String("from-artifact"), Environment: os.Environ()})
	}}
}

func snapshotPrepareInputsCmd() *cli.Command {
	return &cli.Command{Name: "prepare-inputs", Usage: "prepare exact already-built artifact downloads into bound producer outputs; no source rebuild", Flags: []cli.Flag{&cli.StringFlag{Name: "plan", Required: true}, &cli.StringFlag{Name: "artifact-dir", Value: snapshotimage.DefaultDownloadDir}, &cli.StringFlag{Name: "output-dir", Value: snapshotimage.DefaultProducerDir}}, Action: func(ctx context.Context, cmd *cli.Command) error {
		p, err := appsnapshot.ReadPlan(cmd.String("plan"))
		if err != nil {
			return err
		}
		return appsnapshot.PrepareInputs(ctx, gitadapter.New(), p, cmd.String("artifact-dir"), cmd.String("output-dir"))
	}}
}

func snapshotGatherContextCmd() *cli.Command {
	return &cli.Command{Name: "gather-context", Usage: "verify the complete producer set, install bound inputs and emit the frozen context bundle", Flags: []cli.Flag{&cli.StringFlag{Name: "plan", Required: true}, &cli.StringFlag{Name: "input-dir", Value: snapshotimage.DefaultProducerDir}, &cli.StringFlag{Name: "output", Required: true}}, Action: func(ctx context.Context, cmd *cli.Command) error {
		p, err := appsnapshot.ReadPlan(cmd.String("plan"))
		if err != nil {
			return err
		}
		return appsnapshot.GatherContext(ctx, gitadapter.New(), p, cmd.String("plan"), cmd.String("input-dir"), cmd.String("output"))
	}}
}
