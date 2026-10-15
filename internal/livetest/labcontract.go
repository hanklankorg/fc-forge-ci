// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strconv"
	"sync"

	"codefloe.com/itiquette/forge-lab-contract/labcontract"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// The neutral live-target contract is read through the shared
// forge-lab-contract module: it owns the schema, the decoder, the
// private-file policy, the disposable-host list, credential lifetime and the
// run identity. What stays here is what only this tier can know: which
// capabilities its scenarios need, how a selected endpoint projects onto a
// Target, and that the file must not change under a running suite.

// requireLiveCapabilities is what the complete harness needs from an endpoint
// before it is worth arming: the three capabilities every scenario group
// touches, and a registry to push to.
func requireLiveCapabilities(endpoint labcontract.Endpoint) error {
	for _, required := range []string{labcontract.CapabilityRepositories, labcontract.CapabilityPackages, labcontract.CapabilityArtifacts} {
		if !endpoint.HasCapability(required) {
			return fmt.Errorf("selected endpoint %q lacks required live capability %q: %w", endpoint.Name, required, errs.ErrValidation)
		}
	}

	if endpoint.OCIRegistry == nil {
		return fmt.Errorf("selected endpoint %q declares no OCI registry for the complete live harness: %w", endpoint.Name, errs.ErrValidation)
	}

	return nil
}

// hostOf is the host[:port] of an https URL without userinfo, query or
// fragment, refusing anything else. The contract module already validated
// every URL it decoded; this is for the URLs a Target carries as plain strings.
func hostOf(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%q is not a plain https URL: %w", value, errs.ErrValidation)
	}

	return parsed.Host, nil
}

// activeContracts remembers the digest of the first accepted bytes of each
// contract path, so a contract replaced mid-run (a new generation minted over
// the one this run holds) is reported as such rather than as a string of 401s.
var activeContracts = struct { //nolint:gochecknoglobals // Process cache detects mutation after the first accepted contract read.
	sync.Mutex
	byPath map[string][sha256.Size]byte
}{byPath: map[string][sha256.Size]byte{}}

// currentLabContract reads the contract for this suite's lab account, under
// the module's private-file policy, and refuses bytes that differ from the
// first read of the same path in this process. Every call decodes afresh, so
// callers own every nested value they get back.
//
// The path is the module's: PathEnv when set, otherwise the account's default
// under the state directory, where the lab writes it.
func currentLabContract() (*labcontract.Contract, error) {
	path, err := labcontract.Locate(labAccount)
	if err != nil {
		return nil, err
	}

	// Load applies the file policy (owner-only, regular, bounded) and the
	// full validation; the bytes are then read once more so the digest and
	// the returned value describe the same document.
	if _, loadErr := labcontract.Load(path); loadErr != nil {
		return nil, describeMissingContract(path, loadErr)
	}

	body, err := os.ReadFile(path) //nolint:gosec // The located document Load just accepted.
	if err != nil {
		return nil, fmt.Errorf("read live-target contract: %w", err)
	}

	digest := sha256.Sum256(body)

	activeContracts.Lock()
	defer activeContracts.Unlock()

	if cached, ok := activeContracts.byPath[path]; ok && cached != digest {
		return nil, fmt.Errorf("live-target contract changed after first load: %w", errs.ErrValidation)
	}

	contract, err := labcontract.Decode(body)
	if err != nil {
		return nil, err
	}

	activeContracts.byPath[path] = digest

	return contract, nil
}

// describeMissingContract turns an absent contract into a message that says
// where the suite looked and how to get one there; every other Load failure
// passes through as the module reports it.
func describeMissingContract(path string, err error) error {
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	source := labcontract.PathEnv + " is unset, so the default for lab account " + strconv.Quote(labAccount)
	if os.Getenv(labcontract.PathEnv) != "" {
		source = "named by " + labcontract.PathEnv
	}

	// Both sentinels stay reachable; the module's text would only repeat the path.
	return fmt.Errorf("no live-target contract at %s (%s); get one with `just lab` in the forge-lab checkout: %w: %w",
		path, source, fs.ErrNotExist, labcontract.ErrFile)
}
