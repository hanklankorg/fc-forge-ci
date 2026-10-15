//go:build integration

// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package git_test

import (
	"os"
	"path/filepath"
	"testing"

	adaptergit "github.com/diggsweden/forge-ci/internal/adapters/git"
	"github.com/diggsweden/forge-ci/internal/runcontext"
	"github.com/diggsweden/forge-ci/internal/testutil/isolatedgit"
)

// TestRemoteBranchCommit_ReadsExactlyTheNamedBranchFromTheRemote is the native
// half of the pre-mutation identity check that CommitPush makes before it
// commits. ls-remote patterns match trailing path components, so asking for
// refs/heads/main also lists a branch named a/refs/heads/main, which sorts
// first; only the exact ref comparison keeps it out. A tag named main and a
// local main that has moved on are on other commits too, so only a read of
// exactly refs/heads/main on the remote answers with the published commit. An absent branch reports not found rather than an error or an empty
// match.
func TestRemoteBranchCommit_ReadsExactlyTheNamedBranchFromTheRemote(t *testing.T) {
	t.Parallel()

	repo := isolatedgit.NewRepo(t)
	remote := repo.AddBareRemote()
	published := repo.HeadSHA()

	decoy := repo.AddCommit("chore: decoy branch")
	repo.Git("push", "-q", remote, decoy+":refs/heads/a/refs/heads/main")

	tagged := repo.AddCommit("chore: tag named main")
	repo.Git("push", "-q", remote, tagged+":refs/tags/main")

	repo.AddCommit("chore: local main moved, not published")

	adapter := &adaptergit.Repo{Dir: repo.Dir}

	sha, exists, err := adapter.RemoteBranchCommit(t.Context(), "origin", "main", runcontext.Credential{})
	if err != nil || !exists || sha != published {
		t.Fatalf("RemoteBranchCommit(main) = %q, %v, %v; want the published %s", sha, exists, err, published)
	}

	sha, exists, err = adapter.RemoteBranchCommit(t.Context(), "origin", "release/9.x", runcontext.Credential{})
	if err != nil || exists || sha != "" {
		t.Fatalf("RemoteBranchCommit(release/9.x) = %q, %v, %v; want not found", sha, exists, err)
	}
}

func TestRemoteBranchCommit_UsesTheSelectedForeignOwnedWorkspace(t *testing.T) {
	repo := isolatedgit.NewRepo(t)
	remote := repo.AddBareRemote()
	published := repo.HeadSHA()
	repo.Git("tag", "v1.0.0")
	repo.Git("push", "-q", remote, "refs/tags/v1.0.0")
	// Git's own test switch models a mounted checkout owned by the runner's
	// helper UID, without changing host ownership or trusting every directory.
	t.Setenv("GIT_TEST_ASSUME_DIFFERENT_OWNER", "1")
	config := filepath.Join(t.TempDir(), "remote.gitconfig")
	if err := os.WriteFile(config, []byte("[safe]\n\tdirectory = "+remote+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", config)
	adapter := &adaptergit.Repo{Dir: repo.Dir}
	if sha, err := adapter.RevParse(t.Context(), "HEAD"); err != nil || sha != published {
		t.Fatalf("foreign-owned selected HEAD: %q, %v", sha, err)
	}
	for _, credential := range []runcontext.Credential{{}, runcontext.OperatorCredential("inert-fixture")} {
		sha, exists, err := adapter.RemoteBranchCommit(t.Context(), "origin", "main", credential)
		if err != nil || !exists || sha != published {
			t.Fatalf("foreign-owned selected workspace: %q, %v, %v", sha, exists, err)
		}
		sha, exists, err = adapter.RemoteTagCommitIfExists(t.Context(), "origin", "v1.0.0", credential)
		if err != nil || !exists || sha != published {
			t.Fatalf("foreign-owned remote tag probe: %q, %v, %v", sha, exists, err)
		}
		sha, err = adapter.RemoteTagCommit(t.Context(), "origin", "v1.0.0", credential)
		if err != nil || sha != published {
			t.Fatalf("foreign-owned required remote tag: %q, %v", sha, err)
		}
	}
}
