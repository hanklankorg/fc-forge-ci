// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// Each account is out of the other's reach: a demo never acts as the trial
// account, whose repositories trials reset, and a trial never acts as the demo
// account, whose repositories are kept.
func TestRequireAccountMode_KeepsTrialsAndDemosApart(t *testing.T) {
	for name, test := range map[string]struct {
		owner, demo string
		refused     bool
	}{
		"trial as the trial account": {owner: labAccount},
		"demo as the demo account":   {owner: demoAccount, demo: "wallet-provider"},
		"trial as the demo account":  {owner: demoAccount, refused: true},
		"demo as the trial account":  {owner: labAccount, demo: "wallet-provider", refused: true},
		"demo as another account":    {owner: "releaseci", demo: "wallet-provider", refused: true},
	} {
		t.Run(name, func(t *testing.T) {
			err := requireAccountMode(test.owner, test.demo)
			if test.refused != (err != nil) || (err != nil && !errors.Is(err, errs.ErrValidation)) {
				t.Fatalf("requireAccountMode(%q, %q) = %v, want refused %v", test.owner, test.demo, err, test.refused)
			}
		})
	}
}

// The demo name selects the account whose contract is read, and a name that
// could not be a demo repository's is refused before any contract is.
func TestLiveAccount_FollowsDemoMode(t *testing.T) {
	for name, test := range map[string]struct {
		demo, want string
		refused    bool
	}{
		"no demo":             {want: labAccount},
		"demo":                {demo: "wallet-provider", want: demoAccount},
		"uppercase":           {demo: "Wallet", refused: true},
		"path":                {demo: "a/b", refused: true},
		"trailing hyphen":     {demo: "wallet-", refused: true},
		"one character":       {demo: "w", refused: true},
		"a layer's name":      {demo: "wallet-forge-ci", refused: true},
		"a helper's name":     {demo: "wallet-release-engine", refused: true},
		"over the length cap": {demo: "a1234567890123456789012345678901234567890", refused: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(demoEnv, test.demo)

			got, err := liveAccount()
			if test.refused {
				if !errors.Is(err, errs.ErrValidation) {
					t.Fatalf("liveAccount() = %q, %v, want a refusal", got, err)
				}

				return
			}

			if err != nil || got != test.want {
				t.Fatalf("liveAccount() = %q, %v, want %q", got, err, test.want)
			}
		})
	}
}

func TestDemoLayerRepo_NamesTheDemosOwnLayer(t *testing.T) {
	if got := DemoLayerRepo("wallet-provider"); got != "demo-wallet-provider-forge-ci" {
		t.Fatalf("DemoLayerRepo() = %q", got)
	}

	if got := DemoHelperRepo("wallet-provider", "engine"); got != "demo-wallet-provider-release-engine" {
		t.Fatalf("DemoHelperRepo() = %q", got)
	}
}

// Nothing deletes or resets in the demo account, whichever helper a scenario
// reaches a deletion through.
func TestRefuseDemoDeletion_HoldsForTheDemoAccountAlone(t *testing.T) {
	if err := refuseDemoDeletion(Target{Owner: demoAccount}); !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("refuseDemoDeletion(demo) = %v, want a refusal", err)
	}

	if err := refuseDemoDeletion(Target{Owner: labAccount}); err != nil {
		t.Fatalf("refuseDemoDeletion(%s) = %v", labAccount, err)
	}

	if _, err := scratchRepos(Target{Owner: demoAccount, accepted: true}); !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("scratchRepos(demo) = %v, want the scratch lifecycle refused", err)
	}
}

// A run writes only in its own namespace: fci- for a trial, demo- in the demo
// account, and never the bare prefix or the other account's names.
func TestInRunNamespace_FollowsTheAccount(t *testing.T) {
	trial, demo := Target{Owner: labAccount}, Target{Owner: demoAccount}

	for name, test := range map[string]struct {
		target Target
		repo   string
		inside bool
	}{
		"trial repository":       {target: trial, repo: "fci-consumer-trial", inside: true},
		"demo repository":        {target: demo, repo: "demo-hello-release-job-image", inside: true},
		"demo name in a trial":   {target: trial, repo: "demo-hello", inside: false},
		"trial name in the demo": {target: demo, repo: "fci-consumer-trial", inside: false},
		"bare demo prefix":       {target: demo, repo: "demo-", inside: false},
	} {
		if got := inRunNamespace(test.target, test.repo); got != test.inside {
			t.Errorf("%s: inRunNamespace(%s, %q) = %v", name, test.target.Owner, test.repo, got)
		}
	}
}
