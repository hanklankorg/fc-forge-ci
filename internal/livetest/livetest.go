// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package livetest is the kit for the live-forge conformance tier: the tests
// that drive this repository's real provider adapters against real Forgejo and
// GitLab servers instead of an httptest fake.
//
// # Why this tier exists
//
// Every adapter is currently verified against a fake we wrote from the same
// assumptions as the adapter. An assumption that is wrong is therefore wrong in
// both places, and both agree. This tier is where a real server gets a vote.
//
// It is also the only place the product claim can be tested: that a verb means
// the same thing on every forge. One scenario runs against every provider that
// claims the capability, and the same outcome is asserted for each.
//
// # Not the black-box suite
//
// Keep these apart. The black-box suite lives in its own repository
// (forge-ci-blackbox-tests), drives the compiled binary against real
// toolchains with the network faked, and deliberately excludes forge APIs. This
// tier is the opposite trade: real forge APIs, no toolchain claims. A scenario
// that needs syft, gpg, or a reproducible archive belongs there, not here.
//
// # Not lab-specific
//
// This reads the neutral live-target contract: one JSON object describing
// disposable provider infrastructure, read through the shared
// forge-lab-contract module that defines it. Forge Lab produces it, but nothing
// here assumes Forge Lab beyond the module's list of hosts a consumer may
// destroy things on -- any operator willing to meet the documented shape can
// supply one. A Target carries the selected forge identity plus its declared
// registry, transport CA, and Fulcio issuer facts.
//
// # Safety
//
// Every scenario mutates a real server, so nothing runs without independent
// agreements: the `live` build tag, and a contract for this suite's lab account
// naming hosts this tier is compiled to accept as disposable. On those hosts
// the compiled allowlist, the compiled resource prefix and the per-account
// credential are the authorization: the suite cannot reach another host,
// another account's repositories, or a name outside its prefix. A normal
// `go test ./...` never sees any of it.
//
// The contract supplies infrastructure facts only. It carries no namespace,
// because a producer that supplied one would be handing out permission along
// with the address. The owner this run acts as is the user the credential was
// minted for; every resource this tier creates sits under that user and under
// the compiled-in prefix. The preflight prints what a run will touch, so the
// operator still sees the run identity before anything is built.
package livetest

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"time"

	"codefloe.com/itiquette/forge-lab-contract/labcontract"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// TB is the slice of *testing.T this kit needs, kept as an interface so the
// package stays free of the testing import and can be reasoned about (and
// unit-tested) as ordinary code.
type TB interface {
	Context() context.Context
	Helper()
	Logf(format string, args ...any)
	Skipf(format string, args ...any)
	Fatalf(format string, args ...any)
	Errorf(format string, args ...any)
	Cleanup(f func())
}

// ResourcePrefix is the namespace this suite owns on every forge it touches.
//
// It is this suite's own declaration, not something a producer tells it. The
// neutral contract carries no namespace on purpose -- a producer that named one
// would be authorizing a consumer it knows nothing about -- so the prefix is
// compiled in here and every resource this tier creates or destroys is required
// to sit under it.
const ResourcePrefix = "rc-"

// labAccount is this suite's account on the lab. The producer writes the
// account's contract to the module's default path; the module's PathEnv
// (LAB_TARGETS_FILE) overrides it.
const labAccount = "rc"

const (
	proxyBinaryEnv        = "RC_LIVE_CREDENTIAL_PROXY_BIN"
	probeJSONBinaryEnv    = "RC_LIVE_PROBE_JSON_BIN"
	runnerBinaryEnv       = "RC_LIVE_RUNNER_BIN"
	runnerBinarySHA256Env = "RC_LIVE_RUNNER_SHA256"
	layerBinaryEnv        = "RC_LIVE_LAYER_BIN"
	liveProfileEnv        = "RC_LIVE_PROFILE"
)

// Deadlines for waits against a real forge. They are constants: a run that
// needs longer waits changes them here, in review.
const (
	timeoutAPI          = time.Minute      // one forge or registry API call
	timeoutScratch      = 2 * time.Minute  // creating or deleting a scratch project
	timeoutCLI          = 3 * time.Minute  // one forge-ci invocation
	timeoutLayerPush    = 3 * time.Minute  // snapshotting the layer and pushing it to a scratch repository
	timeoutLayerRun     = 20 * time.Minute // a workflow of the layer from its trigger to its last job, queue time included
	timeoutConsumerRun  = 75 * time.Minute // a real consumer's workflow in a trial, the longest release included
	timeoutWorkflowLog  = 3 * time.Minute  // fetching a finished run's logs
	timeoutWorkflowPoll = 4 * time.Minute  // a run from commit to terminal state, queue time included
	timeoutWorkflow     = 5 * time.Minute  // dispatching a workflow run
	timeoutHTTPShort    = 30 * time.Second // one short probing HTTP request
)

// liveForges are the forge kinds this tier drives. A contract may carry
// other endpoints (gitea); they are never selected.
var liveForges = []provider.ForgeAPI{provider.ForgeGitLab, provider.ForgeForgejo} //nolint:gochecknoglobals // read-only table.

// Target names one selected forge instance and the identity to act as.
type Target struct {
	Forge              provider.ForgeAPI
	Host               string // host[:port], no scheme
	Owner              string
	Token              string
	CredentialUsername string
	RegistryOrigin     string
	Capabilities       []string
	CAFile             string
	FulcioURL          string
	OIDCIssuer         string
	caPEM              []byte

	fixtureManifestFile   string
	fixtureCatalogVersion int
	fixtureProvider       labcontract.Kind

	// contract and endpoint are what the module's client is built from. Only
	// the projection sets them, so a hand-built Target has no lifecycle.
	contract *labcontract.Contract
	endpoint labcontract.Endpoint

	// accepted is set only by the destructive guard. Every mutating helper
	// refuses a Target without it, so a hand-built Target cannot reach a
	// server. acceptedAt is the instant the credential lifetime was judged;
	// the module's client judges it again with the same clock.
	accepted   bool
	acceptedAt time.Time
}

// BaseURL is the https origin the adapters and the raw oracle both address.
func (t Target) BaseURL() string { return "https://" + t.Host }

// String names a target for test output without exposing its token.
func (t Target) String() string { return string(t.Forge) + "@" + t.Host + "/" + t.Owner }

// Selected reports whether the contract carries this forge. Scenarios use it to
// skip cleanly rather than fail when a contract was minted for a subset of
// providers.
func Selected(tb TB, forge provider.ForgeAPI) bool {
	tb.Helper()

	lab, err := currentLabContract()
	if err != nil {
		tb.Fatalf("livetest: live-target contract: %v", err)

		return false
	}

	_, err = lab.Endpoint(labcontract.Kind(forge))
	if err != nil && !errors.Is(err, labcontract.ErrNoEndpoint) {
		tb.Fatalf("livetest: %v", err)
	}

	return err == nil
}

// Accept reads the contract, validates every safety rule, and returns a Target
// armed for mutation. It fails the test rather than returning an error: a
// half-armed target is not a thing a scenario should be able to hold.
func Accept(tb TB, forge provider.ForgeAPI) Target {
	tb.Helper()

	if err := Context(tb).Err(); err != nil {
		tb.Fatalf("livetest: run interrupted: %v", err)
	}

	if !Selected(tb, forge) {
		tb.Skipf("livetest: the contract carries no %s endpoint", forge)
	}

	lab, err := currentLabContract()
	if err != nil {
		tb.Fatalf("livetest: live-target contract: %v", err)
	}

	target, err := acceptTarget(lab, forge, time.Now())
	if err != nil {
		tb.Fatalf("livetest: destructive guard refused this run: %v", err)
	}

	target, err = captureTargetCA(target)
	if err != nil {
		tb.Fatalf("livetest: contract ca_file refused this run: %v", err)
	}

	return target
}

// acceptTarget is the whole destructive guard, a function of the contract, the
// forge and the clock, which is what makes it table-testable without a lab.
// Order matters only for the quality of the message: every rule is mandatory.
//
// The contract module supplies the rules any consumer shares -- the endpoint
// of a kind, the compiled disposable-host list and credential lifetime. This
// suite supplies what it alone knows: the forges it drives, the capabilities
// it needs, the projection of an endpoint onto a Target, and its namespace.
// No typed confirmation: on lab hosts the allowlist, the prefix and the
// per-account credential are the authorization.
func acceptTarget(lab *labcontract.Contract, forge provider.ForgeAPI, now time.Time) (Target, error) {
	if !slices.Contains(liveForges, forge) {
		return Target{}, fmt.Errorf("platform %q is not a live-forge target in this tier: %w", forge, errs.ErrValidation)
	}

	// ErrNoEndpoint passes through untouched: to Accept it is a skip.
	endpoint, err := lab.Endpoint(labcontract.Kind(forge))
	if err != nil {
		return Target{}, err
	}

	if err = requireLiveCapabilities(endpoint); err != nil {
		return Target{}, err
	}

	target, err := targetFromContract(lab, endpoint, forge)
	if err != nil {
		return Target{}, fmt.Errorf("endpoint trust boundary refused this run: %w", err)
	}

	if err = requireDisposable(endpoint, target.FulcioURL); err != nil {
		return Target{}, err
	}

	if err = endpoint.Credential.CheckLifetime(now); err != nil {
		return Target{}, fmt.Errorf("%s credential: %w", forge, refused(err))
	}

	target.accepted = true
	target.acceptedAt = now

	return target, nil
}

// requireDisposable holds every host the endpoint would send its credential
// to, and the Fulcio host when one is mapped, to the module's compiled list.
func requireDisposable(endpoint labcontract.Endpoint, fulcioURL string) error {
	hosts := endpoint.Hosts()

	if fulcioURL != "" {
		fulcioHost, err := hostOf(fulcioURL)
		if err != nil {
			return err
		}

		hosts = append(hosts, fulcioHost)
	}

	if err := labcontract.Disposable(hosts...); err != nil {
		return refused(err)
	}

	return nil
}

// refused classifies a contract-module refusal as this tier's validation
// failure while keeping the module's own sentinel reachable through errors.Is.
func refused(err error) error {
	return fmt.Errorf("%w: %w", err, errs.ErrValidation)
}

// targetFromContract projects one endpoint onto the Target the scenarios hold.
// The owner is the user the credential was minted for: the contract already
// says whose namespace the token opens, and a second declaration of the same
// fact would only be one more thing to get wrong.
func targetFromContract(lab *labcontract.Contract, endpoint labcontract.Endpoint, forge provider.ForgeAPI) (Target, error) {
	fulcioURL, oidcIssuer, _ := lab.FulcioFor(endpoint.Name)

	caFile := ""
	if lab.CAFile != nil {
		caFile = *lab.CAFile
	}

	target := Target{
		contract:            lab,
		endpoint:            endpoint,
		fixtureManifestFile: endpoint.FixtureManifestFile,
		fixtureProvider:     endpoint.Kind,
		Forge:               forge,
		Host:                endpoint.Host(),
		Owner:               endpoint.Owner(),
		Token:               endpoint.Credential.Token,
		CredentialUsername:  endpoint.Credential.Username,
		RegistryOrigin:      endpoint.RegistryOrigin(),
		Capabilities:        slices.Clone(endpoint.Capabilities),
		CAFile:              caFile,
		FulcioURL:           fulcioURL,
		OIDCIssuer:          oidcIssuer,
	}
	if lab.Fixtures != nil {
		target.fixtureCatalogVersion = lab.Fixtures.CatalogVersion
	}

	if err := validateTargetAuthorities(target); err != nil {
		return Target{}, err
	}

	return target, nil
}

// parseOrigin is the host of an https URL that carries no path: the web and
// Git roots, which adapters append owner and repository segments to.
func parseOrigin(value string) (string, error) {
	host, err := hostOf(value)
	if err != nil {
		return "", err
	}

	parsed, _ := url.Parse(value)
	if parsed.Path != "" && parsed.Path != "/" {
		return "", fmt.Errorf("%q must be an https origin without a path: %w", value, errs.ErrValidation)
	}

	return host, nil
}

// validateTargetAuthorities admits the registry and Fulcio origins a target
// may send credentials to. Each must be a disposable host: that is the whole
// rule, and it is the same rule the forge host itself passed. The issuer is a
// property of the forge kind, not of the lab, so it is derived from the forge.
func validateTargetAuthorities(target Target) error {
	if err := validateRegistryAuthority(target); err != nil {
		return err
	}

	return validateFulcioAuthority(target)
}

func validateRegistryAuthority(target Target) error {
	if target.RegistryOrigin == "" {
		return nil
	}

	registry, err := parseOrigin(target.RegistryOrigin)
	if err != nil {
		return fmt.Errorf("%s OCI registry: %w", target.Forge, err)
	}

	if err := labcontract.Disposable(registry); err != nil {
		return fmt.Errorf("%s OCI registry: %w", target.Forge, refused(err))
	}

	return nil
}

func validateFulcioAuthority(target Target) error {
	if target.FulcioURL == "" && target.OIDCIssuer == "" {
		return nil
	}

	if target.FulcioURL == "" || target.OIDCIssuer == "" {
		return fmt.Errorf("%s Fulcio URL and issuer mapping must be supplied together: %w", target.Forge, errs.ErrValidation)
	}

	fulcio, err := parseOrigin(target.FulcioURL)
	if err != nil {
		return fmt.Errorf("fulcio: %w", err)
	}

	if err := labcontract.Disposable(fulcio); err != nil {
		return fmt.Errorf("fulcio: %w", refused(err))
	}

	// The module held the mapping to the kind's issuer at decode; a Target
	// built by hand is held to the same fact here, at the use boundary.
	wantIssuer := labcontract.Kind(target.Forge).OIDCIssuer(target.BaseURL())
	if target.OIDCIssuer != wantIssuer {
		return fmt.Errorf("%s OIDC issuer must equal the contract endpoint mapping %q, got %q: %w",
			target.Forge, wantIssuer, target.OIDCIssuer, errs.ErrValidation)
	}

	return nil
}

// FulcioConfig returns the exact contract mapping only after rechecking the
// accepted target's Fulcio and issuer relationship at the use boundary.
func FulcioConfig(tb TB, target Target) (string, string) {
	tb.Helper()
	requireAccepted(tb, target)

	if target.FulcioURL == "" || target.OIDCIssuer == "" {
		tb.Fatalf("livetest: target has no Fulcio issuer mapping")
	}

	if err := validateTargetAuthorities(target); err != nil {
		tb.Fatalf("livetest: refusing to send an OIDC token to Fulcio: %v", err)
	}

	return target.FulcioURL, target.OIDCIssuer
}
