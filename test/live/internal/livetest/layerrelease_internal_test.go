// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// A merge commit of the base and the change, in that order, with the change's
// tree passes; the parents reversed, a single parent, or another tree refuse.
func TestRepoMirror_MergeOfHoldsParentsAndTree(t *testing.T) {
	t.Parallel()

	root, git := stageTestRepo(t, "sha1")
	stageTestWrite(t, root, "base.txt", "base\n", 0o600)
	stageTestCommit(t, git, root)
	base := mustLayerGit(t, git, root, "rev-parse", "HEAD")

	stageTestWrite(t, root, "change.txt", "change\n", 0o600)
	stageTestCommit(t, git, root)
	head := mustLayerGit(t, git, root, "rev-parse", "HEAD")

	headTree := mustLayerGit(t, git, root, "rev-parse", head+"^{tree}")
	baseTree := mustLayerGit(t, git, root, "rev-parse", base+"^{tree}")
	mirror := RepoMirror{dir: root, git: git}

	merge := mustLayerGit(t, git, root, "commit-tree", headTree, "-p", base, "-p", head, "-m", "merge")
	if err := mirror.mergeOf(t.Context(), merge, base, head); err != nil {
		t.Fatalf("a merge of the base and the change was refused: %v", err)
	}

	for name, commit := range map[string]string{
		"parents reversed": mustLayerGit(t, git, root, "commit-tree", headTree, "-p", head, "-p", base, "-m", "reversed"),
		"the change alone": head,
		"the base's tree":  mustLayerGit(t, git, root, "commit-tree", baseTree, "-p", base, "-p", head, "-m", "undone"),
	} {
		if err := mirror.mergeOf(t.Context(), commit, base, head); !errors.Is(err, errs.ErrValidation) {
			t.Errorf("%s: mergeOf = %v, want a validation refusal", name, err)
		}
	}
}
