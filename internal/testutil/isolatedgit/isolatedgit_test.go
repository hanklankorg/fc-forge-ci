//go:build integration

// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package isolatedgit_test

import (
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/isolatedgit"
)

func TestRepos_AreIsolatedFromEachOther(t *testing.T) {
	t.Parallel()

	a := isolatedgit.NewRepo(t)
	b := isolatedgit.NewRepo(t)
	if a.Dir == b.Dir {
		t.Errorf("two NewRepo() calls returned the same directory: %q", a.Dir)
	}
}

// TestNewRepo_ConfiguresItsOwnIdentityAndSigningLocally pins the repository
// state the rest of the suite relies on: a test identity and signing switched
// off, set in THIS repository's config rather than inherited from anywhere.
func TestNewRepo_ConfiguresItsOwnIdentityAndSigningLocally(t *testing.T) {
	t.Parallel()

	r := isolatedgit.NewRepo(t)

	for key, want := range map[string]string{
		"user.name":      "Test Bot",
		"user.email":     "bot@example.invalid",
		"commit.gpgsign": "false",
		"tag.gpgsign":    "false",
	} {
		if got := r.Git("config", "--local", "--get", key); got != want {
			t.Errorf("local %s = %q, want %q", key, got, want)
		}
	}

	if got := r.Git("rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Errorf("branch = %q, want main", got)
	}
}
