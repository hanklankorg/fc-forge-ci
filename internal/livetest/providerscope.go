// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"fmt"
	"os"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

const liveProviderEnv = "FORGE_CI_LIVE_PROVIDER"

func liveProviderScope() (provider.ForgeAPI, error) {
	switch value := os.Getenv(liveProviderEnv); value {
	case "", "all":
		return "", nil
	case string(provider.ForgeGitLab), string(provider.ForgeForgejo):
		return provider.ForgeAPI(value), nil
	default:
		return "", fmt.Errorf("%s must be all, gitlab or forgejo: %w", liveProviderEnv, errs.ErrValidation)
	}
}
