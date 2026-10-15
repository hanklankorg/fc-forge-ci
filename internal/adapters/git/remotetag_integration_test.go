//go:build integration

// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package git_test

import (
	"testing"

	adaptergit "github.com/diggsweden/forge-ci/internal/adapters/git"
	"github.com/diggsweden/forge-ci/internal/runcontext"
	"github.com/diggsweden/forge-ci/internal/testutil/isolatedgit"
)

// ls-remote patterns also match trailing ref components. A different tag ending
// in refs/tags/v1.2.3 must not prevent the release from creating v1.2.3.
func TestRemoteTagReads_RequireTheExactRef(t *testing.T) {
	t.Parallel()

	remote := isolatedgit.NewRepo(t)
	remote.AddTag("archive/refs/tags/v1.2.3", "suffix collision")
	remote.AddTag("v2.0.0", "annotated positive control")
	remote.Git("tag", "v2.0.1")

	// Git can serve this disposable working repository directly; no push or
	// network is needed to exercise its real advertisement and matching.
	repo := &adaptergit.Repo{Dir: remote.Dir}
	for _, test := range []struct {
		tag  string
		want bool
	}{
		{tag: "v1.2.3"},
		{tag: "v2.0.0", want: true},
		{tag: "v2.0.1", want: true},
		{tag: "v3.0.0"},
	} {
		t.Run(test.tag, func(t *testing.T) {
			exists, err := repo.RemoteTagExists(t.Context(), remote.Dir, test.tag, runcontext.Credential{})
			if err != nil || exists != test.want {
				t.Errorf("RemoteTagExists = %v, %v; want %v, nil", exists, err, test.want)
			}

			object, exists, err := repo.RemoteTagObjectIfExists(t.Context(), remote.Dir, test.tag, runcontext.Credential{})
			if err != nil || exists != test.want {
				t.Fatalf("RemoteTagObjectIfExists = %q, %v, %v; want exists=%v", object, exists, err, test.want)
			}

			wantObject := ""
			if test.want {
				wantObject = remote.Git("rev-parse", "refs/tags/"+test.tag)
			}

			if object != wantObject {
				t.Errorf("remote object = %q, want %q", object, wantObject)
			}
		})
	}
}
