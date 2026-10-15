// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

const (
	gitAdd           = "add"
	gitCommitCommand = "commit"
)

// ObjectFormatFixture has one annotated tag followed by one additional commit.
// A checkout of the tag must preserve the algorithm, tag object and exact bytes.
type ObjectFormatFixture struct {
	Tag, TagCommit, TagObject, HeadCommit string
}

// SeedObjectFormatFixture uses native Git in an isolated worktree to publish a
// small same-format history to a previously admitted bare scratch repository.
func SeedObjectFormatFixture(tb TB, target Target, repo, format, directory string) ObjectFormatFixture {
	tb.Helper()
	requireAccepted(tb, target)
	gitBase := strings.TrimSuffix(target.endpoint.GitURL(), "/")
	git := layerGit{target: target, env: ToolEnvironment(tb, target, layerGitEnvironment(gitBase, target.CredentialUsername, target.Token))}

	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()

	facts, err := seedObjectFormatFixture(ctx, git, directory, format)
	if err == nil {
		_, err = git.run(ctx, directory, "push", gitQuiet, scratchRemote(target, repo), "refs/heads/main:refs/heads/main", "refs/tags/"+facts.Tag+":refs/tags/"+facts.Tag)
	}

	if err != nil {
		tb.Fatalf("livetest: seed %s object-format fixture: %v", format, err)
	}

	return facts
}

func seedObjectFormatFixture(ctx context.Context, git layerGit, directory, format string) (ObjectFormatFixture, error) {
	if format != "sha1" && format != "sha256" {
		return ObjectFormatFixture{}, fmt.Errorf("unsupported fixture object format %q: %w", format, errs.ErrValidation)
	}

	if _, err := git.run(ctx, directory, "init", gitQuiet, "--initial-branch=main", "--object-format="+format); err != nil {
		return ObjectFormatFixture{}, err
	}

	facts := ObjectFormatFixture{Tag: "v0.0.1-format"}

	var err error

	facts.TagCommit, err = commitObjectFormatFile(ctx, git, directory, "README.md", "Object-format acceptance.\n")
	if err != nil {
		return facts, err
	}

	if _, err = git.run(ctx, directory, "tag", "-a", facts.Tag, "-m", "Object-format fixture"); err != nil {
		return facts, err
	}

	facts.TagObject, err = git.run(ctx, directory, "rev-parse", "refs/tags/"+facts.Tag)
	if err != nil {
		return facts, err
	}

	facts.HeadCommit, err = commitObjectFormatFile(ctx, git, directory, "after-tag.txt", "This file must not appear at the tag.\n")

	return facts, err
}

func commitObjectFormatFile(ctx context.Context, git layerGit, directory, file, content string) (string, error) {
	if err := os.WriteFile(filepath.Join(directory, file), []byte(content), 0o600); err != nil {
		return "", err
	}

	for _, args := range [][]string{{gitAdd, file}, {gitCommitCommand, gitQuiet, "-m", "test: add " + file}} {
		if _, err := git.run(ctx, directory, args...); err != nil {
			return "", err
		}
	}

	return git.run(ctx, directory, "rev-parse", "HEAD")
}
