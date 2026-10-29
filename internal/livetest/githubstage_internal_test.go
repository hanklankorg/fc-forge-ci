// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stageTestRepo(t *testing.T, format string) (string, layerGit) {
	t.Helper()
	root := t.TempDir()
	git := githubStageGit()
	git.env = append(git.env, "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	stageTestGit(t, git, root, "init", "--quiet", "--template=", "--object-format="+format)
	return root, git
}

func stageTestGit(t *testing.T, git layerGit, dir string, args ...string) string {
	t.Helper()
	out, err := git.run(t.Context(), dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func stageTestWrite(t *testing.T, root, name, body string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func stageTestCommit(t *testing.T, git layerGit, root string) {
	t.Helper()
	stageTestGit(t, git, root, "add", "--all")
	stageTestGit(t, git, root, "commit", "--quiet", "-m", "fixture")
}

func TestGitHubStageRejectsControlLinksWithoutTouchingSource(t *testing.T) {
	for _, path := range []string{".github", ".github/workflows", ".github/workflows/test.yml", consumerCanary} {
		t.Run(path, func(t *testing.T) {
			root, git := stageTestRepo(t, "sha1")
			outside := t.TempDir()
			stageTestWrite(t, outside, "workflows/test.yml", "private sentinel", 0600)
			link := filepath.Join(root, path)
			if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
				t.Fatal(err)
			}
			target := outside
			if strings.HasSuffix(path, ".yml") || path == consumerCanary {
				target = filepath.Join(outside, "workflows/test.yml")
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			stageTestCommit(t, git, root)
			before := stageTestGit(t, git, root, "rev-parse", "HEAD", "HEAD^{tree}")
			index, err := os.ReadFile(filepath.Join(root, ".git/index"))
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = githubStage(t.Context(), git, root, t.TempDir(), t.TempDir(), false)
			if err == nil {
				t.Fatal("control symlink admitted")
			}
			body, err := os.ReadFile(filepath.Join(outside, "workflows/test.yml"))
			if err != nil || string(body) != "private sentinel" {
				t.Fatal("outside sentinel changed")
			}
			if got := stageTestGit(t, git, root, "rev-parse", "HEAD", "HEAD^{tree}"); got != before {
				t.Fatal("source refs changed")
			}
			after, err := os.ReadFile(filepath.Join(root, ".git/index"))
			if err != nil || string(index) != string(after) {
				t.Fatal("source index changed")
			}
			if stageTestGit(t, git, root, "status", "--porcelain", "--untracked-files=all") != "" {
				t.Fatal("source changed")
			}
		})
	}
}

func TestGitHubStagePreservesBlobsModesAndExportAttributes(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			root, git := stageTestRepo(t, format)
			stageTestWrite(t, root, ".gitattributes", "keep export-ignore\nsubst export-subst\n", 0644)
			stageTestWrite(t, root, "keep", "all tracked content", 0755)
			stageTestWrite(t, root, "subst", "$Format:%H$", 0644)
			stageTestWrite(t, root, "tab\tand\nnewline", "NUL parsed name", 0644)
			stageTestWrite(t, root, ".github/workflows/test.yml", "name: original\n", 0644)
			if err := os.Symlink("/nonexistent-private-sentinel", filepath.Join(root, "data-link")); err != nil {
				t.Fatal(err)
			}
			stageTestCommit(t, git, root)
			source, staged, err := githubStage(t.Context(), git, root, t.TempDir(), t.TempDir(), false)
			if err != nil {
				t.Fatal(err)
			}
			workflow, err := githubWorkflow(t.Context(), source, ".github/workflows/test.yml")
			if err != nil || workflow != "name: original\n" {
				t.Fatal("workflow blob changed", err)
			}
			for _, path := range []string{".gitattributes", "keep", "subst", "tab\tand\nnewline", "data-link"} {
				want, err := git.output(t.Context(), source.stage, nil, "show", source.Commit+":"+path)
				if err != nil {
					t.Fatal(err)
				}
				got, err := git.output(t.Context(), staged.stage, nil, "show", ":"+path)
				if err != nil || string(got) != string(want) {
					t.Fatalf("blob changed: %q: %v", path, err)
				}
			}
			if !strings.HasPrefix(stageTestGit(t, git, staged.stage, "ls-files", "--stage", "--", "keep"), "100755 ") {
				t.Fatal("executable mode lost")
			}
			if !strings.HasPrefix(stageTestGit(t, git, staged.stage, "ls-files", "--stage", "--", "data-link"), "120000 ") {
				t.Fatal("data symlink lost")
			}
			if stageTestGit(t, git, staged.stage, "ls-files", "--", ".github/workflows") != "" {
				t.Fatal("original workflows not removed from private index")
			}
		})
	}
}

func TestGitHubStageConsumerUsesObjectSnapshot(t *testing.T) {
	for _, history := range []bool{false, true} {
		root, git := stageTestRepo(t, "sha1")
		stageTestWrite(t, root, "fixtures/workflows/pullrequest.yml", fixturePullRequest, 0644)
		stageTestWrite(t, root, ".gitattributes", "keep export-ignore\n", 0644)
		stageTestWrite(t, root, "keep", "retained", 0755)
		stageTestCommit(t, git, root)
		trial := GitHubTrial{tb: t, history: history}
		dir, _ := trial.stageConsumer(root, "fixtures", trialEngine, nil)
		if got := stageTestGit(t, git, dir, "show", "HEAD:keep"); got != "retained" {
			t.Fatal("existing trial lost content")
		}
		if stageTestGit(t, git, root, "status", "--porcelain") != "" {
			t.Fatal("source changed")
		}
	}
}

func TestGitHubStageRefusesWholeSourceChanges(t *testing.T) {
	for _, dirty := range []string{"tracked", "untracked"} {
		t.Run(dirty, func(t *testing.T) {
			root, git := stageTestRepo(t, "sha1")
			stageTestWrite(t, root, "tracked", "original", 0644)
			stageTestCommit(t, git, root)
			stageTestWrite(t, root, dirty, "must remain untouched", 0644)
			if _, _, err := githubStage(t.Context(), git, root, t.TempDir(), t.TempDir(), false); err == nil {
				t.Fatal("dirty source admitted")
			}
			body, err := os.ReadFile(filepath.Join(root, dirty))
			if err != nil || string(body) != "must remain untouched" {
				t.Fatal("source changed")
			}
		})
	}
}
