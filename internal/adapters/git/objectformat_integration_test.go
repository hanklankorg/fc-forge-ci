//go:build integration

// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package git_test

import (
	"testing"

	adaptergit "github.com/diggsweden/forge-ci/internal/adapters/git"
)

// The changelog renderer refuses git-cliff on a SHA-256 repository, so the
// reported format must be the one the repository was created with.
func TestObjectFormat_ReportsTheRepositoryFormat(t *testing.T) {
	t.Parallel()

	for _, want := range []string{"sha1", "sha256"} {
		repo := &adaptergit.Repo{Dir: t.TempDir()}
		if err := repo.InitWithObjectFormat(t.Context(), want); err != nil {
			t.Fatal(err)
		}

		got, err := repo.ObjectFormat(t.Context())
		if err != nil {
			t.Fatal(err)
		}

		if got != want {
			t.Errorf("ObjectFormat = %q, want %q", got, want)
		}
	}
}
