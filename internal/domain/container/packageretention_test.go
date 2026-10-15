// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func digest(c byte) string { return "sha256:" + strings.Repeat(string(c), 64) }

func referrer(c byte, suffix string) string {
	return "sha256-" + strings.Repeat(string(c), 64) + suffix
}

func runtimeRule() container.RetentionRule {
	return container.RetentionRule{
		Keep:        regexp.MustCompile(`^v[0-9]+(\.[0-9]+){0,2}(-[0-9A-Za-z.]+)?$`),
		Recent:      regexp.MustCompile(`^sha-[0-9a-f]+$`),
		RecentCount: 2,
	}
}

// plan runs the closure to completion with the given manifest references, as
// the application does against a registry.
func plan(t *testing.T, versions []container.PackageVersion, references map[string][]string, rule container.RetentionRule) container.RetentionPlan {
	t.Helper()

	roots, err := container.RetentionRoots(versions, rule)
	if err != nil {
		t.Fatal(err)
	}

	known := map[string][]string{}

	for range 10 {
		kept, unknown := container.RetainedClosure(versions, roots, known)
		if len(unknown) == 0 {
			return container.PlanRetention(versions, kept, rule)
		}

		for _, d := range unknown {
			known[d] = references[d]
		}
	}

	t.Fatal("the closure did not settle")

	return container.RetentionPlan{}
}

func ids(versions []container.PackageVersion) []int64 {
	out := make([]int64, 0, len(versions))
	for _, v := range versions {
		out = append(out, v.ID)
	}

	return out
}

// A release index, the newest commits and everything they refer to stay; an
// old commit, its platform manifests and its signature go. A kept index's
// untagged platform manifests stay, because deleting them would break it.
func TestPlanRetention_KeepsReleasesRecentCommitsAndWhatTheyReferTo(t *testing.T) {
	t.Parallel()

	day := func(n int) time.Time { return time.Date(2026, 10, n, 0, 0, 0, 0, time.UTC) }
	versions := []container.PackageVersion{
		{ID: 1, Digest: digest('a'), Tags: []string{"v1.0.0"}, Created: day(1)}, // release index
		{ID: 2, Digest: digest('b'), Created: day(1)},                           // its amd64 manifest
		{ID: 3, Digest: digest('c'), Tags: []string{referrer('a', ".sig")}, Created: day(1)},
		{ID: 4, Digest: digest('d'), Tags: []string{"sha-1111111"}, Created: day(2)}, // old commit
		{ID: 5, Digest: digest('e'), Created: day(2)},                                // its manifest
		{ID: 6, Digest: digest('f'), Tags: []string{referrer('d', ".sig")}, Created: day(2)},
		{ID: 7, Digest: digest('g'), Tags: []string{"sha-2222222", "v1.1.0-pre-20261003"}, Created: day(3)},
		{ID: 8, Digest: digest('h'), Tags: []string{"sha-3333333"}, Created: day(4)},
		{ID: 9, Digest: digest('i'), Created: day(5)}, // orphan from an interrupted push
	}
	references := map[string][]string{digest('a'): {digest('b')}, digest('d'): {digest('e')}}

	got := plan(t, versions, references, runtimeRule())

	if want := []int64{4, 5, 6, 9}; !slices.Equal(ids(got.Delete), want) {
		t.Errorf("deleted %v, want %v", ids(got.Delete), want)
	}

	for _, kept := range got.Keep {
		if kept.Reason == "" {
			t.Errorf("version %d is kept without a reason", kept.Version.ID)
		}
	}
}

// A version that holds a kept tag and a tag the rule would let go stays whole,
// and the plan says which tag it keeps along with it.
func TestPlanRetention_ReportsTagsAVersionKeepsAlongWithAKeptTag(t *testing.T) {
	t.Parallel()

	versions := []container.PackageVersion{
		{ID: 1, Digest: digest('a'), Tags: []string{"v2.0.0", "staging-2.0.0"}, Created: time.Now()},
	}

	got := plan(t, versions, nil, runtimeRule())

	if len(got.Delete) != 0 || len(got.Keep) != 1 || !strings.Contains(got.Keep[0].Reason, "also keeps staging-2.0.0") {
		t.Fatalf("plan = %+v, want the version kept with staging-2.0.0 reported", got)
	}
}

// A referrer index in the OCI tag schema is kept with its subject, and the
// referrer manifests it lists are kept with it.
func TestRetainedClosure_FollowsReferrerIndexes(t *testing.T) {
	t.Parallel()

	versions := []container.PackageVersion{
		{ID: 1, Digest: digest('a'), Tags: []string{"v1.0.0"}},
		{ID: 2, Digest: digest('b'), Tags: []string{referrer('a', "")}},
		{ID: 3, Digest: digest('c')}, // a bundle the referrer index lists
	}

	got := plan(t, versions, map[string][]string{digest('b'): {digest('c')}}, runtimeRule())
	if len(got.Delete) != 0 {
		t.Fatalf("deleted %v, want the referrer index and its bundle kept", ids(got.Delete))
	}
}

func TestRetentionRule_RefusesARuleThatKeepsNothing(t *testing.T) {
	t.Parallel()

	for name, rule := range map[string]container.RetentionRule{
		"empty":            {},
		"recent count 0":   {Recent: regexp.MustCompile(`^sha-`)},
		"negative count":   {Keep: regexp.MustCompile(`^v`), RecentCount: -1},
		"count no pattern": {RecentCount: 3},
	} {
		if _, err := container.RetentionRoots(nil, rule); !errors.Is(err, errs.ErrUsage) {
			t.Errorf("%s: err = %v, want a usage refusal", name, err)
		}
	}
}

func TestManifestReferences(t *testing.T) {
	t.Parallel()

	index := `{"schemaVersion":2,"manifests":[{"digest":"` + digest('a') + `"},{"digest":"not-a-digest"}]}`

	got, err := container.ManifestReferences([]byte(index))
	if err != nil || !slices.Equal(got, []string{digest('a')}) {
		t.Fatalf("references = %v, %v", got, err)
	}

	if got, err := container.ManifestReferences([]byte(`{"schemaVersion":2,"layers":[]}`)); err != nil || len(got) != 0 {
		t.Fatalf("an image manifest lists %v, %v", got, err)
	}

	if _, err := container.ManifestReferences([]byte("{")); !errors.Is(err, errs.ErrMalformedInput) {
		t.Fatalf("err = %v, want malformed input", err)
	}
}
