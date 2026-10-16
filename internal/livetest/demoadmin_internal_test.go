// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// A demo is its own repository and the helpers named after it; nothing else in
// the account is taken for part of one, so a deletion takes exactly its set.
func TestGroupDemos_TakesEachDemoAndItsHelpersOnly(t *testing.T) {
	listed := []DemoRepository{
		{Name: "demo-wallet-release-engine", ID: 3},
		{Name: "demo-wallet", ID: 1},
		{Name: "demo-wallet-forge-ci", ID: 2},
		{Name: "demo-other", ID: 4},
		{Name: "fci-consumer-trial", ID: 5},
		{Name: "demo-", ID: 6},
		{Name: "wallet", ID: 7},
	}

	want := []DemoSet{
		{Name: "other", Repositories: []DemoRepository{{Name: "demo-other", ID: 4}}},
		{Name: "wallet", Repositories: []DemoRepository{
			{Name: "demo-wallet", ID: 1}, {Name: "demo-wallet-forge-ci", ID: 2}, {Name: "demo-wallet-release-engine", ID: 3},
		}},
	}

	if got := GroupDemos(listed); !reflect.DeepEqual(got, want) {
		t.Fatalf("GroupDemos() = %+v\nwant %+v", got, want)
	}
}

// A repository is deleted only while the forge still gives its name the ID the
// listing showed: one replaced since is refused, one already gone is skipped.
func TestDeleteListed_DeletesOnlyTheListedIdentity(t *testing.T) {
	listed := DemoRepository{Name: "demo-wallet", ID: 7}

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
