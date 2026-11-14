// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

var (
	errConsumerTreeEntry      = errors.New("invalid consumer tree entry")
	errControlPathNotRegular  = errors.New("consumer control path is not regular")
	errControlDirectoryIsFile = errors.New("consumer control directory is a file")
	errGitHubHistoryNeedsSHA1 = errors.New("GitHub history requires SHA-1; SHA-256 supports snapshot import only")
	errSHA256Submodule        = errors.New("cannot reencode SHA-256 submodule")
	errWorkflowNotRegularBlob = errors.New("workflow must be a regular tracked blob")
)

func githubStageGit() layerGit {
	return layerGit{env: []string{
		"PATH=/usr/bin:/bin", "GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0",
		"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=core.hooksPath", "GIT_CONFIG_VALUE_0=" + os.DevNull,
		"GIT_CONFIG_KEY_1=core.fsmonitor", "GIT_CONFIG_VALUE_1=false",
	}}
}

// githubStage imports Git objects, never an archive or a source worktree path.
// Both directories are fresh, caller-owned temporary directories. Rehashing
// blobs explicitly supports SHA-256 consumers at GitHub's SHA-1 boundary.
func githubStage(ctx context.Context, git layerGit, root, bare, dir string, history bool) (Consumer, Consumer, error) {
	source, err := openConsumer(ctx, git, root, bare)
	if err != nil {
		return Consumer{}, Consumer{}, err
	}

	listed, err := git.output(ctx, bare, nil, "ls-tree", "-rz", source.Commit)
	if err != nil {
		return Consumer{}, Consumer{}, err
	}

	entries := strings.Split(strings.TrimSuffix(string(listed), "\x00"), "\x00")
	// Validate every control entry before writing anything to the destination.
	if err := validateGitHubControlEntries(entries); err != nil {
		return Consumer{}, Consumer{}, err
	}

	if history && source.ObjectFormat != objectFormatSHA1 {
		return Consumer{}, Consumer{}, errGitHubHistoryNeedsSHA1
	}

	if _, err = git.run(ctx, dir, "init", "--quiet", "--template=", "--object-format=sha1", "--initial-branch=main"); err != nil {
		return Consumer{}, Consumer{}, err
	}

	if _, err = git.run(ctx, dir, "config", "core.hooksPath", os.DevNull); err != nil {
		return Consumer{}, Consumer{}, err
	}

	staged := Consumer{git: git, stage: dir}
	if history {
		if _, err = git.run(ctx, dir, "fetch", "--quiet", bare, source.Commit); err != nil {
			return Consumer{}, Consumer{}, err
		}

		if _, err = git.run(ctx, dir, "update-ref", "refs/heads/main", source.Commit); err != nil {
			return Consumer{}, Consumer{}, err
		}
	}

	if err = stageGitHubEntries(ctx, git, source, staged, bare, dir, entries); err != nil {
		return Consumer{}, Consumer{}, err
	}

	return source, staged, nil
}

// stageGitHubEntries writes every non-workflow entry into the SHA-1 index,
// rehashing blobs and keeping SHA-1 submodule commits as gitlinks.
func stageGitHubEntries(ctx context.Context, git layerGit, source, staged Consumer, bare, dir string, entries []string) error {
	var err error

	for _, entry := range entries {
		header, path, _ := strings.Cut(entry, "\t")
		fields := strings.Fields(header)

		if strings.HasPrefix(path, ".github/workflows/") {
			continue
		}

		if fields[1] == "commit" {
			if source.ObjectFormat != objectFormatSHA1 {
				return fmt.Errorf("%w: %s", errSHA256Submodule, path)
			}

			_, err = git.run(ctx, dir, "update-index", "--add", "--cacheinfo", fields[0]+","+fields[2]+","+path)
		} else {
			var body []byte

			body, err = git.output(ctx, bare, nil, "cat-file", "blob", fields[2])
			if err == nil {
				err = staged.stageFile(ctx, fields[0], path, string(body))
			}
		}

		if err != nil {
			return err
		}
	}

	return nil
}

// validateGitHubControlEntries refuses malformed tree entries and any GitHub
// control path that is not a regular file.
func validateGitHubControlEntries(entries []string) error {
	for _, entry := range entries {
		header, path, ok := strings.Cut(entry, "\t")

		fields := strings.Fields(header)
		if !ok || len(fields) != 3 {
			return errConsumerTreeEntry
		}

		if path == ".github" || path == githubWorkflowDirectory || strings.HasPrefix(path, ".github/workflows/") || path == consumerCanary {
			if fields[0] != "100644" && fields[0] != "100755" {
				return fmt.Errorf("%w: %s", errControlPathNotRegular, path)
			}

			if path == ".github" || path == githubWorkflowDirectory {
				return fmt.Errorf("%w: %s", errControlDirectoryIsFile, path)
			}
		}
	}

	return nil
}

// githubWorkflow reads only a regular, tracked blob at the admitted revision.
func githubWorkflow(ctx context.Context, source Consumer, path string) (string, error) {
	entry, err := source.git.output(ctx, source.stage, nil, "ls-tree", "-z", source.Commit, "--", path)
	if err != nil {
		return "", err
	}

	header, actual, ok := strings.Cut(strings.TrimSuffix(string(entry), "\x00"), "\t")

	fields := strings.Fields(header)
	if !ok || actual != path || len(fields) != 3 || (fields[0] != "100644" && fields[0] != "100755") {
		return "", fmt.Errorf("%w: %s", errWorkflowNotRegularBlob, path)
	}

	body, err := source.git.output(ctx, source.stage, nil, "cat-file", "blob", fields[2])

	return string(body), err
}
