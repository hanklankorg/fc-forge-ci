// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"codefloe.com/itiquette/forge-lab-contract/fixturemanifest"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// CheckoutFixture is a read-only durable fixture whose tag precedes its branch.
// It carries no cleanup authority: durable fx-* repositories are never scratch.
type CheckoutFixture struct {
	// Owner is the account the durable fixture belongs to, as its manifest
	// records it: the lab's fixture owner, not this suite's account.
	Owner         string
	Repository    string
	Tag           string
	DefaultBranch string
	TagCommit     string
	BranchCommit  string
}

const (
	checkoutFixtureName   = "fx-tag-behind-head"
	checkoutFixtureTag    = "v1.0.0"
	checkoutFixtureBranch = "main"
	fixtureReservedPrefix = "fx-"
	fixtureMarkerPrefix   = "gpl-fx-"
	minimumFixtureCatalog = 2
)

// PublishedCheckoutFixture reads only the selected endpoint's explicit manifest.
// The scenario independently compares these identities with provider responses.
func PublishedCheckoutFixture(tb TB, target Target) CheckoutFixture {
	tb.Helper()
	requireAccepted(tb, target)

	manifest, err := fixturemanifest.Load(target.fixtureManifestFile)
	if err != nil {
		tb.Fatalf("livetest: selected endpoint fixture manifest is unavailable: %v", err)

		return CheckoutFixture{}
	}

	fixture, err := checkoutFixtureFrom(manifest, target)
	if err != nil {
		tb.Fatalf("livetest: %v", err)
	}

	return fixture
}

// checkoutFixtureFrom binds the manifest to the selected target and requires
// the one fixture the checkout scenario reads, in the shape it depends on.
func checkoutFixtureFrom(manifest *fixturemanifest.Manifest, target Target) (CheckoutFixture, error) {
	if manifest.CatalogVersion != target.fixtureCatalogVersion || manifest.CatalogVersion < minimumFixtureCatalog ||
		manifest.Provider != target.fixtureProvider || manifest.Owner == "" ||
		!slices.Contains(manifest.ReservedPrefixes, fixtureReservedPrefix) {
		return CheckoutFixture{}, fmt.Errorf("fixture manifest catalog, provider or owner does not match the selected target: %w", errs.ErrValidation)
	}

	seen := make(map[string]bool, len(manifest.Fixtures))
	for _, record := range manifest.Fixtures {
		if record.Name == "" || seen[record.Name] {
			return CheckoutFixture{}, fmt.Errorf("fixture manifest has missing or duplicate repository names: %w", errs.ErrValidation)
		}

		seen[record.Name] = true
	}

	selected, ok := manifest.Fixture(checkoutFixtureName)
	if !ok {
		return CheckoutFixture{}, fmt.Errorf("%s is missing: %w", checkoutFixtureName, errs.ErrValidation)
	}

	return checkoutFixtureFromRecord(selected, manifest.Owner)
}

func checkoutFixtureFromRecord(selected fixturemanifest.Record, owner string) (CheckoutFixture, error) {
	if err := requireCheckoutFixtureShape(selected, owner); err != nil {
		return CheckoutFixture{}, err
	}

	branch, tag, err := checkoutFixtureReferences(selected)
	if err != nil {
		return CheckoutFixture{}, err
	}

	// Compare commits, not tag objects: Gitea and Forgejo stamp their own
	// tagger onto annotated tags, so the tag object differs per provider
	// while the commit it names does not.
	fixture := CheckoutFixture{
		Owner:         selected.Owner,
		Repository:    selected.Name,
		Tag:           checkoutFixtureTag,
		DefaultBranch: selected.DefaultBranch,
		TagCommit:     tag.Commit(),
		BranchCommit:  branch.Commit(),
	}
	if fixture.TagCommit == fixture.BranchCommit {
		return CheckoutFixture{}, fmt.Errorf("fixture tag must differ from the branch head: %w", errs.ErrValidation)
	}

	return fixture, nil
}

// requireCheckoutFixtureShape is the shape the checkout scenario depends on:
// the fixture owner's public, live repository on main, marked as a fixture,
// with exactly its two references and nothing the provider could not represent.
func requireCheckoutFixtureShape(selected fixturemanifest.Record, owner string) error {
	if selected.Owner != owner || selected.Private || selected.Archived || !strings.HasPrefix(selected.Marker, fixtureMarkerPrefix) ||
		selected.DefaultBranch != checkoutFixtureBranch || len(selected.References) != 2 || len(selected.NotRepresented) != 0 {
		return fmt.Errorf("%s disagrees with its required shape: %w", checkoutFixtureName, errs.ErrValidation)
	}

	return nil
}

// checkoutFixtureReferences returns the branch head and the annotated tag the
// fixture publishes, each with a valid object identity.
func checkoutFixtureReferences(selected fixturemanifest.Record) (fixturemanifest.Reference, fixturemanifest.Reference, error) {
	branch, ok := selected.Reference("refs/heads/" + checkoutFixtureBranch)
	if !ok || branch.Peeled != "" || !fixtureObjectID(branch.Object) {
		return fixturemanifest.Reference{}, fixturemanifest.Reference{}, fmt.Errorf("fixture branch reference is missing, annotated or invalid: %w", errs.ErrValidation)
	}

	tag, ok := selected.Reference("refs/tags/" + checkoutFixtureTag)
	if !ok || !fixtureObjectID(tag.Object) || !fixtureObjectID(tag.Peeled) || tag.Object == tag.Peeled || len(tag.Object) != len(tag.Peeled) {
		return fixturemanifest.Reference{}, fixturemanifest.Reference{}, fmt.Errorf("fixture tag must be one annotated reference: %w", errs.ErrValidation)
	}

	return branch, tag, nil
}

func fixtureObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}

	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}

	return true
}

// BranchCommitSHA is an independent, read-only provider oracle for the fixture.
func BranchCommitSHA(tb TB, target Target, repo, branch string) string {
	tb.Helper()
	requireAccepted(tb, target)

	ctx, cancel := context.WithTimeout(Context(tb), timeoutAPI)
	defer cancel()

	var payload struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}

	endpoint := target.BaseURL() + "/api/v1/repos/" + url.PathEscape(target.Owner) + "/" + url.PathEscape(repo) + "/branches/" + url.PathEscape(branch)
	if target.Forge == provider.ForgeGitLab {
		endpoint = target.BaseURL() + "/api/v4/projects/" + url.PathEscape(target.Owner+"/"+repo) + "/repository/branches/" + url.PathEscape(branch)
	}

	if _, err := decode(ctx, target, http.MethodGet, endpoint, nil, &payload, http.StatusOK); err != nil {
		tb.Fatalf("livetest: read fixture branch: %v", err)
	}

	if !fixtureObjectID(payload.Commit.ID) {
		tb.Fatalf("livetest: %s branch returned no valid commit", target.Forge)
	}

	return payload.Commit.ID
}
