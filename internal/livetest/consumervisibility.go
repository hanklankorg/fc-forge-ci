// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"fmt"
	"os"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

const (
	consumerVisibilityEnv = "FORGE_CI_LIVE_CONSUMER_VISIBILITY"
	visibilityPublic      = "public"
	visibilityPrivate     = "private"
)

// ConsumerVisibility names one shared public/private scenario case.
type ConsumerVisibility struct {
	Name    string
	Private bool
}

// ConsumerVisibilities supplies both cases by default. An explicit selector
// narrows a development run; helpers/policy/images retain their own contracts.
// Call before creating any trial resources.
func ConsumerVisibilities(tb TB) []ConsumerVisibility {
	tb.Helper()

	selected, err := parseConsumerVisibilities(os.Getenv(consumerVisibilityEnv))
	if err != nil {
		tb.Fatalf("livetest: %v", err)
	}

	return selected
}

func parseConsumerVisibilities(value string) ([]ConsumerVisibility, error) {
	public, private := ConsumerVisibility{Name: visibilityPublic}, ConsumerVisibility{Name: visibilityPrivate, Private: true}
	switch value {
	case "", "both":
		return []ConsumerVisibility{public, private}, nil
	case visibilityPublic:
		return []ConsumerVisibility{public}, nil
	case visibilityPrivate:
		return []ConsumerVisibility{private}, nil
	default:
		return nil, fmt.Errorf("%s must be both, public or private: %w", consumerVisibilityEnv, errs.ErrValidation)
	}
}

func visibilityName(private bool) string {
	if private {
		return visibilityPrivate
	}

	return visibilityPublic
}

type repositoryVisibilityState struct {
	ID         int64  `json:"id"`
	Private    *bool  `json:"private"`
	Visibility string `json:"visibility"`
}

func (state repositoryVisibilityState) matches(forge provider.ForgeAPI, private bool) bool {
	if state.ID <= 0 {
		return false
	}

	switch forge {
	case provider.ForgeForgejo:
		return state.Private != nil && *state.Private == private
	case provider.ForgeGitLab:
		return state.Visibility == visibilityName(private)
	case provider.ForgeGitHub, provider.ForgeLocal:
		return false
	}

	return false
}
