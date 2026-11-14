// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	appenforcement "github.com/diggsweden/forge-ci/internal/app/enforcement"
	"github.com/diggsweden/forge-ci/internal/app/gitlabpipeline"
	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/enforcement"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
)

func gitlabSnapshotPipelineCmd() *cli.Command {
	names := []string{"plan", flagOutput, "project", "engine-sha", "server-url", "registry", "pipeline-id", "context-job", "job-image", "build-image", "image-builder", "builder-job-image", flagRuntimeImageGo, flagRuntimeImageJava, flagRuntimeImageNode, flagRuntimeImageRust}
	tagNames := []string{"runner-tags", "amd64-tags", flagARM64Tags}

	flags := make([]cli.Flag, 0, len(names)+len(tagNames))
	for _, name := range names {
		flags = append(flags, &cli.StringFlag{Name: name})
	}

	for _, name := range tagNames {
		flags = append(flags, &cli.StringFlag{Name: name, Value: "[]", Usage: "JSON string array of runner tags"})
	}
	// The child pipeline's enforcement inputs come from the root's validated
	// administrator settings (--enforce-* before or after the subcommand),
	// never from flags of its own: only the root declares those.
	return &cli.Command{Name: "gitlab-snapshot-pipeline", Usage: "render a native GitLab snapshot child pipeline from its frozen image plan", Flags: flags,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			policy, err := appenforcement.Resolve(ctx)
			if err != nil {
				return err
			}

			tests, err := policy.Enforces(enforcement.ApplicationTests)
			if err != nil {
				return err
			}

			vulnerabilities, err := policy.Enforces(enforcement.Vulnerabilities)
			if err != nil {
				return err
			}

			body, err := cliio.ReadRegularFile(cmd.String("plan"))
			if err != nil {
				return err
			}

			var plan snapshotimage.Plan
			if err := json.Unmarshal(body, &plan); err != nil {
				return err
			}

			in := gitlabpipeline.SnapshotPipelineOptions{Project: cmd.String("project"), Pin: cmd.String("engine-sha"), Server: cmd.String("server-url"), Registry: cmd.String("registry"), Pipeline: cmd.String("pipeline-id"), Context: cmd.String("context-job"), JobImage: cmd.String("job-image"), BuildImage: cmd.String("build-image"), Builder: cmd.String("image-builder"), BuilderImage: cmd.String("builder-job-image"), ApplicationTests: tests, Vulnerabilities: vulnerabilities, Images: gitlabpipeline.RuntimeImages{Go: cmd.String(flagRuntimeImageGo), Java: cmd.String(flagRuntimeImageJava), Node: cmd.String(flagRuntimeImageNode), Rust: cmd.String(flagRuntimeImageRust)}}
			for name, dest := range map[string]*[]string{"runner-tags": &in.Tags, "amd64-tags": &in.AMD64Tags, flagARM64Tags: &in.ARM64Tags} {
				if err := json.Unmarshal([]byte(cmd.String(name)), dest); err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
			}

			out, err := gitlabpipeline.SnapshotPipeline(plan, in)
			if err != nil {
				return err
			}

			encoded, err := json.MarshalIndent(out, "", "  ")
			if err != nil {
				return err
			}

			if cmd.String(flagOutput) == "" {
				_, err = os.Stdout.Write(append(encoded, '\n'))

				return err
			}

			return os.WriteFile(cmd.String(flagOutput), append(encoded, '\n'), 0o600)
		},
	}
}
