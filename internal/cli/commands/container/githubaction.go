// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/urfave/cli/v3"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	appsnapshot "github.com/diggsweden/forge-ci/internal/app/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/cli/cienv"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/cli/planfile"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func prepareKanikoActionCmd() *cli.Command {
	return &cli.Command{
		Name: "prepare-kaniko-action", Usage: "prepare the GitHub workflows' isolated native Kaniko Docker action; no build or publication",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "job-image", Required: true, Usage: "approved digest-pinned Kaniko job image"},
			&cli.StringFlag{Name: "workspace", Sources: cienv.Workspace(), Required: true, Usage: "GitHub workspace containing source/"},
			&cli.StringFlag{Name: flagPlan, Required: true, Usage: "frozen container-build or snapshot plan"},
			&cli.BoolFlag{Name: "snapshot", Usage: "prepare a snapshot plan instead of a container-build plan"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			workspace, err := filepath.Abs(cmd.String("workspace"))
			if err != nil {
				return err
			}

			engine, err := os.Executable()
			if err != nil {
				return err
			}

			in := appcontainer.GitHubKanikoAction{Workspace: workspace, JobImage: cmd.String("job-image"), Engine: engine, Snapshot: cmd.Bool("snapshot")}
			if in.Snapshot {
				plan, readErr := appsnapshot.ReadPlan(cmd.String(flagPlan))
				if readErr != nil {
					return readErr
				}

				for _, build := range plan.Builds {
					if err = appcontainer.ValidateSourceBuildPaths(filepath.Join(workspace, "source"), build.Context, build.ContainerFile); err != nil {
						return err
					}
				}

				in.Plan, err = json.Marshal(plan)
			} else {
				in.Plan, err = kanikoActionBuildPlan(workspace, cmd.String(flagPlan))
				if err == nil {
					in.Scanner, err = exec.LookPath("trivy")
				}
			}

			if err != nil {
				return err
			}

			output, err := appcontainer.PrepareGitHubKanikoAction(in)
			if err != nil {
				return err
			}

			return deps.FromCmd(ctx, cmd, func(dep *deps.Deps) error {
				return dep.OutputSink.Set(ctx, "output-dir", output)
			})
		},
	}
}

func kanikoActionBuildPlan(workspace, path string) ([]byte, error) {
	body, err := os.ReadFile(path) //nolint:gosec // frozen workflow plan named by the caller.
	if err != nil {
		return nil, err
	}

	plan, err := planfile.Decode(body)
	if err != nil {
		return nil, err
	}

	build := plan["container build"]
	if len(plan) != 1 || build == nil || build["mode"] != "oci" || (build["secrets"] != nil && build["secrets"] != "") {
		return nil, fmt.Errorf("kaniko action requires one OCI container-build plan without build secrets: %w", errs.ErrValidation)
	}

	if err = validateActionBuildFields(build); err != nil {
		return nil, err
	}

	contextPath, _ := build["context"].(string)

	file, _ := build["file"].(string)
	if err = appcontainer.ValidateSourceBuildPaths(filepath.Join(workspace, "source"), contextPath, file); err != nil {
		return nil, err
	}
	// Host paths and credentials do not travel in the action image. The action
	// logs in with the workflow's scoped credential and explicitly sets output.
	delete(build, "auth-file")
	delete(build, "output-dir")

	return json.Marshal(plan)
}

func validateActionBuildFields(build map[string]any) error {
	known := map[string]bool{}

	for _, flag := range buildCmd().Flags {
		for _, name := range flag.Names() {
			known[name] = true
		}
	}

	for key, value := range build {
		if _, scalar := value.(string); !known[key] || !scalar {
			return fmt.Errorf("kaniko action plan field %q must be a string-valued container-build flag: %w", key, errs.ErrValidation)
		}
	}

	return nil
}
