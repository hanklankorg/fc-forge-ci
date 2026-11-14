// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/urfave/cli/v3"
)

func runtimePublicationVerifySmokeCmd() *cli.Command {
	return &cli.Command{
		Name: "verify-smoke", Usage: "verify a native smoke receipt matches the exact source and candidate digest",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "receipt", Required: true, Usage: "downloaded smoke receipt JSON file"},
			&cli.StringFlag{Name: flagSourceSHA, Required: true, Usage: "expected full product source commit"},
			&cli.StringFlag{Name: flagTarget, Required: true, Usage: "expected product runtime target"},
			&cli.StringFlag{Name: flagArch, Required: true, Usage: "expected native architecture, amd64 or arm64"},
			&cli.StringFlag{Name: flagImageRef, Required: true, Usage: "expected digest-pinned candidate image"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return appcontainer.VerifyRuntimeSmoke(ctx, cmd.String("receipt"), domaincontainer.RuntimeSmokeReceipt{
				Source: cmd.String(flagSourceSHA), Target: cmd.String(flagTarget), Arch: cmd.String(flagArch), Ref: cmd.String(flagImageRef),
			})
		},
	}
}
