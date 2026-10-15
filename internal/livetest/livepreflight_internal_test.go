// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codefloe.com/itiquette/forge-lab-contract/labcontract"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

func TestReadCAFile_AcceptsCertificatesAndRefusesEverythingElse(t *testing.T) {
	t.Parallel()

	body := independentCAPEM(t)
	write := func(t *testing.T, content []byte, mode os.FileMode) string {
		t.Helper()

		path := filepath.Join(t.TempDir(), "ca.pem")
		if err := os.WriteFile(path, content, mode); err != nil {
			t.Fatal(err)
		}

		return path
	}

	// The CA is public material: a world-readable file is fine.
	got, err := readCAFile(write(t, body, 0o644)) //nolint:gosec // Deliberately public certificate.
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("readCAFile = %q, %v, want the certificate bytes", got, err)
	}

	oversized := write(t, body, 0o600)
	// A sparse extension needs no real write.
	if err := os.Truncate(oversized, maxCABytes+1); err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct{ path, reason string }{
		"missing":           {path: filepath.Join(t.TempDir(), "absent.pem"), reason: "inspect ca_file"},
		"directory":         {path: t.TempDir(), reason: "must be a regular file"},
		"empty":             {path: write(t, nil, 0o600), reason: "must be a regular file"},
		"not_a_certificate": {path: write(t, []byte("not a certificate\n"), 0o600), reason: "PEM CERTIFICATE"},
		"oversized":         {path: oversized, reason: "must be a regular file no larger than"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := readCAFile(tc.path); err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("refusal = %v, want %q", err, tc.reason)
			}
		})
	}
}

// TestValidateLiveProfile_FullMeansFull requires both forges, each with a
// deployed runner and a Fulcio issuer mapping, under the full profile only.
func TestValidateLiveProfile_FullMeansFull(t *testing.T) {
	endpoint := func(kind labcontract.Kind) labcontract.Endpoint {
		return labcontract.Endpoint{Name: string(kind), Kind: kind, Capabilities: []string{labcontract.CapabilityWorkflowRuns}}
	}
	issuer := func(kind labcontract.Kind) labcontract.FulcioIssuer {
		return labcontract.FulcioIssuer{Endpoint: string(kind), OIDCIssuer: "https://" + string(kind) + ".compose.forgelab:8443"}
	}
	complete := func() *labcontract.Contract {
		return &labcontract.Contract{
			Endpoints: []labcontract.Endpoint{endpoint(labcontract.GitLab), endpoint(labcontract.Forgejo)},
			Fulcio:    &labcontract.Fulcio{Issuers: []labcontract.FulcioIssuer{issuer(labcontract.GitLab), issuer(labcontract.Forgejo)}},
		}
	}

	for name, tc := range map[string]struct {
		profile  string
		selected int
		mutate   func(*labcontract.Contract)
		reason   string
	}{
		"unset is focused":         {selected: 1},
		"focused":                  {profile: "focused", selected: 1},
		"full complete":            {profile: "full", selected: 2},
		"unknown profile":          {profile: "partial", selected: 2, reason: liveProfileEnv + " must be full or focused"},
		"full with one forge":      {profile: "full", selected: 1, reason: "requires both gitlab and forgejo"},
		"full without a runner":    {profile: "full", selected: 2, mutate: func(c *labcontract.Contract) { c.Endpoints[1].Capabilities = nil }, reason: "workflow-runs"},
		"full without an issuer":   {profile: "full", selected: 2, mutate: func(c *labcontract.Contract) { c.Fulcio.Issuers = c.Fulcio.Issuers[:1] }, reason: "Fulcio issuer mapping"},
		"full without any fulcio":  {profile: "full", selected: 2, mutate: func(c *labcontract.Contract) { c.Fulcio = nil }, reason: "Fulcio issuer mapping"},
		"full with a missing kind": {profile: "full", selected: 2, mutate: func(c *labcontract.Contract) { c.Endpoints[0].Kind = labcontract.Gitea }, reason: "gitlab: no endpoint of that kind"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(liveProfileEnv, tc.profile)

			lab := complete()
			if tc.mutate != nil {
				tc.mutate(lab)
			}

			err := validateLiveProfile(lab, tc.selected)

			switch {
			case tc.reason == "" && err != nil:
				t.Fatalf("rejected: %v", err)
			case tc.reason != "" && (!errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), tc.reason)):
				t.Fatalf("%v, want a refusal mentioning %q", err, tc.reason)
			}
		})
	}
}

// fixtureNow is inside every credential window the compose fixture declares.
func fixtureNow() time.Time { return time.Date(2026, time.August, 6, 10, 30, 0, 0, time.UTC) }

// withOwnedCA moves the compose fixture's CA path to an owned certificate.
// The GitHub fixture and a null ca_file carry no CA path to move.
func withOwnedCA(t *testing.T, body string) string {
	t.Helper()

	caPath := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caPath, independentCAPEM(t), 0o600); err != nil {
		t.Fatal(err)
	}

	return strings.Replace(body, "/opt/forge-lab/certs/forge-lab-ca.crt", caPath, 1)
}

// arrangePreflight writes the fixture with an owned CA at an explicit path
// and arms the environment CheckLiveInputs reads.
func arrangePreflight(t *testing.T, body string) {
	t.Helper()

	setCurrentContract(t, writeContract(t, withOwnedCA(t, body)))
	t.Setenv(liveProfileEnv, "focused")
}

// requireExplained requires a summary for the complete compose fixture whose
// explanation names the generation and the run identity, credential-free.
func requireExplained(t *testing.T, summary LivePreflightSummary) {
	t.Helper()

	if summary.Selected != 2 || summary.Generation != "run-neutral-fixture" {
		t.Fatalf("summary = %+v", summary)
	}

	for _, want := range []string{"generation run-neutral-fixture\n", "\nforgejo (forgejo)\n", "\ngitlab (gitlab)\n"} {
		if !strings.Contains(summary.Explanation, want) {
			t.Fatalf("explanation lacks %q:\n%s", want, summary.Explanation)
		}
	}

	for _, secret := range []string{"fake-forgejo-token", "fake-gitlab-token", "fake-gitea-token"} {
		if strings.Contains(summary.Explanation, secret) {
			t.Fatalf("explanation carries a credential:\n%s", summary.Explanation)
		}
	}
}

// TestCheckLiveInputs_GuardsWithoutTouchingAProvider runs the preflight over
// owned synthetic inputs: a success reports the selection and explains the
// run, and each refusal names its cause.
func TestCheckLiveInputs_GuardsWithoutTouchingAProvider(t *testing.T) {
	for name, tc := range map[string]struct {
		body   func(*testing.T) string
		env    map[string]string
		reason string
	}{
		"focused accepts":                   {body: composeFixtureBody},
		"full accepts the complete fixture": {body: composeFixtureBody, env: map[string]string{liveProfileEnv: "full"}},
		"unknown profile":                   {body: composeFixtureBody, env: map[string]string{liveProfileEnv: "partial"}, reason: "must be full or focused"},
		"full without an issuer mapping": {body: func(t *testing.T) string {
			t.Helper()

			return withoutGitLabIssuer(t, composeFixtureBody(t))
		}, env: map[string]string{liveProfileEnv: "full"}, reason: "Fulcio issuer mapping"},
		"full without a runner": {body: func(t *testing.T) string {
			t.Helper()

			return withoutWorkflowRuns(t, composeFixtureBody(t))
		}, env: map[string]string{liveProfileEnv: "full"}, reason: "workflow-runs"},
		"null ca_file": {body: func(t *testing.T) string {
			t.Helper()

			return replaceInFixture(t, composeFixtureBody(t), `"ca_file": "/opt/forge-lab/certs/forge-lab-ca.crt"`, `"ca_file": null`)
		}, reason: "non-null ca_file"},
		"missing live capability": {body: func(t *testing.T) string {
			t.Helper()

			return replaceInFixture(t, composeFixtureBody(t), "        \"artifacts\",\n        \"groups\",", "        \"groups\",")
		}, reason: `lacks required live capability "artifacts"`},
		"no forge this tier drives": {body: func(t *testing.T) string {
			t.Helper()

			return fixtureBody(t, "neutral-targets-v4-github.json")
		}, reason: "carries no endpoint this tier drives"},
	} {
		t.Run(name, func(t *testing.T) {
			arrangePreflight(t, tc.body(t))

			for key, value := range tc.env {
				t.Setenv(key, value)
			}

			summary, err := CheckLiveInputs(fixtureNow())

			switch {
			case tc.reason == "":
				if err != nil {
					t.Fatalf("CheckLiveInputs = %+v, %v", summary, err)
				}

				requireExplained(t, summary)
			case err == nil || !strings.Contains(err.Error(), tc.reason):
				t.Fatalf("CheckLiveInputs = %+v, %v; want a refusal mentioning %q", summary, err, tc.reason)
			}
		})
	}

	t.Run("default path when the override is unset", func(t *testing.T) {
		// The common case: no variable, the contract where the lab wrote it
		// for account rc under the state directory.
		installDefaultContract(t, withOwnedCA(t, composeFixtureBody(t)))
		t.Setenv(liveProfileEnv, "full")

		summary, err := CheckLiveInputs(fixtureNow())
		if err != nil {
			t.Fatal(err)
		}

		requireExplained(t, summary)
	})

	t.Run("no contract at either location says where it looked", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		t.Setenv(labcontract.PathEnv, "")

		want, err := labcontract.DefaultPath(labAccount)
		if err != nil {
			t.Fatal(err)
		}

		_, err = CheckLiveInputs(fixtureNow())
		if err == nil || !strings.Contains(err.Error(), "no live-target contract at "+want) || !strings.Contains(err.Error(), "`just lab`") {
			t.Fatalf("missing contract = %v, want the default path and how to get one", err)
		}
	})

	t.Run("invalid ca_file is refused after every guard", func(t *testing.T) {
		// A contract whose CA path names something other than certificates is
		// refused after every guard passed, so a broken CA cannot be reported
		// as a guard failure.
		arrangePreflight(t, composeFixtureBody(t))

		lab, err := labcontract.Load(os.Getenv(labcontract.PathEnv))
		if err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(*lab.CAFile, []byte("not a certificate\n"), 0o600); err != nil { //nolint:gosec // The CA path arrangePreflight placed under this test's temporary directory.
			t.Fatal(err)
		}

		if _, err := CheckLiveInputs(fixtureNow()); err == nil || !strings.Contains(err.Error(), "PEM CERTIFICATE") {
			t.Fatalf("invalid CA = %v", err)
		}
	})

	t.Run("stale credential is a guard failure naming the forge", func(t *testing.T) {
		arrangePreflight(t, composeFixtureBody(t))

		_, err := CheckLiveInputs(fixtureNow().Add(48 * time.Hour))
		if err == nil || !strings.Contains(err.Error(), string(provider.ForgeForgejo)+" destructive guard") || !strings.Contains(err.Error(), "older than 24h0m0s") {
			t.Fatalf("stale credential = %v", err)
		}
	})
}
