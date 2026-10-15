// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codefloe.com/itiquette/forge-lab-contract/fixturemanifest"
	"codefloe.com/itiquette/forge-lab-contract/labcontract"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/stretchr/testify/require"
)

func checkoutManifestFixture() string {
	return `{"version":1,"catalog_version":2,"provider":"forgejo","owner":"owner","reserved_prefixes":["fx-"],"fixtures":[{"name":"fx-tag-behind-head","owner":"owner","marker":"gpl-fx-aaaaaaaaaaaaaaaaaaaaaaaaaa","default_branch":"main","private":false,"archived":false,"references":[{"name":"refs/heads/main","object":"1111111111111111111111111111111111111111"},{"name":"refs/tags/v1.0.0","object":"2222222222222222222222222222222222222222","peeled":"3333333333333333333333333333333333333333"}]}]}`
}

// checkoutFixtureOf decodes a manifest through the module and binds it to the
// target, so a refusal by either stage counts as a refusal.
func checkoutFixtureOf(body string, target Target) (CheckoutFixture, error) {
	manifest, err := fixturemanifest.Decode([]byte(body))
	if err != nil {
		return CheckoutFixture{}, err
	}

	return checkoutFixtureFrom(manifest, target)
}

func TestCheckoutFixtureUsesPublishedRefIdentities(t *testing.T) {
	t.Parallel()

	target := Target{Owner: "owner", fixtureCatalogVersion: 2, fixtureProvider: labcontract.Forgejo}
	fixture, err := checkoutFixtureOf(checkoutManifestFixture(), target)
	require.NoError(t, err)
	require.Equal(t, "fx-tag-behind-head", fixture.Repository)
	require.Equal(t, "v1.0.0", fixture.Tag)
	require.Equal(t, "main", fixture.DefaultBranch)
	require.Equal(t, strings.Repeat("1", 40), fixture.BranchCommit)
	require.Equal(t, strings.Repeat("3", 40), fixture.TagCommit)

	for _, change := range []struct{ old, replacement string }{
		{`"version":1`, `"version":2`},
		{`"version":1`, `"version":1,"version":1`},
		{`"catalog_version":2`, `"catalog_version":1`},
		{`"catalog_version":2`, `"catalog_version":3`},
		{`"provider":"forgejo"`, `"provider":"gitlab"`},
		{`"owner":"owner"`, `"owner":"other"`},
		{`"reserved_prefixes":["fx-"]`, `"reserved_prefixes":["scratch-"]`},
		{`"name":"fx-tag-behind-head"`, `"name":"fx-plain"`},
		{`"marker":"gpl-fx-`, `"marker":"other-`},
		{`"default_branch":"main"`, `"default_branch":"trunk"`},
		{`"private":false`, `"private":true`},
		{`"archived":false`, `"archived":true`},
		{`"archived":false,`, `"archived":false,"not_represented":[{"field":"topics","reason":"unsupported"}],`},
		{strings.Repeat("3", 40), strings.Repeat("1", 40)},
		{strings.Repeat("2", 40), strings.Repeat("3", 40)},
		{`"refs/tags/v1.0.0"`, `"refs/heads/main"`},
		{`"refs/heads/main"`, `"refs/heads/other"`},
		{`"peeled":"` + strings.Repeat("3", 40) + `"`, `"peeled":""`},
		{`"object":"1111111111111111111111111111111111111111"`, `"object":"1111111111111111111111111111111111111111","peeled":"` + strings.Repeat("4", 40) + `"`},
		{`"object":"1111111111111111111111111111111111111111"`, `"object":"111111111111111111111111111111111111111Z"`},
	} {
		body := strings.Replace(checkoutManifestFixture(), change.old, change.replacement, 1)
		require.NotEqual(t, checkoutManifestFixture(), body, change.old)

		_, err = checkoutFixtureOf(body, target)
		require.Error(t, err, change.old)
	}

	_, err = checkoutFixtureOf(checkoutManifestFixture()+checkoutManifestFixture(), target)
	require.Error(t, err)
}

// TestPublishedCheckoutFixture_ReadsTheEndpointManifest pins the wiring: the
// manifest the target names is read through the module, a manifest that is
// not there is a fatal refusal rather than an empty fixture, and a manifest
// holds no credential, so a world-readable one is fine.
func TestPublishedCheckoutFixture_ReadsTheEndpointManifest(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "manifest.json")
	target := Target{Owner: "owner", fixtureCatalogVersion: 2, fixtureProvider: labcontract.Forgejo, fixtureManifestFile: path, accepted: true}

	recorder := &fatalRecorder{}
	require.Equal(t, CheckoutFixture{}, PublishedCheckoutFixture(recorder, target))
	require.True(t, recorder.fatal)

	require.NoError(t, os.WriteFile(path, []byte(checkoutManifestFixture()), 0o644)) //nolint:gosec // Deliberately public: a manifest is not a credential.

	recorder = &fatalRecorder{}
	require.Equal(t, "fx-tag-behind-head", PublishedCheckoutFixture(recorder, target).Repository)
	require.False(t, recorder.failed)
}

// TestContractProjectsFixtureFacts carries the fixture catalog and the
// endpoint's manifest path onto the target, and refuses a catalog declaration
// that is absent, non-positive, mistyped or under an unknown key.
func TestContractProjectsFixtureFacts(t *testing.T) {
	t.Parallel()

	var wire map[string]any

	require.NoError(t, json.Unmarshal([]byte(composeFixtureBody(t)), &wire))
	wire["fixtures"] = map[string]any{"catalog_version": 2}
	endpoints, ok := wire["endpoints"].([]any)
	require.True(t, ok)

	for _, value := range endpoints {
		endpoint, valid := value.(map[string]any)
		require.True(t, valid)

		name, named := endpoint["name"].(string)
		require.True(t, named)

		endpoint["fixture_manifest_file"] = "/private/manifest-" + name + ".json"
		endpoint["runner_label"] = "ubuntu-latest"
		endpoint["git_ssh_base_url"] = "ssh://git@forgejo.compose.forgelab:2222"
		endpoint["host_key_fingerprint"] = "SHA256:fixture"
	}

	body, err := json.Marshal(wire)
	require.NoError(t, err)
	lab, err := labcontract.Decode(body)
	require.NoError(t, err)
	endpoint, err := lab.Endpoint(labcontract.Forgejo)
	require.NoError(t, err)
	target, err := targetFromContract(lab, endpoint, provider.ForgeForgejo)
	require.NoError(t, err)
	require.Equal(t, 2, target.fixtureCatalogVersion)
	require.Equal(t, "/private/manifest-forgejo.json", target.fixtureManifestFile)
	require.Equal(t, labcontract.Forgejo, target.fixtureProvider)
	require.Equal(t, "fixture-user", target.Owner)

	for _, declaration := range []map[string]any{
		{}, {"catalog_version": 0}, {"catalog_version": -1}, {"catalog_version": "2"}, {"catalog": 2},
	} {
		wire["fixtures"] = declaration
		body, err = json.Marshal(wire)
		require.NoError(t, err)
		_, err = labcontract.Decode(body)
		require.Error(t, err)
	}
}

// The durable fixture belongs to the lab's fixture owner, not to the account
// this suite's credential was minted for; the binding names that owner so the
// scenario reads the fixture where it lives, and Reading addresses it.
func TestCheckoutFixtureNamesTheManifestOwnerNotTheSuiteAccount(t *testing.T) {
	t.Parallel()

	target := Target{Owner: "rc", fixtureCatalogVersion: 2, fixtureProvider: labcontract.Forgejo}
	fixture, err := checkoutFixtureOf(checkoutManifestFixture(), target)
	require.NoError(t, err)
	require.Equal(t, "owner", fixture.Owner)
	require.Equal(t, "owner/fx-tag-behind-head", RepoSlug(target.Reading(fixture.Owner), fixture.Repository))
	require.Equal(t, "rc", target.Owner, "Reading must not change the suite's own target")
}
