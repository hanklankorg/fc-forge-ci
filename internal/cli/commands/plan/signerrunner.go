// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package plan

import (
	"context"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/runners"
)

const buildStagePlanJSONFlag = "build-stage-plan-json"

// The flags have no environment source: the administrator's list reaches the
// engine only on the command line of the layer's own job.
func admitSignerRunnerCmd() *cli.Command {
	return &cli.Command{
		Name:  "admit-signer-runner",
		Usage: "refuse a signed Xcode build on a macOS runner the administrator's allowlist does not cover",
		Description: `The signed Xcode build of the GitHub layer holds the signing certificate on
the runner its caller names in runs-on-darwin-arm64. The administrator
restricts that runner with ` + runners.SignerAllowlistInput + `, a JSON array of labels:
every label the caller named must be in it. [] lets the caller choose.

The layer runs this on a hosted runner the caller cannot name, in a job the
build needs, so a refused runner never starts the job that receives the
certificate. A build stage with no signed Xcode build is admitted; a list
that is not a JSON array of labels never is.`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: buildStagePlanJSONFlag, Required: true, Usage: "release or snapshot build-stage plan, as the Xcode build's caller reads it"},
			&cli.StringFlag{Name: "runs-on-darwin-arm64", Required: true, Usage: "the macOS runner the caller named: a JSON array of labels"},
			&cli.StringFlag{Name: runners.SignerAllowlistInput, Required: true, Usage: "the administrator's JSON array of labels a signing job on a caller-named runner may run on; [] lets the caller choose"},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			return pipeline.AdmitSignedXcodeRunner(cmd.String(buildStagePlanJSONFlag), cmd.String("runs-on-darwin-arm64"), cmd.String(runners.SignerAllowlistInput))
		},
	}
}
