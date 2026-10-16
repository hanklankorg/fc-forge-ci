// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

// These run in the ordinary suite, with no build tag and no forge. That is
// deliberate: the guard is the thing standing between a typo and a destroyed
// server, so its refusals must be proven on every commit, not only on the rare
// occasion someone has a lab running.
//
// It touches unexported state (the pure acceptTarget guard and the accepted
// flag), so it is a same-package test in an _internal_test.go file per the
// repository's carve-out convention.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codefloe.com/itiquette/forge-lab-contract/labcontract"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// errTransportBlocked is what the blocked transport returns; tests match it by
// identity rather than by message.
var errTransportBlocked = errors.New("test transport blocked")

// guardNow is inside every credential window the compose fixture declares.
func guardNow() time.Time { return time.Date(2026, time.August, 6, 10, 30, 0, 0, time.UTC) }

// TestAcceptTarget_RefusesEachUnsafeRunForItsOwnReason changes one input of
// an acceptable run at a time -- the contract's bytes, the forge or the clock
// -- and requires the rule that guards that input to be the one that objects.
//
// The stakes are why the reason matters: acceptTarget decides whether a run
// is allowed to mutate a real forge. A mutation rejected by some other rule
// leaves the guard it was written for free to rot unnoticed.
func TestAcceptTarget_RefusesEachUnsafeRunForItsOwnReason(t *testing.T) {
	t.Parallel()

	type run struct {
		body  string
		forge provider.ForgeAPI
		now   time.Time
	}

	tests := []struct {
		name   string
		mutate func(*testing.T, *run)
		// sentinel and reason together identify the rule that must refuse the
		// run. Every refusal also wraps errs.ErrValidation except the clean
		// skip, ErrNoEndpoint, which must not.
		sentinel error
		reason   string
	}{
		{name: "complete_forgejo_run"},
		{name: "complete_gitlab_run", mutate: func(_ *testing.T, r *run) { r.forge = provider.ForgeGitLab }},
		{name: "no_fulcio_is_fine", mutate: func(t *testing.T, r *run) {
			t.Helper()

			r.body = replaceInFixture(t, r.body, `"issuers": [
      {
        "endpoint": "forgejo",
        "oidc_issuer": "https://forgejo.compose.forgelab:8443/api/actions"
      },`, `"issuers": [`)
		}},
		{name: "github_is_not_a_target", sentinel: errs.ErrValidation, reason: "is not a live-forge target", mutate: func(_ *testing.T, r *run) { r.forge = provider.ForgeGitHub }},
		{name: "unknown_platform", sentinel: errs.ErrValidation, reason: "is not a live-forge target", mutate: func(_ *testing.T, r *run) { r.forge = "bitbucket" }},
		{name: "absent_forge_is_a_skip", sentinel: labcontract.ErrNoEndpoint, reason: "no endpoint of that kind", mutate: func(t *testing.T, r *run) {
			t.Helper()

			r.body = fixtureBody(t, "neutral-targets-v4-github.json")
			r.forge = provider.ForgeGitLab
		}},
		{name: "missing_live_capability", sentinel: errs.ErrValidation, reason: `lacks required live capability "artifacts"`, mutate: func(t *testing.T, r *run) {
			t.Helper()

			r.body = replaceInFixture(t, r.body, "        \"artifacts\",\n        \"organizations\",", "        \"organizations\",")
		}},
		{name: "host_outside_disposable_list", sentinel: labcontract.ErrHost, reason: `"forgejo.codeberg.org:8443": host is not disposable`, mutate: func(_ *testing.T, r *run) {
			r.body = strings.ReplaceAll(r.body, "forgejo.compose.forgelab", "forgejo.codeberg.org")
		}},
		{name: "registry_outside_disposable_list", sentinel: labcontract.ErrHost, reason: `OCI registry: "registry.attacker.example": host is not disposable`, mutate: func(t *testing.T, r *run) {
			t.Helper()

			r.body = replaceInFixture(t, r.body, `"base_url": "https://forgejo.compose.forgelab:8443"`, `"base_url": "https://registry.attacker.example"`)
		}},
		{name: "fulcio_outside_disposable_list", sentinel: labcontract.ErrHost, reason: `fulcio: "fulcio.attacker.example": host is not disposable`, mutate: func(t *testing.T, r *run) {
			t.Helper()

			r.body = replaceInFixture(t, r.body, `"base_url": "https://fulcio.compose.forgelab:8443"`, `"base_url": "https://fulcio.attacker.example"`)
		}},
		{name: "non_expiring_credential_older_than_a_day", sentinel: labcontract.ErrLifetime, reason: "forgejo credential: non-expiring credential created 2026-08-06T10:00:00Z is older than 24h0m0s", mutate: func(_ *testing.T, r *run) {
			r.now = r.now.Add(48 * time.Hour)
		}},
		{name: "credential_created_in_the_future", sentinel: labcontract.ErrLifetime, reason: "is in the future", mutate: func(_ *testing.T, r *run) {
			r.now = r.now.Add(-time.Hour)
		}},
		{name: "expiring_credential_already_expired", sentinel: labcontract.ErrLifetime, reason: "credential expires 2026-09-05", mutate: func(_ *testing.T, r *run) {
			r.forge = provider.ForgeGitLab
			r.now = r.now.Add(30 * 24 * time.Hour)
		}},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			current := run{body: composeFixtureBody(t), forge: provider.ForgeForgejo, now: guardNow()}
			if testCase.mutate != nil {
				testCase.mutate(t, &current)
			}

			target, err := acceptTarget(loadFixture(t, current.body), current.forge, current.now)

			if testCase.reason == "" {
				if err != nil {
					t.Fatalf("acceptTarget() refused a valid run: %v", err)
				}

				if !target.accepted || target.Forge != current.forge || target.Owner != "fixture-user" {
					t.Fatalf("acceptTarget() = %+v, want an armed target for %s", target, current.forge)
				}

				return
			}

			if target.accepted {
				t.Fatal("acceptTarget() armed a target it refused")
			}

			if !errors.Is(err, testCase.sentinel) {
				t.Fatalf("acceptTarget() = %v, want %v", err, testCase.sentinel)
			}

			if wantValidation := !errors.Is(testCase.sentinel, labcontract.ErrNoEndpoint); errors.Is(err, errs.ErrValidation) != wantValidation {
				t.Fatalf("acceptTarget() = %v; classified as this tier's validation failure = %t, want %t", err, !wantValidation, wantValidation)
			}

			if !strings.Contains(err.Error(), testCase.reason) {
				t.Fatalf("acceptTarget() refused the run, but not for the reason this row is about:\n got: %v\nwant it to mention: %q",
					err, testCase.reason)
			}
		})
	}
}

func TestRegistryHost_UsesDeclaredOrigin(t *testing.T) {
	t.Parallel()

	target := Target{
		Forge:          provider.ForgeGitLab,
		Host:           "gitlab.compose.forgelab:8443",
		RegistryOrigin: "https://registry.gitlab.compose.forgelab:8443",
	}

	host, err := RegistryHost(target)
	if err != nil {
		t.Fatal(err)
	}

	if host != "registry.gitlab.compose.forgelab:8443" {
		t.Fatalf("RegistryHost() = %q", host)
	}

	target.RegistryOrigin = ""
	if _, err := RegistryHost(target); err == nil {
		t.Fatal("RegistryHost derived an undeclared registry")
	}
}

func TestRequireAccepted_RefusesUnguardedTarget(t *testing.T) {
	t.Parallel()

	// A Target built by hand has never passed the guard, so no helper may act
	// through it however complete it looks.
	recorder := &fatalRecorder{}
	downstream := false

	defer func() {
		failure, ok := recover().(acceptanceFailure)
		if !ok || string(failure) != "livetest: refusing to act through a target the destructive guard never accepted" {
			t.Errorf("refusal = %q, want nonreturning Fatalf acceptance refusal", failure)
		}

		if downstream || recorder.failed || recorder.skipped {
			t.Errorf("guard returned or used Errorf/Skipf: downstream=%t recorder=%+v", downstream, recorder)
		}
	}()

	requireAccepted(stopOnFatalTB{TB: recorder}, Target{
		Forge: provider.ForgeForgejo,
		Host:  "forgejo.compose.forgelab:8443",
		Owner: "garga",
		Token: "token",
	})

	downstream = true
}

func TestDeleteScratchRepo_RefusesForeignNamespace(t *testing.T) {
	t.Parallel()

	// An accepted target, so the refusal is the lifecycle's own and not the
	// guard's. Without a CA: the fixture names one this machine does not have,
	// and the client is built (never used) before the name is judged.
	body := replaceInFixture(t, composeFixtureBody(t), `"ca_file": "/opt/forge-lab/certs/forge-lab-ca.crt"`, `"ca_file": null`)

	target, err := acceptTarget(loadFixture(t, body), provider.ForgeForgejo, guardNow())
	if err != nil {
		t.Fatal(err)
	}

	// cl- belongs to git-provider-clean. Even armed, this suite must not reach
	// outside the namespace it declared; the module's prefix rule is what says
	// so, and it says so before anything is sent.
	if err := DeleteScratchRepo(t.Context(), target, "cl-runs-mixed"); !errors.Is(err, labcontract.ErrOutsidePrefix) {
		t.Fatalf("namespace refusal = %v, want labcontract.ErrOutsidePrefix", err)
	}
}

// These entrypoints must refuse before even listing remote resources. A blocked
// dialer makes a missing guard observable without ever opening a socket.
func TestScratchMutators_RejectUnacceptedTargetsBeforeTransport(t *testing.T) {
	original := http.DefaultTransport

	var calls atomic.Int32

	http.DefaultTransport = &http.Transport{
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			calls.Add(1)

			return nil, errTransportBlocked
		},
	}

	t.Cleanup(func() { http.DefaultTransport = original })

	for _, forge := range []provider.ForgeAPI{provider.ForgeForgejo, provider.ForgeGitLab} {
		for _, operation := range []string{"delete", "unique"} {
			t.Run(string(forge)+"/"+operation, func(t *testing.T) {
				calls.Store(0)

				// A lab host, so acceptance is the only thing that can refuse.
				target := Target{
					Forge: forge, Host: "forgejo.compose.forgelab", Owner: "test-owner",
					Token: "synthetic-test-token",
				}

				defer func() {
					if got := calls.Load(); got != 0 {
						t.Errorf("unaccepted target reached transport %d time(s)", got)
					}
				}()

				if operation == "delete" {
					ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
					defer cancel()

					err := DeleteScratchRepo(ctx, target, "fci-test")
					if !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), "unaccepted") {
						t.Fatalf("unaccepted cleanup = %v, want acceptance-specific ErrValidation", err)
					}

					return
				}

				defer func() {
					failure, ok := recover().(acceptanceFailure)
					if !ok || !strings.Contains(string(failure), "never accepted") {
						t.Errorf("unique scratch refusal = %q, want fatal acceptance guard", failure)
					}
				}()

				NewScratchRepoUnique(stopOnFatalTB{TB: t}, target, "test")
			})
		}
	}
}

type acceptanceFailure string

type stopOnFatalTB struct{ TB }

func (tb stopOnFatalTB) Fatalf(format string, args ...any) {
	panic(acceptanceFailure(fmt.Sprintf(format, args...)))
}

// fatalRecorder is the minimum TB that records a Fatalf without stopping the
// test, so a refusal can be asserted rather than crashing the run.
type fatalRecorder struct {
	failed  bool
	fatal   bool
	skipped bool
}

func (f *fatalRecorder) Helper()                  {}
func (f *fatalRecorder) Context() context.Context { return context.Background() }
func (f *fatalRecorder) Logf(string, ...any)      {}
func (f *fatalRecorder) Skipf(string, ...any)     { f.failed, f.skipped = true, true }
func (f *fatalRecorder) Fatalf(string, ...any)    { f.failed, f.fatal = true, true }
func (f *fatalRecorder) Errorf(string, ...any)    { f.failed = true }
func (f *fatalRecorder) Cleanup(func())           {}
