// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"strings"
	"testing"

	"codefloe.com/itiquette/forge-lab-contract/labcontract"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

func TestLiveProviderScopeRejectsUnknownSelection(t *testing.T) {
	for _, value := range []string{"", "all", "gitlab", "forgejo", "github", "../forgejo", "gitlab,forgejo"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(liveProviderEnv, value)
			scope, err := liveProviderScope()
			valid := value == "" || value == "all" || value == "gitlab" || value == "forgejo"
			if valid && err != nil {
				t.Fatal(err)
			}
			if !valid && !errors.Is(err, errs.ErrValidation) {
				t.Fatalf("invalid provider selection accepted: %q, %v", scope, err)
			}
		})
	}
}

func TestLiveProviderScopeLimitsExecutionAndCannotClaimFull(t *testing.T) {
	for _, scope := range []provider.ForgeAPI{provider.ForgeGitLab, provider.ForgeForgejo} {
		t.Run(string(scope), func(t *testing.T) {
			arrangePreflight(t, composeFixtureBody(t))
			t.Setenv(liveProviderEnv, string(scope))
			summary, err := CheckLiveInputs(fixtureNow())
			if err != nil || summary.Selected != 1 || !strings.Contains(summary.Explanation, "execution_provider="+string(scope)) {
				t.Fatalf("scoped preflight: %+v, %v", summary, err)
			}
			for _, forge := range liveForges {
				if Selected(t, forge) != (scope == forge) {
					t.Fatalf("provider selection escaped scope: %s", forge)
				}
				if forge != scope {
					if Requires(t, forge, NeedsInRunner) {
						t.Fatal("excluded provider passed runtime selection")
					}
					if _, err := AcceptDemoTarget(forge, fixtureNow()); !errors.Is(err, errs.ErrValidation) {
						t.Fatalf("excluded demo target could be armed: %v", err)
					}
				}
			}
			t.Setenv(liveProfileEnv, profileFull)
			if _, err := CheckLiveInputs(fixtureNow()); !errors.Is(err, errs.ErrValidation) {
				t.Fatalf("one-provider run claimed full coverage: %v", err)
			}
		})
	}
}

func TestLiveProviderScopeChoosesOnlyItsRunnerArchitecture(t *testing.T) {
	lab := &labcontract.Contract{Endpoints: []labcontract.Endpoint{
		{Name: "gitlab", Kind: labcontract.GitLab, Capabilities: []string{capabilityWorkflowRuns}, Runner: &labcontract.Runner{Arch: "amd64"}},
		{Name: "forgejo", Kind: labcontract.Forgejo, Capabilities: []string{capabilityWorkflowRuns}, Runner: &labcontract.Runner{Arch: "arm64"}},
	}}
	for _, tc := range []struct{ scope, arch string }{{"gitlab", "amd64"}, {"forgejo", "arm64"}} {
		t.Setenv(liveProviderEnv, tc.scope)
		arch, err := labRunnerArch(lab)
		if err != nil || arch != tc.arch {
			t.Fatalf("runner architecture for %s: %s, %v", tc.scope, arch, err)
		}
	}
	t.Setenv(liveProviderEnv, "all")
	if _, err := labRunnerArch(lab); !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("mixed runner architecture was not refused: %v", err)
	}
}
