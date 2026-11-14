// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// A demo is its own repository and the helpers named after it; nothing else in
// the account is taken for part of one, so a deletion takes exactly its set.
func TestGroupDemos_TakesEachDemoAndItsHelpersOnly(t *testing.T) {
	listed := []DemoRepository{
		{Name: "demo-service-release-engine", ID: 3},
		{Name: "demo-service", ID: 1},
		{Name: "demo-service-forge-ci", ID: 2},
		{Name: "demo-other", ID: 4},
		{Name: "fci-consumer-trial", ID: 5},
		{Name: "demo-", ID: 6},
		{Name: "service", ID: 7},
	}

	want := []DemoSet{
		{Name: "other", Repositories: []DemoRepository{{Name: "demo-other", ID: 4}}},
		{Name: "service", Repositories: []DemoRepository{
			{Name: "demo-service", ID: 1}, {Name: "demo-service-forge-ci", ID: 2}, {Name: "demo-service-release-engine", ID: 3},
		}},
	}

	if got := GroupDemos(listed); !reflect.DeepEqual(got, want) {
		t.Fatalf("GroupDemos() = %+v\nwant %+v", got, want)
	}
}

// A repository is deleted only while the forge still gives its name the ID the
// listing showed: one replaced since is refused, one already gone is skipped.
func TestDeleteListed_DeletesOnlyTheListedIdentity(t *testing.T) {
	listed := DemoRepository{Name: "demo-service", ID: 7}

	for name, test := range map[string]struct {
		current int64
		found   bool
		deleted bool
		refused bool
	}{
		"same identity":   {current: 7, found: true, deleted: true},
		"replaced since":  {current: 8, found: true, refused: true},
		"already deleted": {},
	} {
		t.Run(name, func(t *testing.T) {
			var removed []string

			lookup := func(context.Context, string) (int64, bool, error) { return test.current, test.found, nil }
			remove := func(_ context.Context, repo string) error {
				removed = append(removed, repo)

				return nil
			}

			err := deleteListed(t.Context(), demoAccount, listed, lookup, remove)
			if test.refused != errors.Is(err, errs.ErrValidation) || (!test.refused && err != nil) {
				t.Fatalf("deleteListed() = %v, want refused %v", err, test.refused)
			}

			if test.deleted != (len(removed) == 1) {
				t.Fatalf("removed %v, want deleted %v", removed, test.deleted)
			}
		})
	}
}

// Only the forge's own link makes a package a repository's: one linked to
// another repository, and one linked to none under the demo's name, are not.
func TestLinkedPackages_TakesOnlyWhatTheForgeLinksToTheRepository(t *testing.T) {
	got := linkedPackages([]OwnerPackage{
		{ID: 1, Type: "container", Name: "demo-service", Version: "v1", RepositoryID: 7},
		{ID: 2, Type: "maven", Name: "org.example:service", Version: "1.0.0", RepositoryID: 7},
		{ID: 3, Type: "npm", Name: "demo-service", Version: "1.0.0"},
		{ID: 4, Type: "container", Name: "demo-other", Version: "v1", RepositoryID: 8},
	}, 7)

	want := []DemoPackage{
		{Type: "container", Name: "demo-service", Version: "v1", ID: 1},
		{Type: "maven", Name: "org.example:service", Version: "1.0.0", ID: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("linkedPackages() = %+v\nwant %+v", got, want)
	}

	if got := linkedPackages([]OwnerPackage{{ID: 3, Type: "npm", Name: "demo-service"}}, 0); got != nil {
		t.Fatalf("a repository without an ID took %+v", got)
	}
}

// A listed package version is deleted only while the forge still lists that
// ID as that version linked to that repository, and the deletion counts only
// when a second listing shows it gone.
func TestDeleteLinkedPackages_DeletesOnlyTheListedIdentityAndConfirmsIt(t *testing.T) {
	repository := DemoRepository{Name: "demo-service", ID: 7, Packages: []DemoPackage{
		{Type: "container", Name: "demo-service", Version: "v1", ID: 1},
		{Type: "maven", Name: "org.example:service", Version: "1.0.0", ID: 2},
	}}
	linked := []OwnerPackage{
		{ID: 1, Type: "container", Name: "demo-service", Version: "v1", RepositoryID: 7},
		{ID: 2, Type: "maven", Name: "org.example:service", Version: "1.0.0", RepositoryID: 7},
		{ID: 3, Type: "npm", Name: "demo-service", Version: "1.0.0"},
	}

	for name, test := range map[string]struct {
		current []OwnerPackage
		kept    bool
		removed int
		want    error
	}{
		"linked as listed":   {current: linked, removed: 2},
		"already deleted":    {current: linked[1:], removed: 1},
		"linked elsewhere":   {current: []OwnerPackage{{ID: 1, Type: "container", Name: "demo-service", Version: "v1", RepositoryID: 8}}, want: errPackageIdentityDrift},
		"unlinked since":     {current: []OwnerPackage{{ID: 1, Type: "container", Name: "demo-service", Version: "v1"}}, want: errPackageIdentityDrift},
		"another version":    {current: []OwnerPackage{{ID: 1, Type: "container", Name: "demo-service", Version: "v2", RepositoryID: 7}}, want: errPackageIdentityDrift},
		"deletion not shown": {current: linked, kept: true, removed: 2, want: errPackageDeletionUnconfirmed},
	} {
		t.Run(name, func(t *testing.T) {
			current := slices.Clone(test.current)

			var removed []int64

			list := func(context.Context) ([]OwnerPackage, error) { return current, nil }
			remove := func(_ context.Context, pkg DemoPackage) error {
				removed = append(removed, pkg.ID)

				if !test.kept {
					current = slices.DeleteFunc(current, func(listed OwnerPackage) bool { return listed.ID == pkg.ID })
				}

				return nil
			}

			err := deleteLinkedPackages(t.Context(), repository, list, remove)
			if !errors.Is(err, test.want) || (test.want == nil && err != nil) {
				t.Fatalf("deleteLinkedPackages() = %v, want %v", err, test.want)
			}

			if len(removed) != test.removed {
				t.Fatalf("removed %v, want %d", removed, test.removed)
			}

			if slices.Contains(removed, 3) {
				t.Fatalf("the unlinked package was deleted: %v", removed)
			}
		})
	}
}

// The packages go before their repository, and a package that stays keeps
// its repository and every later one.
func TestDeleteWithPackages_LeavesTheRepositoryOfAPackageThatStays(t *testing.T) {
	repositories := []DemoRepository{{Name: "demo-service"}, {Name: "demo-service-forge-ci"}}

	for name, failing := range map[string]string{"all deleted": "", "a package stays": "demo-service"} {
		t.Run(name, func(t *testing.T) {
			var log []string

			packages := func(_ context.Context, repository DemoRepository) error {
				log = append(log, "packages "+repository.Name)

				if repository.Name == failing {
					return errPackageDeletionUnconfirmed
				}

				return nil
			}
			repository := func(_ context.Context, repository DemoRepository) error {
				log = append(log, "repository "+repository.Name)

				return nil
			}

			err := deleteWithPackages(t.Context(), repositories, packages, repository)

			want := []string{"packages demo-service", "repository demo-service", "packages demo-service-forge-ci", "repository demo-service-forge-ci"}
			if failing != "" {
				want = want[:1]
			}

			if (failing != "") != errors.Is(err, errPackageDeletionUnconfirmed) || !slices.Equal(log, want) {
				t.Fatalf("deleteWithPackages() = %v, did %v\nwant %v", err, log, want)
			}
		})
	}
}
