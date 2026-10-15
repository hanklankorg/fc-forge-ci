// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

// The contract's decoder, file policy, host list, lifetime windows and run
// identity are the shared forge-lab-contract module's, tested there against
// its corpus. What is tested here is this tier's use of it: the process cache,
// the projection of an endpoint onto a Target, and the capability rule.

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"codefloe.com/itiquette/forge-lab-contract/labcontract"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

func writeContract(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "contract.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// setCurrentContract points the process at path through the module's
// override variable and forgets the cached first load afterwards, so one
// test's contract cannot be another's mutation.
func setCurrentContract(t *testing.T, path string) {
	t.Helper()

	t.Setenv(labcontract.PathEnv, path)
	forgetContract(t, path)
}

// forgetContract drops path from the process cache when the test ends.
func forgetContract(t *testing.T, path string) {
	t.Helper()

	t.Cleanup(func() {
		activeContracts.Lock()
		defer activeContracts.Unlock()

		delete(activeContracts.byPath, path)
	})
}

// installDefaultContract writes body where the lab writes the rc account's
// contract under an owned state directory, unsets the override, and returns
// that path.
func installDefaultContract(t *testing.T, body string) string {
	t.Helper()

	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(labcontract.PathEnv, "")

	path, err := labcontract.DefaultPath(labAccount)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	forgetContract(t, path)

	return path
}

func fixtureBody(t *testing.T, name string) string {
	t.Helper()

	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}

	return string(body)
}

func composeFixtureBody(t *testing.T) string {
	t.Helper()

	return fixtureBody(t, "neutral-targets-v4-compose.json")
}

// replaceInFixture replaces the first occurrence of from, failing the test when
// the fixture no longer contains it: a stale anchor leaves the valid fixture in
// place, which turns a refusal case into a failure for the wrong reason.
func replaceInFixture(t *testing.T, body, from, to string) string {
	t.Helper()

	if !strings.Contains(body, from) {
		t.Fatalf("fixture no longer contains %q", from)
	}

	return strings.Replace(body, from, to, 1)
}

// loadFixture decodes one of this repository's accepted fixtures through the
// module, so a projection test starts from what a real run would hold.
func loadFixture(t *testing.T, body string) *labcontract.Contract {
	t.Helper()

	contract, err := labcontract.Load(writeContract(t, body))
	if err != nil {
		t.Fatalf("the module rejected this repository's fixture: %v", err)
	}

	return contract
}

func TestAcceptedFixtures_AreReadByTheModule(t *testing.T) {
	t.Parallel()

	for name, wantNulls := range map[string]bool{"neutral-targets-v4-compose.json": false, "neutral-targets-v4-github.json": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			contract := loadFixture(t, fixtureBody(t, name))
			if contract.Version != labcontract.Version || (contract.CAFile == nil) != wantNulls || (contract.Fulcio == nil) != wantNulls {
				t.Fatalf("projection = %+v, want ca_file and fulcio null=%t", contract, wantNulls)
			}
		})
	}
}

func TestCurrentLabContract_ReturnedValuesAreIndependent(t *testing.T) {
	body := composeFixtureBody(t)
	want := loadFixture(t, body)

	setCurrentContract(t, writeContract(t, body))

	// Exercise both the first return and cache hits, including concurrent callers.
	mutate := func() {
		got, readErr := currentLabContract()
		if readErr != nil {
			t.Error(readErr)

			return
		}

		if !reflect.DeepEqual(got, want) {
			t.Error("returned contract differs from validated fixture")

			return
		}

		got.Endpoints[0].Name = "changed"
		got.Endpoints[0].Credential.Token = "changed"
		*got.Endpoints[0].Credential.ProviderID = "999"
		*got.Endpoints[2].Credential.ExpiresAt = "changed"
		got.Endpoints[0].Capabilities[0] = "changed"
		*got.Endpoints[0].GitBaseURL = "changed"
		got.Endpoints[0].OCIRegistry.BaseURL = "changed"
		*got.CAFile = "changed"
		got.Fulcio.Issuers[0].OIDCIssuer = "changed"
		got.Fulcio.BaseURL = "changed"
	}
	mutate()
	mutate()

	var readers sync.WaitGroup
	for range 4 {
		readers.Go(mutate)
	}

	readers.Wait()

	got, err := currentLabContract()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("caller mutation changed cached contract: err=%v", err)
	}
}

func TestCurrentLabContract_DetectsMutationAfterFirstLoad(t *testing.T) {
	body := composeFixtureBody(t)
	path := writeContract(t, body)
	setCurrentContract(t, path)

	if _, err := currentLabContract(); err != nil {
		t.Fatal(err)
	}

	// Same length, so only the bytes tell the two apart.
	changed := replaceInFixture(t, body, `"run-neutral-fixture"`, `"run-neutral-fixturf"`)
	if err := os.WriteFile(path, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := currentLabContract(); !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), "changed after first load") {
		t.Fatalf("mutation result = %v", err)
	}
}

// TestCurrentLabContract_LocatesByAccount: with the override unset the
// contract is read from the lab's default path for account rc under the state
// directory, and an absent one, at either location, is reported with where
// the suite looked and how to get one there.
func TestCurrentLabContract_LocatesByAccount(t *testing.T) {
	path := installDefaultContract(t, composeFixtureBody(t))
	if !strings.HasSuffix(path, filepath.Join("forge-lab", "targets", "rc.json")) {
		t.Fatalf("default path = %s", path)
	}

	got, err := currentLabContract()
	if err != nil || got.Generation.ID != "run-neutral-fixture" {
		t.Fatalf("default path contract = %+v, %v", got, err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	requireMissingContractHint(t, path, labcontract.PathEnv+` is unset, so the default for lab account "rc"`)

	absent := filepath.Join(t.TempDir(), "absent.json")
	t.Setenv(labcontract.PathEnv, absent)
	requireMissingContractHint(t, absent, "named by "+labcontract.PathEnv)
}

func requireMissingContractHint(t *testing.T, path, source string) {
	t.Helper()

	_, err := currentLabContract()
	if !errors.Is(err, labcontract.ErrFile) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing contract = %v, want ErrFile and ErrNotExist", err)
	}

	for _, fragment := range []string{"no live-target contract at " + path + " (" + source + ")", "`just lab` in the forge-lab checkout"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("missing contract = %v, want it to mention %q", err, fragment)
		}
	}
}

// TestCurrentLabContract_AppliesTheModuleFilePolicy pins that the cached read
// goes through Load: a contract readable by others is refused as a file
// failure, and a malformed one as the module classifies it.
func TestCurrentLabContract_AppliesTheModuleFilePolicy(t *testing.T) {
	exposed := writeContract(t, composeFixtureBody(t))
	if err := os.Chmod(exposed, 0o640); err != nil { //nolint:gosec // The exposure under test.
		t.Fatal(err)
	}

	setCurrentContract(t, exposed)

	if _, err := currentLabContract(); !errors.Is(err, labcontract.ErrFile) {
		t.Fatalf("group-readable contract = %v, want ErrFile", err)
	}

	setCurrentContract(t, writeContract(t, replaceInFixture(t, composeFixtureBody(t), `"version": 4`, `"version": 3`)))

	if _, err := currentLabContract(); !errors.Is(err, labcontract.ErrVersion) {
		t.Fatalf("retired contract = %v, want ErrVersion", err)
	}
}

func TestSelected_MalformedContractCannotBecomeASkip(t *testing.T) {
	body := replaceInFixture(t, composeFixtureBody(t), `"version": 4`, `"version": 1`)
	setCurrentContract(t, writeContract(t, body))

	recorder := &fatalRecorder{}
	if Selected(recorder, provider.ForgeGitLab) {
		t.Fatal("malformed v1 contract was selected")
	}

	if !recorder.fatal || recorder.skipped {
		t.Fatalf("malformed contract became a skip: %+v", recorder)
	}
}

// TestSelected_AbsentForgeIsASkipNotAFailure: a contract minted for other
// forges selects nothing and fails nothing; one that carries the forge selects
// it.
func TestSelected_AbsentForgeIsASkipNotAFailure(t *testing.T) {
	setCurrentContract(t, writeContract(t, fixtureBody(t, "neutral-targets-v4-github.json")))

	recorder := &fatalRecorder{}
	if Selected(recorder, provider.ForgeGitLab) || recorder.failed {
		t.Fatalf("absent forge: selected or failed: %+v", recorder)
	}

	setCurrentContract(t, writeContract(t, composeFixtureBody(t)))

	recorder = &fatalRecorder{}
	if !Selected(recorder, provider.ForgeGitLab) || recorder.failed {
		t.Fatalf("present forge: not selected or failed: %+v", recorder)
	}
}

// TestTargetFromContract_ProjectsProducerFacts checks every field a scenario
// reads off a Target against the compose fixture, for both forges this tier
// drives.
func TestTargetFromContract_ProjectsProducerFacts(t *testing.T) {
	t.Parallel()

	contract := loadFixture(t, replaceInFixture(t, composeFixtureBody(t), `"version": 4,`, `"version": 4, "fixtures": {"catalog_version": 2},`))

	forgejoEndpoint, err := contract.Endpoint(labcontract.Forgejo)
	if err != nil {
		t.Fatal(err)
	}

	forgejo, err := targetFromContract(contract, forgejoEndpoint, provider.ForgeForgejo)
	if err != nil {
		t.Fatal(err)
	}

	//nolint:gosec // Synthetic fixture credential the projection must carry unchanged.
	wantForgejo := Target{
		Forge:              provider.ForgeForgejo,
		Host:               "forgejo.compose.forgelab:8443",
		Owner:              "fixture-user",
		Token:              "fake-forgejo-token",
		CredentialUsername: "fixture-user",
		RegistryOrigin:     "https://forgejo.compose.forgelab:8443",
		Capabilities: []string{
			labcontract.CapabilityArtifacts, labcontract.CapabilityOrganizations, labcontract.CapabilityPackages,
			labcontract.CapabilityRepositories, labcontract.CapabilityWorkflowRuns,
		},
		CAFile:     "/opt/forge-lab/certs/forge-lab-ca.crt",
		FulcioURL:  "https://fulcio.compose.forgelab:8443",
		OIDCIssuer: "https://forgejo.compose.forgelab:8443/api/actions",

		fixtureCatalogVersion: 2,
		fixtureProvider:       labcontract.Forgejo,

		// What the module's client is built from: the contract as decoded and
		// the endpoint as selected, carried rather than re-derived.
		contract: contract,
		endpoint: forgejoEndpoint,
	}
	if !reflect.DeepEqual(forgejo, wantForgejo) {
		t.Fatalf("forgejo target = %+v\nwant %+v", forgejo, wantForgejo)
	}

	gitlabEndpoint, err := contract.Endpoint(labcontract.GitLab)
	if err != nil {
		t.Fatal(err)
	}

	gitlab, err := targetFromContract(contract, gitlabEndpoint, provider.ForgeGitLab)
	if err != nil {
		t.Fatal(err)
	}

	// GitLab's registry is its own host.
	if gitlab.Host != "gitlab.compose.forgelab:8443" || gitlab.RegistryOrigin != "https://registry.gitlab.compose.forgelab:8443" ||
		gitlab.OIDCIssuer != "https://gitlab.compose.forgelab:8443" || gitlab.accepted {
		t.Fatalf("gitlab target = %+v", gitlab)
	}
}

// TestTargetFromContract_UnmappedFactsStayEmpty: an endpoint without a Fulcio
// mapping or a registry projects empty origins rather than invented ones.
func TestTargetFromContract_UnmappedFactsStayEmpty(t *testing.T) {
	t.Parallel()

	body := replaceInFixture(t, composeFixtureBody(t), `,
      {
        "endpoint": "gitlab",
        "oidc_issuer": "https://gitlab.compose.forgelab:8443"
      }`, "")
	body = replaceInFixture(t, body, `"oci_registry": {
        "base_url": "https://registry.gitlab.compose.forgelab:8443",
        "credential": "endpoint"
      }`, `"oci_registry": null`)

	contract := loadFixture(t, body)

	endpoint, err := contract.Endpoint(labcontract.GitLab)
	if err != nil {
		t.Fatal(err)
	}

	target, err := targetFromContract(contract, endpoint, provider.ForgeGitLab)
	if err != nil {
		t.Fatal(err)
	}

	if target.FulcioURL != "" || target.OIDCIssuer != "" || target.RegistryOrigin != "" || target.fixtureCatalogVersion != 0 {
		t.Fatalf("projection invented a mapping: %+v", target)
	}
}

// TestTargetFromContract_RefusesEachAuthorityForItsOwnReason moves one
// credential-bearing origin of the compose fixture's forgejo endpoint outside
// the lab at a time. The module accepts each result as a contract (the
// endpoint layout, owner grammar and issuer mapping are its rules, tested
// there); the disposable-host rule on the registry and Fulcio is this tier's.
func TestTargetFromContract_RefusesEachAuthorityForItsOwnReason(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ from, to, reason string }{
		"registry outside the lab": {from: `"base_url": "https://forgejo.compose.forgelab:8443"`, to: `"base_url": "https://registry.attacker.example"`, reason: `OCI registry: "registry.attacker.example": host is not disposable`},
		"fulcio outside the lab":   {from: `"base_url": "https://fulcio.compose.forgelab:8443"`, to: `"base_url": "https://fulcio.attacker.example"`, reason: `fulcio: "fulcio.attacker.example": host is not disposable`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			contract := loadFixture(t, replaceInFixture(t, composeFixtureBody(t), tc.from, tc.to))

			endpoint, err := contract.Endpoint(labcontract.Forgejo)
			if err != nil {
				t.Fatal(err)
			}

			_, err = targetFromContract(contract, endpoint, provider.ForgeForgejo)
			if !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("refusal = %v, want %q", err, tc.reason)
			}
		})
	}
}

func TestRequireLiveCapabilities_NamesTheMissingOne(t *testing.T) {
	t.Parallel()

	contract := loadFixture(t, composeFixtureBody(t))

	endpoint, err := contract.Endpoint(labcontract.Forgejo)
	if err != nil {
		t.Fatal(err)
	}

	if err := requireLiveCapabilities(endpoint); err != nil {
		t.Fatalf("complete endpoint refused: %v", err)
	}

	for _, missing := range []string{labcontract.CapabilityRepositories, labcontract.CapabilityPackages, labcontract.CapabilityArtifacts} {
		reduced := endpoint
		reduced.Capabilities = slices.DeleteFunc(slices.Clone(endpoint.Capabilities), func(capability string) bool { return capability == missing })

		if err := requireLiveCapabilities(reduced); !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), `capability "`+missing+`"`) {
			t.Errorf("without %s: %v", missing, err)
		}
	}

	endpoint.OCIRegistry = nil
	if err := requireLiveCapabilities(endpoint); !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), "declares no OCI registry") {
		t.Fatalf("without a registry: %v", err)
	}
}

// TestHostOf_RefusesAnythingButAPlainHTTPSURL pins the parse every origin a
// Target carries goes through: an https URL yields its host[:port], and a
// non-https, hostless, credential-bearing or query-bearing value is a refusal
// rather than an empty host.
func TestHostOf_RefusesAnythingButAPlainHTTPSURL(t *testing.T) {
	t.Parallel()

	if got, err := hostOf("https://registry.gitlab.compose.forgelab:8443/"); err != nil || got != "registry.gitlab.compose.forgelab:8443" {
		t.Fatalf("hostOf = %q, %v", got, err)
	}

	for _, value := range []string{"", "http://gitlab.compose.forgelab", "gitlab.compose.forgelab", "https://", "https://user@gitlab.compose.forgelab", "https://gitlab.compose.forgelab?x=1"} {
		if _, err := hostOf(value); !errors.Is(err, errs.ErrValidation) {
			t.Errorf("hostOf(%q) = %v, want a refusal", value, err)
		}
	}
}
