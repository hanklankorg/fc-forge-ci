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
	gitlabpipeline "github.com/diggsweden/forge-ci/internal/app/gitlabpipeline"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

const (
	defaultGitLabArtifactsConfig = ".forge-ci/artifacts.yml"
	// usageReleaseFile is the --file usage of the commands that read the
	// release's declaration.
	usageReleaseFile      = "artifacts.yml of the release (default: .forge-ci/artifacts.yml, which may be absent)"
	flagFile              = "file"
	flagFamily            = "family"
	flagPrepareOutput     = "prepare-plan-output"
	flagArtifactsOutput   = "artifacts-output"
	flagJobNamesOutput    = "job-names-output"
	flagProjectTypeOutput = "project-type-output"
	flagPackagesOutput    = "packages-output"
	flagBaseStages        = "base-stages"
	flagImageBuilder      = "image-builder"
)

// gitlabReleasePipelineCmd plans the GitLab release child from the
// declaration: the generated jobs, the version-bump plan the release commit
// applies and checks, and a build family's artifacts.
func gitlabReleasePipelineCmd() *cli.Command {
	return &cli.Command{
		Name:  "gitlab-release-pipeline",
		Usage: "plan the GitLab release child pipeline's version and build jobs from artifacts.yml",
		Description: `Plans the release's version bump and builds from the declaration, as
   'plan release' does, and writes what the GitLab release jobs read:

   --output               the generated part of the release child pipeline:
                          one "Prepare: version <family>" job per toolchain
                          family whose projects the plan bumps with their
                          native tool, one "Build: <family>" job per family it
                          builds, and "Publish: npm packages" when an npm
                          project lists forge-packages. Each extends its
                          hidden job in templates/release-jobs.yml and carries
                          nothing else.
                          A planned family needs its --runtime-image-<family>.
   --prepare-plan-output  the prepare-stage plan JSON, for 'version bump-plan'
                          and 'release validate-tag --prepare-plan-file'.
   --artifacts-output     with --family, the names of the artifacts that
                          family's build job builds, one a line.
   --job-names-output     the names of the generated jobs, one a line, for
                          the jobs that check every planned job ran.
   --project-type-output  with --family, the project type that family's
                          version job bumps with its native tool; a family
                          the plan bumps no project of is refused.
   --base-stages          adds the jobs of each stage of the policy's base
                          image graph: plan, build, publish, and sign and
                          promote, a child's parent job, and one gate.
   --packages-output      the directories of the npm projects the release publishes
                          to the project's package registry, one a line.

   Without --file and without .forge-ci/artifacts.yml the release has no
   planned bump and no family; a file named by --file must exist. With
   --output, a sign.method or signing key the GitLab release does not use is
   warned about, in the log and the job summary, with the key it signs with
   instead, and so is a sign.transparency, which the release's
   transparency-log input decides.

EXAMPLE:
   forge-ci plan gitlab-release-pipeline \
     --runtime-image-java "registry.example.com/team/forge-ci-runtime-java-25@sha256:<digest>" \
     --output release-families.yml`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: flagFile, Usage: usageReleaseFile},
			&cli.StringFlag{Name: flagRuntimeImageJava, Usage: "runtime image of the java family (Maven, Gradle)"},
			&cli.StringFlag{Name: flagRuntimeImageNode, Usage: "runtime image of the node family (npm)"},
			&cli.StringFlag{Name: flagRuntimeImageGo, Usage: "runtime image of the go family"},
			&cli.StringFlag{Name: flagRuntimeImageRust, Usage: "runtime image of the rust family (Cargo)"},
			&cli.StringFlag{Name: flagRuntimeImageAndroid, Usage: "runtime image of the android family (Gradle Android)"},
			&cli.StringFlag{Name: flagOutput, Usage: "file to write the generated release jobs to"},
			&cli.StringFlag{Name: flagPrepareOutput, Usage: "file to write the prepare-stage plan JSON to"},
			&cli.StringFlag{Name: flagFamily, Usage: "family whose artifacts --artifacts-output lists or whose project type --project-type-output names: java, node, go, rust or android"},
			&cli.StringFlag{Name: flagArtifactsOutput, Usage: "file to write --family's artifact names to, one a line"},
			&cli.StringFlag{Name: flagJobNamesOutput, Usage: "file to write the generated jobs' names to, one a line"},
			&cli.StringFlag{Name: flagProjectTypeOutput, Usage: "file to write --family's version project type to"},
			&cli.StringFlag{Name: flagBaseStages, Usage: "the policy's base image graph: stages separated by spaces, each <flavor> for a root or <flavor>[+<flavor>...]:<parent> for the children of one parent built by an earlier stage"},
			&cli.StringFlag{Name: flagImageBuilder, Value: "buildah", Usage: "the policy's image builder; a child base stage is refused with kaniko"},
			&cli.StringFlag{Name: flagPackagesOutput, Usage: "file to write the directories of the npm projects published to forge-packages to, one a line"},
		},
		Action: planGitLabRelease,
	}
}

// planGitLabRelease writes each output the command names.
func planGitLabRelease(ctx context.Context, cmd *cli.Command) error {
	if err := requireGitLabReleaseOutputs(cmd); err != nil {
		return err
	}

	file := cmd.String(flagFile)
	if file == "" {
		file = defaultGitLabArtifactsConfig
	}

	plans, err := appconfig.LoadReleaseStagePlans(appconfig.EmitConfigPlanInput{Path: file}, deps.Annotator(cmd), cmd.String(flagFile) == "")
	if err != nil {
		return err
	}

	if err := pipeline.RefuseDistroPackages(plans.DistroPackages, "GitLab", "GitLab's Debian registry is an experiment behind a feature flag and it has no RPM or Alpine registry"); err != nil {
		return err
	}

	// "Release: plan", which writes the jobs, warns once a run; the release
	// jobs' re-checks of the same declaration do not repeat it.
	if cmd.String(flagOutput) != "" {
		if err := warnReleaseSigning(ctx, cmd, plans, provider.ForgeGitLab); err != nil {
			return err
		}
	}

	if path := cmd.String(flagPrepareOutput); path != "" {
		if err := writeJSONFile(path, plans.Prepare); err != nil {
			return err
		}
	}

	if err := writeFamilyOutputs(cmd, plans); err != nil {
		return err
	}

	if path := cmd.String(flagPackagesOutput); path != "" {
		if err := writeLines(path, gitlabpipeline.ReleasePackages(plans.Build)); err != nil {
			return err
		}
	}

	if cmd.String(flagOutput) == "" && cmd.String(flagJobNamesOutput) == "" {
		return nil
	}

	return writeReleaseFamilies(cmd, plans)
}

// requireGitLabReleaseOutputs refuses a command that names no output, and
// one whose --family does not go with an output of that family.
func requireGitLabReleaseOutputs(cmd *cli.Command) error {
	if cmd.String(flagOutput) == "" && cmd.String(flagPrepareOutput) == "" && cmd.String(flagArtifactsOutput) == "" && cmd.String(flagJobNamesOutput) == "" && cmd.String(flagProjectTypeOutput) == "" && cmd.String(flagPackagesOutput) == "" {
		return fmt.Errorf("name at least one of --%s, --%s, --%s, --%s, --%s and --%s: %w", flagOutput, flagPrepareOutput, flagArtifactsOutput, flagJobNamesOutput, flagProjectTypeOutput, flagPackagesOutput, errs.ErrUsage)
	}

	familyOutput := cmd.String(flagArtifactsOutput) != "" || cmd.String(flagProjectTypeOutput) != ""
	if (cmd.String(flagFamily) == "") == familyOutput {
		return fmt.Errorf("--%s goes with --%s or --%s: %w", flagFamily, flagArtifactsOutput, flagProjectTypeOutput, errs.ErrUsage)
	}

	return nil
}

// writeFamilyOutputs writes what --family's jobs read: the artifacts its
// build job builds and the project type its version job bumps.
func writeFamilyOutputs(cmd *cli.Command, plans appconfig.ReleaseStagePlans) error {
	if path := cmd.String(flagArtifactsOutput); path != "" {
		names, err := gitlabpipeline.ReleaseFamilyArtifacts(plans.Build, cmd.String(flagFamily))
		if err != nil {
			return err
		}

		if err := writeLines(path, names); err != nil {
			return err
		}
	}

	if path := cmd.String(flagProjectTypeOutput); path != "" {
		return writeVersionProjectType(path, plans.Prepare, cmd.String(flagFamily))
	}

	return nil
}

// writeVersionProjectType writes the project type the plan's version family
// bumps with its native tool, so a version job takes it from the plan.
func writeVersionProjectType(path string, prepare pipeline.ReleasePrepareStagePlan, family string) error {
	for _, planned := range prepare.VersionFamilies() {
		if planned.Family == family {
			return writeLines(path, []string{string(planned.ProjectType)})
		}
	}

	return fmt.Errorf("the plan bumps no project of the %s family with its native tool: %w", family, errs.ErrUsage)
}

// writeReleaseFamilies writes the generated family jobs, which need each
// planned family's runtime image, and their names, which do not.
func writeReleaseFamilies(cmd *cli.Command, plans appconfig.ReleaseStagePlans) error {
	families, err := gitlabpipeline.PlanReleaseFamilies(plans.Prepare, plans.Build)
	if err != nil {
		return err
	}

	if families.Bases, err = gitlabpipeline.ParseBaseStages(cmd.String(flagBaseStages), cmd.String(flagImageBuilder)); err != nil {
		return err
	}

	if path := cmd.String(flagOutput); path != "" {
		if err := families.RequireImages(runtimeImages(cmd)); err != nil {
			return err
		}

		if err := writeJSONFile(path, families.Jobs()); err != nil {
			return err
		}
	}

	if path := cmd.String(flagJobNamesOutput); path != "" {
		return writeLines(path, families.JobNames())
	}

	return nil
}

// writeLines writes one line a value, or an empty file for none.
func writeLines(path string, values []string) error {
	body := ""
	if len(values) > 0 {
		body = strings.Join(values, "\n") + "\n"
	}

	if err := cliio.WriteFile(path, []byte(body), 0o644); err != nil {
		return fmt.Errorf("write %q: %w", path, err)
	}

	return nil
}

// writeJSONFile writes value as indented JSON, which GitLab also reads as YAML.
func writeJSONFile(path string, value any) error {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}

	if err := cliio.WriteFile(path, append(body, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %q: %w", path, err)
	}

	return nil
}
