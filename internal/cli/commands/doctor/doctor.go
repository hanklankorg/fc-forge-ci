// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package doctor wires `forge-ci doctor` — runs a fixed set of
// setup-validation checks against the current repository and emits
// an OK/WARN/FAIL report. Designed for "I just adopted forge-ci,
// am I set up correctly?" UX.
package doctor

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/platform"
	appdoctor "github.com/diggsweden/forge-ci/internal/app/doctor"
	appenforcement "github.com/diggsweden/forge-ci/internal/app/enforcement"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/runcontext"
)

// New returns the `doctor` top-level subcommand.
func New(commit string) *cli.Command {
	return &cli.Command{
		Name:  "doctor",
		Usage: "check local repository configuration for forge-ci",
		Description: `EXAMPLES:
   # Run all checks in the current directory:
   forge-ci doctor

   # Run against a specific repo root:
   forge-ci doctor --root /path/to/repo

   # Use a custom artifacts.yml location:
   forge-ci doctor --artifacts-file ./custom/artifacts.yml

   # Machine-readable report for a CI gate:
   forge-ci doctor --json | jq '.failures'

Exit codes:
   0   — all checks pass or are warnings
   1   — at least one FAIL check
   66  — the --root directory does not exist`,
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "requirements", Usage: "show local release setup guidance for GitHub, GitLab and Forgejo; unverified workflow contracts are labelled"},
			&cli.StringFlag{
				Name:    "root",
				Sources: cli.EnvVars("FORGE_CI_DOCTOR_ROOT"),
				Usage:   "repository root to inspect (default: cwd)",
			},
			&cli.StringFlag{
				Name:    "artifacts-file",
				Sources: cli.EnvVars("FORGE_CI_DOCTOR_ARTIFACTS"),
				Usage:   "path to the artifacts.yml to inspect (default: <root>/.forge-ci/artifacts.yml)",
			},
			&cli.StringFlag{
				Name:    "forge-ci-repository",
				Sources: cli.EnvVars("FORGE_CI_REPO_SLUG"),
				Usage:   "owner/repo this binary belongs to, for the workflow-pin check (default: derived from the binary's own module path)",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			env := resolveEnvironment(ctx)

			checks, err := appdoctor.Run(appdoctor.Input{
				Root:             cmd.String("root"),
				ArtifactsPath:    cmd.String("artifacts-file"),
				RepoSlug:         cmd.String("forge-ci-repository"),
				KeylessAvailable: env.Capabilities.PublicFulcioTrusted,
				ForgeAPI:         provider.ForgeAPI(env.ForgeAPI),
				ServerURL:        runcontext.ServerURL().Resolve(os.Getenv),
			})
			if err != nil {
				return err
			}

			failures := appdoctor.CountFailures(checks)

			var requirements *appdoctor.RequirementsReport

			if cmd.Bool("requirements") {
				report := appdoctor.Requirements(cmd.String("root"), cmd.String("forge-ci-repository"), commit)
				requirements = &report
			}

			// Honour the global --json / --format=json: doctor's checks are
			// structured data, so a machine-readable form lets a CI gate
			// parse setup status instead of scraping the human text.
			format, err := deps.OutputFormat(cmd)
			if err != nil {
				return err
			}

			if format == output.FormatJSON {
				if err := appdoctor.FormatJSON(os.Stdout, appdoctor.Report{
					Environment:  env,
					Checks:       checks,
					Failures:     failures,
					Requirements: requirements,
				}); err != nil {
					return err
				}
			} else {
				writeTextReport(os.Stdout, env, checks, failures)

				if requirements != nil {
					appdoctor.FormatRequirements(os.Stdout, *requirements)
				}
			}

			if failures > 0 {
				// Wrap ErrValidation so main maps the exit code via
				// errs.ExitCodeFromError; the message complements
				// (not duplicates) the FormatText output above.
				return fmt.Errorf("doctor: %d failing check(s); see output above: %w", failures, errs.ErrValidation)
			}

			return nil
		},
	}
}

// writeTextReport prints the human report: the environment block, the
// checks, the scope caveat, and one line that says what to do now, so the
// report never ends on a caveat.
func writeTextReport(out io.Writer, env appdoctor.Environment, checks []appdoctor.Check, failures int) {
	appdoctor.FormatEnvironment(out, env)
	_, _ = fmt.Fprintln(out)
	appdoctor.FormatText(out, checks)

	_, _ = fmt.Fprintln(out, "\nScope: local configuration checks only; remote secrets, runner availability and publishing permissions were not checked.")
	if env.ForgeAPI == string(provider.ForgeGitLab) {
		_, _ = fmt.Fprintln(out, "GitLab include pins are not checked by the workflow-pin scan.")
	}

	// The pull-request advice is for a person at a terminal. Two release
	// workflows run doctor inside a job, where that line would tell the job
	// to open a pull request, so only the failure case keeps its Next: there.
	switch {
	case failures > 0:
		_, _ = fmt.Fprintln(out, "Next: fix the FAIL lines above, then run forge-ci doctor again.")
	case env.Runner != string(provider.RunnerLocal):
	case hasWarnings(checks):
		_, _ = fmt.Fprintln(out, "Next: review the WARN lines above, then commit the Forge CI files and open a pull request; the reusable workflows check the rest in CI. "+
			"forge-ci setup status lists what is left before a first release.")
	default:
		_, _ = fmt.Fprintln(out, "Next: commit the Forge CI files and open a pull request; the reusable workflows check the rest in CI. "+
			"forge-ci setup status lists what is left before a first release.")
	}
}

// hasWarnings reports whether any check is advisory. Warnings never change
// the exit code, so the report says they are there instead.
func hasWarnings(checks []appdoctor.Check) bool {
	for _, c := range checks {
		if c.Severity == appdoctor.SeverityWarn {
			return true
		}
	}

	return false
}

// resolveEnvironment gathers the active forge/runner/capabilities for
// the doctor environment block. Detection (env reads) lives in
// internal/adapters/platform and the provider factory in internal/cli/deps; this
// helper only assembles their results into the app-layer Environment.
func resolveEnvironment(ctx context.Context) appdoctor.Environment {
	env := appdoctor.Environment{
		Provider:     deps.DescriberForDetected().Describe().DisplayName,
		ForgeAPI:     platform.Detect().String(),
		Runner:       platform.DetectRunner().String(),
		Capabilities: deps.CapabilitiesForDetected(),
	}

	// The same resolver the gates use, so the line says what this binary,
	// with this command line, would block. When it refuses, the
	// administrator-policy check fails with the reason, and there is no
	// policy to describe.
	if policy, err := appenforcement.Resolve(ctx); err == nil {
		env.Enforcement = appenforcement.Summary(policy)
	}

	return env
}
