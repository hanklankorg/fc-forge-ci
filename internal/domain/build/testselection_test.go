// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build

import (
	"errors"
	"regexp/syntax"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestSelectionDiagnosticsKeepTextAndUsageIdentity(t *testing.T) {
	for _, tc := range []struct{ body, diagnostic string }{
		{`null`, "test selection: null is not a typed selection value"},
		{`{"gate":"policy","Gate":"application"}`, `test selection: duplicate or invalid member "Gate"`},
		{`{"packages":["./..."],"gate":"policy","run":"["}`, "invalid test regexp: error parsing regexp: missing closing ]: `[`"},
	} {
		_, err := ParseGoTestSelection(tc.body)

		want := tc.diagnostic + ": " + errs.ErrUsage.Error()
		if err == nil || err.Error() != want || !errors.Is(err, errs.ErrUsage) {
			t.Fatalf("selection %s: got %v, want %q", tc.body, err, want)
		}

		var syntaxError *syntax.Error
		if errors.As(err, &syntaxError) || errors.Is(err, errInvalidSelectionMember) || errors.Is(err, errNullSelectionValue) {
			t.Fatalf("selection exposed an internal parsing identity: %v", err)
		}
	}
}

func TestSelectionsRejectOptOutsAndExtraArguments(t *testing.T) {
	for _, body := range []string{`null`, `{}`, `{"packages":[]}`, `{"packages":["-race=false"]}`, `{"packages":["../outside"]}`, `{"packages":["./../outside"]}`, `{"packages":["./ok"],"race":false}`, `{"packages":["./ok"],"run":"["}`, `{"packages":["./ok"]} {}`} {
		if _, err := ParseGoTestSelection(body); !errors.Is(err, errs.ErrUsage) {
			t.Errorf("Go accepted %s: %v", body, err)
		}
	}

	for _, body := range []string{`null`, `{}`, `{"runs":[]}`, `{"runs":[{"kind":"all-targets"}]}`, `{"runs":[{"kind":"workspace-bins","package":"nano"}]}`, `{"runs":[{"kind":"package-tests"}]}`, `{"runs":[{"kind":"package-test","package":"nano","name":"--all-targets"}]}`, `{"runs":[{"kind":"workspace-bins","command":"true"}]}`} {
		if _, err := ParseCargoTestSelection(body); !errors.Is(err, errs.ErrUsage) {
			t.Errorf("Cargo accepted %s: %v", body, err)
		}
	}

	if _, err := ParseGoTestSelection(`{"packages":["./internal/...","./cmd/..."],"run":"TestSafe","gate":"policy"}`); err != nil {
		t.Fatal(err)
	}

	for _, body := range []string{
		`{"runs":[{"kind":"package-bin","package":"app","name":"one"}]}`,
		`{"runs":[{"kind":"package-bin","package":"app","name":"one","gate":"optional"}]}`,
		`{"runs":[{"kind":"package-bin","package":"app","name":"one","gate":"policy","gate":"application"}]}`,
		`{"runs":[{"kind":"package-bin","package":"app","name":"one","gate":"policy","Gate":"application"}]}`,
		`{"runs":[{"kind":"package-bin","package":"app","name":"one","gate":"policy"},{"kind":"package-bin","package":"app","name":"one","gate":"application"}]}`,
	} {
		if _, err := ParseCargoTestSelection(body); err == nil {
			t.Fatalf("invalid/ambiguous gate accepted: %s", body)
		}
	}
}

func TestGoSelectionRequiresGateAndRejectsExplicitNull(t *testing.T) {
	for _, body := range []string{
		`{"packages":["./..."]}`,
		`{"packages":["./..."],"gate":"optional"}`,
		`{"packages":["./..."],"gate":null}`,
		`{"packages":["./..."],"gate":"policy","run":null}`,
		`{"packages":["./..."],"gate":"policy","gate":"application"}`,
	} {
		if _, err := ParseGoTestSelection(body); !errors.Is(err, errs.ErrUsage) {
			t.Fatalf("accepted %s: %v", body, err)
		}
	}

	for _, gate := range []string{"policy", "application"} {
		if _, err := ParseGoTestSelection(`{"packages":["./..."],"gate":"` + gate + `"}`); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSelectionDuplicateMembersUseUnicodeFold(t *testing.T) {
	for _, body := range []string{
		`{"runs":[{"kind":"package-test","package":"app","name":"guard","gate":"policy"}],"runſ":[{"kind":"package-bin","package":"app","name":"main","gate":"application"}]}`,
		`{"runs":[],"RUNS":[]}`,
	} {
		if _, err := ParseCargoTestSelection(body); !errors.Is(err, errs.ErrUsage) {
			t.Fatalf("duplicate accepted %s: %v", body, err)
		}
	}
}
