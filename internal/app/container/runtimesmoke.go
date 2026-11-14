// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"

	"github.com/diggsweden/forge-ci/internal/cliio"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
)

// VerifyRuntimeSmoke reads bounded regular-file evidence without running a
// candidate or contacting a provider. Artifact transport remains the caller's.
func VerifyRuntimeSmoke(ctx context.Context, file string, expected domaincontainer.RuntimeSmokeReceipt) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := domaincontainer.VerifyRuntimeSmokeReceipt(expected, expected); err != nil {
		return err
	}

	body, err := cliio.ReadRegularFile(file)
	if err != nil {
		return err
	}

	var receipt domaincontainer.RuntimeSmokeReceipt
	if err := decodeRuntimeJSON(body, &receipt); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	return domaincontainer.VerifyRuntimeSmokeReceipt(receipt, expected)
}
