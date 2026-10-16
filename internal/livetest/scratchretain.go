// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

type retainedScratch struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
	Archived *bool  `json:"archived"`
}

type retainedScratchRequest func(string, any, any) (int, error)

func retainScratchForInspection(tb TB, target Target, repo string) {
	tb.Helper()

	if target.Forge != provider.ForgeForgejo {
		tb.Logf("livetest: keeping %s/%s for inspection (FORGE_CI_LIVE_KEEP_SCRATCH=1)", target.Owner, repo)

		return
	}

	ctx, cancel := cleanupContext(timeoutScratch)
	defer cancel()

	endpoint := actionsURL(target, repo, "")
	request := func(method string, body, into any) (int, error) {
		return decode(ctx, target, method, endpoint, body, into, http.StatusOK, http.StatusNotFound)
	}

	present, err := archiveRetainedScratch(target, repo, request)
	if err != nil {
		tb.Errorf("livetest: could not archive retained %s/%s; schedules may still run; use the guarded lab cleanup: %v", target.Owner, repo, err)

		return
	}

	if present {
		tb.Logf("livetest: retained %s/%s archived for inspection; future scheduled workflows are stopped", target.Owner, repo)
	} else {
		tb.Logf("livetest: %s/%s is absent; nothing to retain", target.Owner, repo)
	}
}

// Archiving stops Forgejo's scheduler without disabling the Actions unit, which
// would hide the run history this opt-in retains. This changes no workflow or
// artifact, and uses the same repository coordinate for admission and read-back.
func archiveRetainedScratch(target Target, repo string, request retainedScratchRequest) (bool, error) {
	if !validRetainedScratch(target, repo) {
		return false, fmt.Errorf("retained repository is outside the accepted Forgejo scratch namespace: %w", errs.ErrValidation)
	}

	before, present, err := readRetainedScratch(RepoSlug(target, repo), request)
	if err != nil || !present {
		return false, err
	}

	if *before.Archived {
		return true, nil
	}

	status, err := request(http.MethodPatch, map[string]bool{"archived": true}, nil)
	if err != nil {
		return false, err
	}

	if status != http.StatusOK {
		return false, fmt.Errorf("retained repository archive was not accepted (HTTP %d): %w", status, errs.ErrValidation)
	}

	after, present, err := readRetainedScratch(before.FullName, request)
	if err != nil {
		return false, err
	}

	if !present || !after.archivedAs(before) {
		return false, fmt.Errorf("retained repository archive was not verified: %w", errs.ErrValidation)
	}

	return true, nil
}

func validRetainedScratch(target Target, repo string) bool {
	return target.accepted && target.Forge == provider.ForgeForgejo && strings.HasPrefix(repo, ResourcePrefix) &&
		repo != ResourcePrefix && url.PathEscape(repo) == repo
}

func readRetainedScratch(slug string, request retainedScratchRequest) (retainedScratch, bool, error) {
	var state retainedScratch

	status, err := request(http.MethodGet, nil, &state)
	if err != nil {
		return state, false, err
	}

	if status == http.StatusNotFound {
		return state, false, nil
	}

	if status != http.StatusOK || state.ID <= 0 || state.FullName != slug || state.Archived == nil {
		return state, false, fmt.Errorf("cannot establish retained repository identity: %w", errs.ErrValidation)
	}

	return state, true, nil
}

func (state retainedScratch) archivedAs(before retainedScratch) bool {
	return state.ID == before.ID && state.FullName == before.FullName && state.Archived != nil && *state.Archived
}
