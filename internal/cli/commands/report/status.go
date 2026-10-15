// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package report

import (
	"context"
	"fmt"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/git"
	appsummary "github.com/diggsweden/forge-ci/internal/app/summary"
	"github.com/diggsweden/forge-ci/internal/cli/cienv"
	"github.com/diggsweden/forge-ci/internal/cli/commonflags"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	domainsummary "github.com/diggsweden/forge-ci/internal/domain/summary"
)

// statusGroup wires `forge-ci report status <verb>` — pipeline /
// stage status summaries that summarise the outcome of a CI stage or
// release-prerequisite gate rather than a single build/publish step.
func statusGroup() *cli.Command {
	return &cli.Command{
		Name:  "status", //nolint:goconst // test fixture / generic identifier — extracting would explode setup boilerplate.
		Usage: "append a stage / job / prerequisite status summary",
		Commands: []*cli.Command{
			statusPrerequisitesCmd(),
			statusSBOMCountCmd(),
			statusQualityCheckCmd(),
		},
	}
}

// stageResultCmd COMPOSES the typed stage-result contract (it does not render a
// summary), so it lives flat under `report`, not under `report status` whose
// other verbs append step-summary blocks.
func stageResultCmd() *cli.Command {
	return &cli.Command{
		Name:  "stage-result",
		Usage: "compose a typed stage-result manifest from a stage plan and target results",
		// Each --result, --extra and --json-field is one key=value pair whose
		// value may hold commas; splitting on them broke a pair, as it did for
		// plan write --set.
		DisableSliceFlagSeparator: true,
		Description: `Aggregates outcomes against --stage-plan-json or STAGE_PLAN_JSON (required).
Explicit --result pairs take precedence over --job-results / JOB_RESULTS.
Otherwise it collects local job records from $CI_RESULTS_DIR/jobs/
(default .ci-results/jobs/). The workflow must transfer only the current
run/stage's records into a fresh result root; the command does not download them.

A planned target with no record is recorded as failure. Successful command
execution means the manifest and outputs were written, not that the stage
succeeded. Consume stage-result or result-json when gating the pipeline.

EXAMPLES (choose one result source):
   export STAGE_PLAN_JSON='{"version":1,"stage":"build","targets":{"maven":{"runs":true},"npm":{"runs":false}}}'
   forge-ci report stage-result --job-results '{"maven":{"result":"success"}}'
   # Or provide results explicitly for planned targets:
   forge-ci report stage-result --result maven=success
   # Or, after restoring this stage's job records, with JOB_RESULTS unset:
   forge-ci report stage-result

GitHub/Forgejo workflow env binding (YAML, not shell):
   JOB_RESULTS: ${{ toJson(needs) }}`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "stage-plan-json", Sources: cli.EnvVars("STAGE_PLAN_JSON"), Usage: "typed stage-plan JSON listing the targets this stage was expected to run"},
			&cli.StringFlag{Name: "job-results", Sources: cli.EnvVars("JOB_RESULTS"), Usage: "JSON {job:{result}} map of job outcomes (GitHub/Forgejo pass toJson(needs)); GitLab omits this and uses collected per-job records"},
			&cli.StringSliceFlag{Name: "result", Usage: "target=result pair; when set, supplies results explicitly instead of using job records"},
			&cli.StringSliceFlag{Name: "extra", Usage: "manifest extra key=value pair"},
			&cli.StringFlag{Name: "json-output-key", Usage: "optional extra output key for JSON fields"},
			&cli.StringSliceFlag{Name: "json-field", Usage: "field=value pair for --json-output-key"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			results, err := parseKeyValues("--result", cmd.StringSlice("result"))
			if err != nil {
				return err
			}

			extras, err := parseKeyValues("--extra", cmd.StringSlice("extra"))
			if err != nil {
				return err
			}

			jsonFields, err := parseKeyValues("--json-field", cmd.StringSlice("json-field"))
			if err != nil {
				return err
			}

			return deps.FromCmd(ctx, cmd, func(d *deps.Deps) error {
				_, err := appsummary.StageResult(ctx, d.OutputSink, d.ManifestSink, d.JobResultStore, appsummary.StageResultInput{
					StagePlanJSON: cmd.String("stage-plan-json"),
					JobResultsMap: cmd.String("job-results"),
					Results:       results,
					Extras:        extras,
					JSONOutputKey: cmd.String("json-output-key"),
					JSONFields:    jsonFields,
				})

				return err
			})
		},
	}
}

// jobResultCmd writes the calling job's own outcome to the JobResultStore so a
// downstream `report stage-result` can aggregate it — the forge-neutral
// replacement for GitHub's toJson(needs). It is each job's last, always-run
// step. Like stage-result it COMPOSES a typed record (no summary rendering), so
// it lives flat under `report`.
func jobResultCmd() *cli.Command {
	return &cli.Command{
		Name:  "job-result",
		Usage: "record this job's outcome for cross-job stage aggregation",
		Description: `Writes $CI_RESULTS_DIR/jobs/<name>.json (default .ci-results/jobs/<name>.json).
Run it as the job's last, always-run step; --status takes the runner's own job
status (GitHub ${{ job.status }} / GitLab $CI_JOB_STATUS). Unknown status records
failure. A successful write returns success even when the recorded job failed.

The workflow must preserve the original job failure and transfer this record
to the downstream stage-result job. Records carry no run/stage identity; collect
only the current run/stage's records in a fresh result root.

EXAMPLE:
   # GitHub: trailing 'if: always()' step
   forge-ci report job-result --name nanolinter --status "$JOB_STATUS"`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "name", Required: true, Sources: cli.EnvVars("JOB_NAME"), Usage: "this job's name; must match the stage-plan target it aggregates against"},
			&cli.StringFlag{Name: "status", Sources: cli.EnvVars("JOB_STATUS"), Usage: "the runner's job status (GitHub job.status / GitLab CI_JOB_STATUS); unknown is treated as failure"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return deps.FromCmd(ctx, cmd, func(d *deps.Deps) error {
				return appsummary.JobResult(ctx, d.JobResultStore, appsummary.JobResultInput{
					Job:    cmd.String("name"),
					Status: cmd.String("status"),
				})
			})
		},
	}
}

func parseKeyValues(flag string, values []string) ([]domainsummary.KeyValue, error) {
	parsed := make([]domainsummary.KeyValue, 0, len(values))
	for _, value := range values {
		key, val, ok := strings.Cut(value, "=")

		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("%s must be key=value, got %q: %w", flag, value, errs.ErrUsage)
		}

		parsed = append(parsed, domainsummary.KeyValue{Key: key, Value: val})
	}

	return parsed, nil
}

func statusPrerequisitesCmd() *cli.Command {
	return &cli.Command{
		Name:  "prerequisites",
		Usage: "append the release prerequisites validation report (tag/commit info, secrets, validations)",
		Description: `EXAMPLE:
   forge-ci report status prerequisites --tag v1.2.3 --ref-type tag --job-status success`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "tag", Sources: cienv.Tag(), Usage: "tag the release is anchored to (e.g. v1.2.3)"},
			&cli.StringFlag{Name: "commit", Sources: cienv.Commit(), Usage: "commit SHA the tag points at"},
			&cli.StringFlag{Name: "ref-type", Sources: cienv.RefType(), Usage: "trigger ref type (tag/branch/…)"},
			&cli.StringFlag{Name: "config-plan-json", Sources: cli.EnvVars("CONFIG_PLAN_JSON"), Usage: "typed config-plan JSON used to describe targets in the summary"},
			&cli.StringFlag{Name: "project-types", Sources: cli.EnvVars("PROJECT_TYPES"), Usage: "comma-separated ecosystems detected in the project"},
			&cli.StringFlag{Name: "build-types", Sources: cli.EnvVars("BUILD_TYPES"), Usage: "comma-separated build types planned (library/application/...)"},
			&cli.StringFlag{Name: "registry", Sources: cli.EnvVars("CONTAINER_REGISTRY"), Usage: "container registry the release will push to"},
			&cli.BoolFlag{Name: "sign-artifacts", Sources: cli.EnvVars("SIGN_ARTIFACTS"), Usage: "GPG signing was requested for release artifacts"},
			&cli.BoolFlag{Name: "require-allowlisted-signer", Sources: cli.EnvVars("REQUIRE_ALLOWLISTED_SIGNER"), Usage: "the signer-allowlist gate is enforced for this release"},
			&cli.StringFlag{Name: "job-status", Sources: cli.EnvVars("JOB_STATUS"), Usage: "validator job outcome (success/failure/cancelled)"},
			&cli.StringFlag{Name: "publish-to", Sources: cli.EnvVars("PUBLISH_TO"), Usage: "comma/space/newline-separated publish targets in the plan (maven-central/npm/…)"},
			&cli.BoolFlag{Name: "has-release-gpg-private-key", Sources: cli.EnvVars("HAS_RELEASE_GPG_PRIVATE_KEY"), Usage: "the GPG private-key secret is configured"},
			&cli.BoolFlag{Name: "has-release-gpg-passphrase", Sources: cli.EnvVars("HAS_RELEASE_GPG_PASSPHRASE"), Usage: "the GPG passphrase secret is configured"},
			&cli.BoolFlag{Name: "has-release-token", Sources: cli.EnvVars("HAS_RELEASE_TOKEN"), Usage: "the release-bot token secret is configured"},
			&cli.BoolFlag{Name: "has-release-gpg-public-key", Sources: cli.EnvVars("HAS_RELEASE_GPG_PUBLIC_KEY"), Usage: "the GPG public-key secret is configured"},
			&cli.BoolFlag{Name: "has-maven-central-username", Sources: cli.EnvVars("HAS_MAVEN_CENTRAL_USERNAME"), Usage: "the Maven Central username secret is configured"},
			&cli.BoolFlag{Name: "has-maven-central-password", Sources: cli.EnvVars("HAS_MAVEN_CENTRAL_PASSWORD"), Usage: "the Maven Central password secret is configured"},
			&cli.BoolFlag{Name: "has-npm-token", Sources: cli.EnvVars("HAS_NPM_TOKEN"), Usage: "the npm token secret is configured"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return deps.FromCmd(ctx, cmd, func(d *deps.Deps) error {
				return appsummary.Prerequisites(ctx, d.SummarySink, git.New(), appsummary.PrerequisitesSummaryInput{
					TagName:                  cmd.String("tag"),
					CommitSHA:                cmd.String("commit"),
					RefType:                  provider.RefType(cmd.String("ref-type")),
					ConfigPlanJSON:           cmd.String("config-plan-json"),
					ProjectTypes:             cmd.String("project-types"),
					BuildTypes:               cmd.String("build-types"),
					ContainerRegistry:        cmd.String("registry"),
					SignArtifacts:            cmd.Bool("sign-artifacts"),
					RequireAllowlistedSigner: cmd.Bool("require-allowlisted-signer"),
					JobStatus:                domainsummary.NormalizeResult(cmd.String("job-status")),
					PublishTo:                cmd.String("publish-to"),
					HasReleaseGPGPrivateKey:  cmd.Bool("has-release-gpg-private-key"),
					HasReleaseGPGPassphrase:  cmd.Bool("has-release-gpg-passphrase"),
					HasReleaseToken:          cmd.Bool("has-release-token"),
					HasReleaseGPGPublicKey:   cmd.Bool("has-release-gpg-public-key"),
					HasMavenCentralUsername:  cmd.Bool("has-maven-central-username"),
					HasMavenCentralPassword:  cmd.Bool("has-maven-central-password"),
					HasNPMToken:              cmd.Bool("has-npm-token"),
				})
			})
		},
	}
}

func statusSBOMCountCmd() *cli.Command {
	return &cli.Command{
		Name:  "sbom-count",
		Usage: "append SBOM status for multi-artifact bom.json outputs",
		Description: `EXAMPLE:
   forge-ci report status sbom-count --kind build --outcome success`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "kind", Sources: cli.EnvVars("SBOM_KIND"), Usage: "SBOM kind (build/source/analyzed-container/…)"},
			&cli.StringFlag{Name: "outcome", Sources: cli.EnvVars("SBOM_OUTCOME"), Usage: "SBOM step outcome (success/failure/skipped)"},
			commonflags.WorkingDir("directory the bom.json files were produced in"),
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return deps.FromCmd(ctx, cmd, func(d *deps.Deps) error {
				return appsummary.SBOMCountStatus(ctx, d.SummarySink, appsummary.SBOMCountStatusInput{
					Kind:    cmd.String("kind"),
					Outcome: cmd.String("outcome"),
					WorkDir: cmd.String("working-dir"),
				})
			})
		},
	}
}

func statusQualityCheckCmd() *cli.Command {
	return &cli.Command{
		Name:      "quality-check",
		Usage:     "append a PR-quality summary block from \"Name|enabled|result\" args",
		ArgsUsage: "<Name|enabled|result> ...",
		Description: `EXAMPLE:
   forge-ci report status quality-check "nanolinter|true|success" "swiftlint|true|skipped"`,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			checks := appsummary.ParseQualityChecks(cmd.Args().Slice())

			return deps.FromCmd(ctx, cmd, func(d *deps.Deps) error {
				return appsummary.QualityCheckStatus(ctx, d.SummarySink, checks)
			})
		},
	}
}
