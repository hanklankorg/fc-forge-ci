// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package git_test

import (
	"slices"
	"testing"

	adaptergit "github.com/diggsweden/forge-ci/internal/adapters/git"
	"github.com/diggsweden/forge-ci/internal/runcontext"
	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
)

func TestRemoteReleaseTags_PreservesBothNamesAndExistingVersionQuery(t *testing.T) {
	t.Parallel()

	mock := mockbinary.New(t)
	mock.Add("git", `
case "$*" in
  'ls-remote --tags --refs -- https://example.invalid/engine refs/tags/v* refs/tags/[0-9]*')
    printf 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\trefs/tags/1.2.3\nbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\trefs/tags/v1.2.3\n'
    ;;
  'ls-remote --tags --refs -- https://example.invalid/engine refs/tags/v*')
    printf 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\trefs/tags/v1.2.3\n'
    ;;
  *) exit 1 ;;
esac
`)
	repo := &adaptergit.Repo{GitBin: mock.Path("git")}

	tags, err := repo.RemoteReleaseTags(t.Context(), "https://example.invalid/engine", runcontext.Credential{})
	if err != nil || !slices.Equal(tags, []string{"1.2.3", "v1.2.3"}) {
		t.Fatalf("initializer candidates = %v, %v; invocations: %+v", tags, err, mock.Invocations("git"))
	}

	tags, err = repo.RemoteVersionTags(t.Context(), "https://example.invalid/engine", runcontext.Credential{})
	if err != nil || !slices.Equal(tags, []string{"v1.2.3"}) {
		t.Fatalf("existing version query changed = %v, %v", tags, err)
	}
}
