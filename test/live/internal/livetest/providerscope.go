// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"fmt"
	"os"
	"regexp"

	"codefloe.com/itiquette/forge-lab-contract/labcontract"

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

// labAccountName is a lab account the launcher can name a lock file after.
var labAccountName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,38}$`)

// LabProviderAccount is the lab account a provider-scoped run acts as on its
// provider, for the launcher: every scratch repository and package of a run
// sits under its account, so two runs on one provider exclude each other
// only when they share it.
func LabProviderAccount() (string, error) {
	scope, err := liveProviderScope()
	if err != nil {
		return "", err
	}

	if scope == "" {
		return "", fmt.Errorf("%s names no single provider: %w", liveProviderEnv, errs.ErrValidation)
	}

	lab, err := currentLabContract()
	if err != nil {
		return "", err
	}

	endpoint, err := lab.Endpoint(labcontract.Kind(scope))
	if err != nil {
		return "", err
	}

	account := endpoint.Owner()
	if !labAccountName.MatchString(account) {
		return "", fmt.Errorf("the %s contract's account %q cannot name a lock: %w", scope, account, errs.ErrValidation)
	}

	return account, nil
}
