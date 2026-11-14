// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pipeline

import (
	"errors"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// White-box tests for the unexported resolveReleasePolicy. The function
// is package-private because resolution is an implementation detail of
// NewReleasePlan, but the cases below have enough surface to deserve
// their own focused test file.

// resolve runs the resolver and fails on error, so a resolution failure can
// never be mistaken for a policy whose fields happen to be false.
//
// The tests used to discard the error with `got, _ :=`. That made
// TestResolveReleasePolicy_PrereleaseTagNotStable vacuous: it asserts two
// fields are false, and the zero-value ReleasePolicy satisfies both -- so it
// stayed green even when resolveReleasePolicy was made to fail unconditionally.
func resolve(t *testing.T, in releasePolicyInputs) ReleasePolicy {
	t.Helper()

	got, err := resolveReleasePolicy(in)
	if err != nil {
		t.Fatalf("resolveReleasePolicy(%+v): %v", in, err)
	}

	return got
}

func baseInputs() releasePolicyInputs {
	return releasePolicyInputs{
		ReleaseSBOMs:  "all",
		PipelineSBOMs: "none",
		RefName:       "v1.0.0",
	}
}

func TestResolveReleasePolicy_StableTagInfersMakeLatest(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.RefName = "v1.0.0"
	in.ReleasePolicy = "stable"

	got := resolve(t, in)

	if !got.MakeLatest {
		t.Errorf("v1.0.0 with no release_type should be stable")
	}
}

func TestResolveReleasePolicy_PrereleaseTagNotStable(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.RefName = "v1.0.0-rc.1"
	in.ReleasePolicy = "stable-prerelease"

	got := resolve(t, in)
	if got.MakeLatest {
		t.Errorf("prerelease tag should not be stable")
	}

	if got.CreateDraftRelease {
		t.Errorf("semver prerelease tag should not become draft")
	}
}

func TestResolveReleasePolicy_StableRejectsPrerelease(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.ReleasePolicy = "stable"
	in.RefName = "v1.0.0-rc.1"

	if _, err := resolveReleasePolicy(in); !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("stable policy accepted prerelease: %v", err)
	}
}

func TestResolveReleasePolicy_DraftReleaseDecisions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		refName      string
		releaseDraft bool
	}{
		{name: "uppercase_snapshot", refName: "v1.0.0-SNAPSHOT"},
		{name: "lowercase_snapshot", refName: "v1.0.0-snapshot"},
		{name: "explicit_draft_flag", refName: "v1.0.0", releaseDraft: true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			in := baseInputs()
			in.RefName = testCase.refName
			in.ReleasePolicy = "stable-prerelease"
			in.ReleaseDraft = testCase.releaseDraft

			got := resolve(t, in)
			if got.CreateDraftRelease != testCase.releaseDraft {
				t.Errorf("draft must be explicit: ref=%q got=%v", testCase.refName, got.CreateDraftRelease)
			}
		})
	}
}

func TestResolveReleasePolicy_CreateReleaseGatesOnPublisher(t *testing.T) {
	t.Parallel()

	cases := map[string]bool{
		"github-cli": true,
		"":           false,
		"forgejo":    false,
	}
	for publisher, want := range cases {
		in := baseInputs()
		in.ReleasePublisher = publisher

		got := resolve(t, in)
		if got.CreateRelease != want {
			t.Errorf("publisher=%q → CreateRelease=%v, want %v", publisher, got.CreateRelease, want)
		}
	}
}

func TestResolveReleasePolicy_AuthorizationDisjunction(t *testing.T) {
	t.Parallel()

	cases := []struct {
		any, release, want bool
	}{
		{false, false, false},
		{true, false, true},
		{false, true, true},
		{true, true, true},
	}
	for _, cfg := range cases {
		in := baseInputs()
		in.AnyRequireAuthorization = cfg.any
		in.ReleaseRequireAllowlistedSigner = cfg.release

		got := resolve(t, in)
		if got.RequireAllowlistedSigner != cfg.want {
			t.Errorf("any=%v release=%v → %v, want %v", cfg.any, cfg.release, got.RequireAllowlistedSigner, cfg.want)
		}
	}
}

func TestResolveReleasePolicy_VersionBumpRequiresGitCliffAndNotSkipped(t *testing.T) {
	t.Parallel()

	cases := []struct {
		creator string
		skip    bool
		want    bool
	}{
		{"git-cliff", false, true},
		{"git-cliff", true, false},
		{"", false, false},
	}
	for _, cfg := range cases {
		in := baseInputs()
		in.ChangelogCreator = cfg.creator
		in.ChangelogSkipVersionBump = cfg.skip

		got := resolve(t, in)
		if got.RunVersionBump != cfg.want {
			t.Errorf("creator=%q skip=%v → %v, want %v", cfg.creator, cfg.skip, got.RunVersionBump, cfg.want)
		}
	}
}

func TestResolveReleasePolicy_EffectiveSBOMsIntersection(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.ReleaseSBOMs = "all"
	in.PipelineSBOMs = "build,analyzed-artifact"

	got := resolve(t, in)
	if got.SBOMs != "build,analyzed-artifact" {
		t.Errorf("SBOMs = %q", got.SBOMs)
	}

	if got.SBOMConflict != nil {
		t.Errorf("no conflict expected: %+v", got.SBOMConflict)
	}
}

func TestResolveReleasePolicy_EffectiveSBOMsIntersectionCanonicalOrder(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.ReleaseSBOMs = "analyzed-container,build"
	in.PipelineSBOMs = "analyzed-artifact,analyzed-container,build"

	got := resolve(t, in)
	if got.SBOMs != "build,analyzed-container" {
		t.Errorf("SBOMs = %q", got.SBOMs)
	}
}

func TestResolveReleasePolicy_EffectiveSBOMsDisjointConflict(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.ReleaseSBOMs = "build"
	in.PipelineSBOMs = "analyzed-container"

	got := resolve(t, in)
	if got.SBOMs != "none" {
		t.Errorf("disjoint should yield 'none', got %q", got.SBOMs)
	}

	if got.SBOMConflict == nil {
		t.Error("expected SBOMConflict to be set on disjoint non-none configs")
	}
}

func TestResolveReleasePolicy_EffectiveSBOMsNoneNoConflict(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.ReleaseSBOMs = "none"
	in.PipelineSBOMs = "build"

	got := resolve(t, in)
	if got.SBOMs != "none" {
		t.Errorf("SBOMs = %q", got.SBOMs)
	}

	if got.SBOMConflict != nil {
		t.Error("explicit 'none' should not flag a conflict")
	}
}

func TestResolveReleasePolicy_HasContainersPassthrough(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.HasContainers = true

	got := resolve(t, in)
	if !got.HasContainers {
		t.Error("HasContainers should pass through")
	}
}

func TestResolveReleasePolicy_BadSBOMValueErrors(t *testing.T) {
	t.Parallel()

	in := baseInputs()

	in.ReleaseSBOMs = "banana"
	if _, err := resolveReleasePolicy(in); !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("bad release_sboms: err = %v, want errs.ErrValidation", err)
	}
}

func TestResolveReleasePolicy_RejectsUnknownPolicy(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.ReleasePolicy = "auto"
	in.RefName = "v2.0.0"

	if _, err := resolveReleasePolicy(in); !errors.Is(err, errs.ErrUsage) {
		t.Fatalf("unknown policy accepted: %v", err)
	}
}

// GitHub renders git-cliff only: another renderer's name is refused, not
// planned as a release without a version bump or changelog.
func TestResolveReleasePolicy_RefusesAChangelogCreatorOtherThanGitCliff(t *testing.T) {
	t.Parallel()

	for _, creator := range []string{"git-chglog", "changie", " git-cliff"} {
		in := baseInputs()
		in.ChangelogCreator = creator

		if _, err := resolveReleasePolicy(in); !errors.Is(err, errs.ErrUsage) || !strings.Contains(err.Error(), "git-cliff only") {
			t.Errorf("changelog-creator %q: got %v, want a usage error naming git-cliff only", creator, err)
		}
	}
}
