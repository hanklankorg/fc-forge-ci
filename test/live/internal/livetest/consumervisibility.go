// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"fmt"
	"os"
	"slices"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

const (
	consumerVisibilityEnv = "FORGE_CI_LIVE_CONSUMER_VISIBILITY"
	visibilityPublic      = "public"
	visibilityPrivate     = "private"
)

// What a variable or a setting holds when it is a switch turned on, and the
// Cosign transparency setting that names no log.
const (
	variableTrue           = "true"
	cosignTransparencyNone = "none"
)

// ConsumerVisibility names one shared public/private scenario case.
type ConsumerVisibility struct {
	Name    string
	Private bool
}

// ConsumerVisibilities supplies both cases by default. An explicit selector
// narrows a run, and a run that asks for private narrows every repository
// holding the code under test (ScenarioPrivate). Call before creating any
// trial resources.
func ConsumerVisibilities(tb TB) []ConsumerVisibility {
	tb.Helper()

	selected, err := parseConsumerVisibilities(os.Getenv(consumerVisibilityEnv))
	if err != nil {
		tb.Fatalf("livetest: %v", err)
	}

	// A demo is public, so anyone it is shown to can follow it.
	if DemoName() != "" {
		if len(selected) != 2 && selected[0].Private {
			tb.Fatalf("livetest: a demo is public; leave %s unset or public", consumerVisibilityEnv)
		}

		return []ConsumerVisibility{{Name: visibilityPublic}}
	}

	return selected
}

// ScenarioPrivate reports whether the run asks for private repositories only.
// Every repository holding the code under test (consumer, scenario, policy
// and publication repositories) is then private. The repositories that stand
// in for the public forge-ci distribution or lab infrastructure stay public
// with a reason (NewPublicScratchRepo). Otherwise a scenario of one
// visibility keeps the public repositories it has always had.
func ScenarioPrivate(tb TB) bool {
	tb.Helper()

	selected := ConsumerVisibilities(tb)

	return len(selected) == 1 && selected[0].Private
}

// RequireVisibility reports the scenario not covered when the run excludes
// the visibility its subject is: a scenario that proves a private repository's
// refusal of anonymous reads, say, has nothing to prove in a public-only run.
// reason says why the subject is that visibility.
func RequireVisibility(tb TB, private bool, reason string) {
	tb.Helper()

	if reason == "" {
		tb.Fatalf("livetest: name why the scenario's subject is one visibility")
	}

	if !slices.ContainsFunc(ConsumerVisibilities(tb), func(c ConsumerVisibility) bool { return c.Private == private }) {
		NotCoveredf(tb, "%s; the run asks for %s repositories only (%s)", reason, visibilityName(!private), consumerVisibilityEnv)
	}
}

// The reasons a scenario's repository is public in every run: it stands in
// for the public forge-ci distribution or lab infrastructure, which something
// reads without credentials.
const (
	// ReadLayerAnonymously: the product's jobs fetch the engine layer's
	// source and its binary package without credentials (docs/gitlab.md,
	// docs/forgejo.md). A private self-hosted distribution would need that
	// bootstrap to authenticate.
	ReadLayerAnonymously = "it serves the engine layer, whose source and binary package the product's jobs fetch without credentials"
	// ReadJobImageAnonymously: a runner pulls the job images it serves
	// without the job's credentials.
	ReadJobImageAnonymously = "it serves a job image, which the runner pulls without the job's credentials"
	// ReadProbeAssetAnonymously: an in-runner probe downloads the candidate
	// binary from its release without credentials (ProbePrelude).
	ReadProbeAssetAnonymously = "an in-runner probe downloads the candidate binary from its release without credentials"
)

// requirePublicReason fails the test, before repo is created, when a public
// repository names no reason.
func requirePublicReason(tb TB, repo, reason string) {
	tb.Helper()

	if reason == "" {
		tb.Fatalf("livetest: name why %s must be public", repo)
	}
}

// requireRunVisibility fails the test, before repo is created, when the run
// does not ask for the visibility a scenario passed.
func requireRunVisibility(tb TB, repo string, private bool) {
	tb.Helper()

	if !slices.ContainsFunc(ConsumerVisibilities(tb), func(c ConsumerVisibility) bool { return c.Private == private }) {
		tb.Fatalf("livetest: %s would be %s, which the run (%s) does not ask for; pass a ConsumerVisibility or ScenarioPrivate, or admit the subject's visibility with RequireVisibility",
			repo, visibilityName(private), consumerVisibilityEnv)
	}
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
