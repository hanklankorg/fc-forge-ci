// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/urfave/cli/v3"

	appconfig "github.com/diggsweden/forge-ci/internal/app/config"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

const (
	flagFamiliesOutput = "families-output"
	flagMatrixOutput   = "matrix-output"
)

// releaseVersionsCmd plans a release's version bump from the declaration for
// a forge that expands the key-free version jobs as a matrix, Forgejo's
// prepare-release.yml, and for its signer's re-check of the release commit.
func releaseVersionsCmd() *cli.Command {
	return &cli.Command{
		Name:  "release-versions",
		Usage: "plan a release's version bump and its per-family version jobs from artifacts.yml",
		Description: `Plans the release's version bump from the declaration, as
   'plan gitlab-release-pipeline' does, and writes what the release's version
   jobs read:

   --matrix-output        the version jobs as one-line JSON for strategy.matrix:
                          one {"family", "project-type"} row per toolchain
                          family whose projects the plan bumps with their
                          native tool, [] for none. A planned family needs
                          its --runtime-image-<family>.
   --families-output      the same families' names, one a line, for the job
                          that joins their patches.
   --prepare-plan-output  the prepare-stage plan JSON, for 'version bump-plan'
                          and 'release validate-tag --prepare-plan-file'.

   Without --file and without .forge-ci/artifacts.yml the release has no
   planned bump; a file named by --file must exist. With --matrix-output, a
   sign.method or signing key the Forgejo release does not use is warned
   about, in the log and the job summary, with the key it signs with instead,
   and so is a sign.transparency, which the release's transparency-log input
   decides.

EXAMPLE:
   forge-ci plan release-versions \
     --runtime-image-java "registry.example.com/team/forge-ci-runtime-java-25@sha256:<digest>" \
     --matrix-output versions.json`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: flagFile, Usage: usageReleaseFile},
			&cli.StringFlag{Name: flagRuntimeImageJava, Usage: "runtime image of the java family, whose Maven projects it bumps"},
			&cli.StringFlag{Name: flagRuntimeImageNode, Usage: "runtime image of the node family, whose npm projects it bumps"},
			&cli.StringFlag{Name: flagRuntimeImageRust, Usage: "runtime image of the rust family, whose Cargo projects it bumps"},
			&cli.StringFlag{Name: flagMatrixOutput, Usage: "file to write the version jobs' matrix JSON to"},
			&cli.StringFlag{Name: flagFamiliesOutput, Usage: "file to write the planned version families to, one a line"},
			&cli.StringFlag{Name: flagPrepareOutput, Usage: "file to write the prepare-stage plan JSON to"},
		},
		Action: planReleaseVersions,
	}
}

// planReleaseVersions writes each output the command names.
func planReleaseVersions(ctx context.Context, cmd *cli.Command) error {
	if cmd.String(flagMatrixOutput) == "" && cmd.String(flagFamiliesOutput) == "" && cmd.String(flagPrepareOutput) == "" {
		return fmt.Errorf("name at least one of --%s, --%s and --%s: %w", flagMatrixOutput, flagFamiliesOutput, flagPrepareOutput, errs.ErrUsage)
	}

	file := cmd.String(flagFile)
	if file == "" {
		file = defaultGitLabArtifactsConfig
	}

	plans, err := appconfig.LoadReleaseStagePlans(appconfig.EmitConfigPlanInput{Path: file}, deps.Annotator(cmd), cmd.String(flagFile) == "")
	if err != nil {
		return err
	}

	// The plan job, which writes the matrix, warns once a run; the re-checks
	// of the same declaration do not repeat it.
	if cmd.String(flagMatrixOutput) != "" {
		if err := warnReleaseSigning(ctx, cmd, plans, provider.ForgeForgejo); err != nil {
			return err
		}
	}

	families := plans.Prepare.VersionFamilies()

	if path := cmd.String(flagPrepareOutput); path != "" {
		if err := writeJSONFile(path, plans.Prepare); err != nil {
			return err
		}
	}

	if path := cmd.String(flagFamiliesOutput); path != "" {
		names := make([]string, 0, len(families))
		for _, family := range families {
			names = append(names, family.Family)
		}

		if err := writeLines(path, names); err != nil {
			return err
		}
	}

	if path := cmd.String(flagMatrixOutput); path != "" {
		return writeVersionMatrix(cmd, path, families)
	}

	return nil
}

// writeVersionMatrix writes the version jobs' rows, each of which runs in its
// family's runtime image, so a planned family without one is refused.
func writeVersionMatrix(cmd *cli.Command, path string, families []pipeline.VersionFamily) error {
	images := map[string]string{"java": cmd.String(flagRuntimeImageJava), "node": cmd.String(flagRuntimeImageNode), "rust": cmd.String(flagRuntimeImageRust)}

	for _, family := range families {
		if strings.TrimSpace(images[family.Family]) == "" {
			return fmt.Errorf("the plan has %s projects, which need the runtime-image-%s input: %w", family.Family, family.Family, errs.ErrUsage)
		}
	}

	if families == nil {
		families = []pipeline.VersionFamily{}
	}

	// One line: a step output carries it to the matrix.
	body, err := json.Marshal(families)
	if err != nil {
		return err
	}

	if err := cliio.WriteFile(path, append(body, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %q: %w", path, err)
	}

	return nil
}
