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
	"os"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/platform"
	appdoctor "github.com/diggsweden/forge-ci/internal/app/doctor"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/runcontext"
)

// New returns the `doctor` top-level subcommand.
func New() *cli.Command {
	return &cli.Command{
		Name:  "doctor",
		Usage: "check that a repository is configured correctly for forge-ci",
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
   1   — at least one FAIL check (ExitCodeValidation)`,
		Flags: []cli.Flag{
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
		Action: func(_ context.Context, cmd *cli.Command) error {
			env := resolveEnvironment()

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

			// Honour the global --json / --format=json: doctor's checks are
			// structured data, so a machine-readable form lets a CI gate
			// parse setup status instead of scraping the human text.
			format, err := deps.OutputFormat(cmd)
			if err != nil {
				return err
			}

			if format == output.FormatJSON {
				if err := appdoctor.FormatJSON(os.Stdout, appdoctor.Report{
					Environment: env,
					Checks:      checks,
					Failures:    failures,
				}); err != nil {
					return err
				}
			} else {
				appdoctor.FormatEnvironment(os.Stdout, env)
				_, _ = fmt.Fprintln(os.Stdout)
				appdoctor.FormatText(os.Stdout, checks)
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

// resolveEnvironment gathers the active forge/runner/capabilities for
// the doctor environment block. Detection (env reads) lives in
// internal/adapters/platform and the provider factory in internal/cli/deps; this
// helper only assembles their results into the app-layer Environment.
func resolveEnvironment() appdoctor.Environment {
	return appdoctor.Environment{
		Provider:     deps.DescriberForDetected().Describe().DisplayName,
		ForgeAPI:     platform.Detect().String(),
		Runner:       platform.DetectRunner().String(),
		Capabilities: deps.CapabilitiesForDetected(),
	}
}
