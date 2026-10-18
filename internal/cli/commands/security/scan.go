// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package security

import (
	"context"
	"os"
	"time"

	"github.com/diggsweden/forge-ci/internal/cli/secretenv"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/trivy"
	appsecurity "github.com/diggsweden/forge-ci/internal/app/security"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/domain/security"
)

// scanGroup wires `forge-ci security scan <subcmd>`: every subcommand
// here runs Trivy against an image. `container` gates on severity and
// derives the SARIF and GitLab container-scanning reports; `container-json`
// only produces the validated raw JSON.
func scanGroup() *cli.Command {
	return &cli.Command{
		Name:  "scan",
		Usage: "run a security scanner against an image",
		Commands: []*cli.Command{
			scanContainerCmd(),
			scanContainerJSONCmd(),
		},
	}
}

func scanContainerCmd() *cli.Command {
	return &cli.Command{
		Name:  "container",
		Usage: "run `trivy image`, derive SARIF/GitLab reports, and fail on fixable or unapproved critical findings",
		Description: `EXAMPLES:
   # Scan a digest-pinned image, failing on HIGH+ findings
   forge-ci security scan container \
     --image-ref ghcr.io/owner/app@sha256:… --fail-on-severity HIGH

   # Write the trivy JSON for a downstream step
   forge-ci security scan container --image-ref ghcr.io/owner/app:v1.2.3 \
     --json-file dist/trivy.json`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: flagImageRef, Required: true, Sources: cli.EnvVars("IMAGE_REF"), Usage: "fully-qualified image (registry/owner/name@sha256:…) to scan"},
			&cli.StringFlag{Name: "json-file", Value: security.DefaultTrivyContainerJSONFile, Sources: cli.EnvVars("CONTAINER_JSON_FILE"), Usage: "destination path for the raw trivy JSON findings"},
			&cli.StringFlag{Name: flagSARIFFile, Value: security.DefaultTrivyContainerSARIFFile, Sources: cli.EnvVars("CONTAINER_SARIF_FILE"), Usage: "destination path for the trivy SARIF findings"},
			&cli.StringFlag{Name: "gitlab-report-file", Value: security.DefaultTrivyGitLabContainerFile, Sources: cli.EnvVars("CONTAINER_GITLAB_REPORT_FILE"), Usage: "destination path for the GitLab container-scanning report"},
			&cli.StringFlag{Name: "fail-on-severity", Value: "CRITICAL,HIGH", Sources: cli.EnvVars("CONTAINER_FAIL_ON_SEVERITY"), Usage: "trivy --severity filter and gate threshold: a finding at this level or above fails the scan when a fixed package exists, an unfixed CRITICAL fails it unless approved, other unfixed findings are reported (comma-list, e.g. 'CRITICAL,HIGH'; narrow to 'CRITICAL' to relax)"},
			&cli.StringFlag{Name: "ignore-file", Sources: cli.EnvVars("CONTAINER_IGNORE_FILE"), Usage: "Trivy ignore file approving findings, with reasons and expiry (default: .trivyignore.yaml when present)"},
			&cli.StringFlag{Name: "trivy-version", Sources: cli.EnvVars("TRIVY_VERSION"), Usage: "trivy version string embedded in the SARIF / GitLab report"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			annot := deps.Annotator(cmd)

			return appsecurity.ScanContainer(ctx, trivy.New(secretenv.Names()...), os.Stderr, os.Stderr, annot, appsecurity.ScanContainerInput{
				ImageRef:         cmd.String(flagImageRef),
				JSONFile:         cmd.String("json-file"),
				SARIFFile:        cmd.String(flagSARIFFile),
				GitLabReportFile: cmd.String("gitlab-report-file"),
				Severity:         cmd.String("fail-on-severity"),
				TrivyVersion:     cmd.String("trivy-version"),
				IgnoreFile:       cmd.String("ignore-file"),
			})
		},
	}
}

func scanContainerJSONCmd() *cli.Command {
	return &cli.Command{
		Name:  "container-json",
		Usage: "run `trivy image`, retry transient failures, and validate raw JSON output shape",
		Description: `Runs Trivy against one image/platform and validates that the raw
JSON output is an object with a top-level Results array. It does not fail on
vulnerability findings or derive SARIF/GitLab reports; use ` + "`security scan container`" + ` for a
severity-gated scanner.`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: flagImageRef, Required: true, Sources: cli.EnvVars("SCAN_CONTAINER_IMAGE_REF", "IMAGE_REF"), Usage: "image reference to scan"},
			&cli.StringFlag{Name: "platform", Value: "linux/amd64", Sources: cli.EnvVars("SCAN_CONTAINER_IMAGE_PLATFORM"), Usage: "OCI platform to scan, for example linux/amd64 or linux/arm64"},
			&cli.StringFlag{Name: "output", Required: true, Sources: cli.EnvVars("SCAN_CONTAINER_IMAGE_OUTPUT"), Usage: "destination path for the Trivy JSON result"},
			&cli.StringFlag{Name: "timeout", Value: "30m", Sources: cli.EnvVars("SCAN_CONTAINER_IMAGE_TIMEOUT"), Usage: "Trivy scan timeout"},
			&cli.IntFlag{Name: "attempts", Value: 3, Sources: cli.EnvVars("SCAN_CONTAINER_IMAGE_ATTEMPTS"), Usage: "scan attempts before failing"},
			&cli.IntFlag{Name: "retry-delay-seconds", Value: 20, Sources: cli.EnvVars("SCAN_CONTAINER_IMAGE_RETRY_DELAY_SECONDS"), Usage: "base delay between scan retry attempts"},
			&cli.StringFlag{Name: "scanners", Value: "vuln", Sources: cli.EnvVars("SCAN_CONTAINER_IMAGE_SCANNERS"), Usage: "comma-separated Trivy scanners"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return deps.FromCmd(ctx, cmd, func(d *deps.Deps) error {
				return appsecurity.RawContainerScan(ctx, trivy.New(secretenv.Names()...), d.OutputSink, os.Stderr, os.Stderr, appsecurity.RawContainerScanInput{
					ImageRef:   cmd.String(flagImageRef),
					Platform:   cmd.String("platform"),
					Output:     cmd.String("output"),
					Timeout:    cmd.String("timeout"),
					Attempts:   cmd.Int("attempts"),
					RetryDelay: time.Duration(cmd.Int("retry-delay-seconds")) * time.Second,
					Scanners:   cmd.String("scanners"),
				})
			})
		},
	}
}
