// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package genericcli_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Only synthetic repositories are written. No application, forge or compiler is
// invoked; commit-tree creates disposable fixture objects, never project commits.
func TestPrepareMaterializesPrivateSnapshot(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			kind := map[string]string{"sha1": "go", "sha256": "cargo"}[format]
			root := t.TempDir()
			source, destination := filepath.Join(root, "source"), filepath.Join(root, "prepared")
			env := append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@localhost", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@localhost")
			run := func(input string, args ...string) string {
				t.Helper()
				cmd := exec.CommandContext(t.Context(), "git", args...)
				cmd.Env, cmd.Stdin = env, strings.NewReader(input)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			run("", "init", "--quiet", "--object-format="+format, source)
			fixtureBody := "# SPDX-FileCopyrightText: Fixture Authors\n# SPDX-License-Identifier: CC0-1.0\nfixture source\n"
			blob := run(fixtureBody, "-C", source, "hash-object", "-w", "--stdin")
			license, err := os.ReadFile("../../../../LICENSES/CC0-1.0.txt")
			if err != nil {
				t.Fatal(err)
			}
			licenseBlob := run(string(license), "-C", source, "hash-object", "-w", "--stdin")
			run("", "-C", source, "update-index", "--add", "--cacheinfo", "100644,"+licenseBlob+",LICENSES/CC0-1.0.txt")
			for _, entry := range []string{"100644," + blob + ",README.md", "100755," + blob + ",executable.sh"} {
				run("", "-C", source, "update-index", "--add", "--cacheinfo", entry)
			}
			link := run("README.md", "-C", source, "hash-object", "-w", "--stdin")
			run("", "-C", source, "update-index", "--add", "--cacheinfo", "120000,"+link+",source-link")
			tree := run("", "-C", source, "write-tree")
			commit := run("fixture\n", "-C", source, "commit-tree", tree)
			run("", "-C", source, "update-ref", "HEAD", commit)
			run("", "-C", source, "checkout", "--force", "--detach", commit)
			before := run("", "-C", source, "ls-files", "--stage")
			objectsBefore := run("", "-C", source, "count-objects", "-v")
			prepare := func(dest, expected string) ([]byte, error) {
				cmd := exec.CommandContext(t.Context(), "bash", "prepare.sh", source, dest, kind, expected)
				cmd.Env = env
				return cmd.CombinedOutput()
			}
			if out, err := prepare(destination, commit); err != nil {
				t.Fatalf("prepare: %v\n%s", err, out)
			}
			if got := run("", "-C", destination, "status", "--porcelain", "--untracked-files=all"); got != "" {
				t.Fatalf("snapshot not materialized/clean: %s", got)
			}
			if got := run("", "-C", destination, "rev-parse", "--show-object-format"); got != format {
				t.Fatal(got)
			}
			for _, path := range []string{"README.md", "executable.sh", "source-link"} {
				if run("", "-C", source, "ls-tree", commit, "--", path) != run("", "-C", destination, "ls-tree", "HEAD", "--", path) {
					t.Fatalf("changed source entry %s", path)
				}
			}
			if body, err := os.ReadFile(filepath.Join(destination, "README.md")); err != nil || string(body) != fixtureBody {
				t.Fatalf("source not materialized: %q %v", body, err)
			}
			if info, err := os.Stat(filepath.Join(destination, "executable.sh")); err != nil || info.Mode().Perm()&0100 == 0 {
				t.Fatalf("executable mode lost: %v", err)
			}
			if target, err := os.Readlink(filepath.Join(destination, "source-link")); err != nil || target != "README.md" {
				t.Fatalf("symlink changed: %s %v", target, err)
			}
			if got := run("", "-C", source, "ls-files", "--stage"); got != before {
				t.Fatal("source index changed")
			}
			if got := run("", "-C", source, "status", "--porcelain", "--untracked-files=all"); got != "" {
				t.Fatal("source dirtied: " + got)
			}
			if run("", "-C", source, "rev-parse", "HEAD") != commit || run("", "-C", source, "count-objects", "-v") != objectsBefore {
				t.Fatal("source refs/objects changed")
			}
			for _, path := range strings.Split(run("", "-C", destination, "diff-tree", "--no-commit-id", "--name-only", "-r", commit, "HEAD"), "\n") {
				if !strings.HasPrefix(path, ".gitlab/") && !strings.HasPrefix(path, ".chglog/") {
					t.Fatalf("overlay escaped CI scope: %s", path)
				}
			}
			// Validate the generated output, not only the checked-in templates.
			if body, err := os.ReadFile(filepath.Join(destination, ".gitlab/generic-cli-kind")); err != nil || string(body) != kind+"\n" {
				t.Fatalf("machine-readable kind changed: %q %v", body, err)
			}
			marker, markerErr := os.ReadFile(filepath.Join(destination, ".gitlab/generic-cli-checks"))
			if kind == "go" {
				if markerErr != nil || string(marker) != "buildah-integration\n" {
					t.Fatalf("Go integration capability missing: %q %v", marker, markerErr)
				}
			} else if !os.IsNotExist(markerErr) {
				t.Fatalf("Cargo fixture acquired Go integration capability: %q %v", marker, markerErr)
			}
			identity, err := os.ReadFile(filepath.Join(destination, ".gitlab/generic-cli-source.json"))
			if err != nil {
				t.Fatal(err)
			}
			var decoded struct {
				Commit     string   `json:"commit"`
				Format     string   `json:"objectFormat"`
				LintPolicy string   `json:"lintPolicy"`
				Excluded   []string `json:"excludedChecks"`
			}
			if err := json.Unmarshal(identity, &decoded); err != nil || decoded.Commit != commit || decoded.Format != format || decoded.LintPolicy != "informational" || decoded.Excluded == nil || len(decoded.Excluded) != 0 {
				t.Fatalf("machine-readable identity changed: %s %v", identity, err)
			}
			for _, file := range []string{".gitlab/generic-cli-kind.license", ".gitlab/generic-cli-source.json.license", ".chglog/CHANGELOG-keepachangelog.tpl.license", ".chglog/CHANGELOG-minimal.tpl.license"} {
				if !strings.HasPrefix(run("", "-C", destination, "ls-tree", "HEAD", "--", file), "100644 blob ") {
					t.Fatalf("missing regular license sidecar %s", file)
				}
			}
			t.Run("quality-preflight", func(t *testing.T) {
				shfmt, reuse := fixtureTool(t, "shfmt"), fixtureTool(t, "reuse")
				for _, file := range []string{".gitlab/build-release.sh", ".gitlab/check-release.sh"} {
					cmd := exec.CommandContext(t.Context(), shfmt, "-d", filepath.Join(destination, file))
					if out, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("generated shell format: %v\n%s", err, out)
					}
				}
				lint := func() ([]byte, error) {
					return exec.CommandContext(t.Context(), reuse, "--root", destination, "lint").CombinedOutput()
				}
				if out, err := lint(); err != nil {
					t.Fatalf("generated overlay REUSE: %v\n%s", err, out)
				}
				// Reproduce the original missing metadata attribution failure.
				if err := os.Remove(filepath.Join(destination, ".gitlab/generic-cli-kind.license")); err != nil {
					t.Fatal(err)
				}
				if out, err := lint(); err == nil {
					t.Fatalf("REUSE accepted missing sidecar: %s", out)
				}
			})
			for _, dest := range []string{destination, filepath.Join(source, "nested")} {
				if out, err := prepare(dest, commit); err == nil {
					t.Fatalf("accepted unsafe destination %s: %s", dest, out)
				}
			}
			t.Run("forgejo-overlay", func(t *testing.T) {
				native := filepath.Join(root, "forgejo")
				cmd := exec.CommandContext(t.Context(), "bash", "prepare.sh", source, native, kind, commit, "forgejo")
				cmd.Env = env
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("Forgejo prepare: %v\n%s", err, out)
				}
				if run("", "-C", native, "status", "--porcelain") != "" || run("", "-C", native, "rev-parse", "--show-object-format") != format {
					t.Fatal("dirty/converted native snapshot")
				}
				for _, path := range strings.Split(run("", "-C", native, "diff-tree", "--no-commit-id", "--name-only", "-r", commit, "HEAD"), "\n") {
					if !strings.HasPrefix(path, ".gitlab/") && !strings.HasPrefix(path, ".chglog/") && path != ".forgejo/workflows/release.yml" {
						t.Fatalf("native overlay escaped CI scope: %s", path)
					}
				}
				workflow, err := os.ReadFile(filepath.Join(native, ".forgejo/workflows/release.yml"))
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(workflow), "ENGINE_SHA") || strings.Contains(string(workflow), "BUMP_IMAGE") || strings.Contains(string(workflow), "IS_GO") {
					t.Fatal("unrendered transport inputs")
				}
				if out, err := exec.CommandContext(t.Context(), fixtureTool(t, "reuse"), "--root", native, "lint").CombinedOutput(); err != nil {
					t.Fatalf("native overlay REUSE: %v\n%s", err, out)
				}
			})
			if out, err := prepare(filepath.Join(root, "wrong-commit"), strings.Repeat("0", len(commit))); err == nil {
				t.Fatalf("accepted wrong source: %s", out)
			}
			for _, flag := range []string{"--exclude-kics", "--exclude-security"} {
				cmd := exec.CommandContext(t.Context(), "bash", "prepare.sh", source, filepath.Join(root, "unadmitted-exclusion"), kind, commit, flag)
				cmd.Env = env
				if out, err := cmd.CombinedOutput(); err == nil {
					t.Fatalf("accepted exclusion on unadmitted source: %s", out)
				}
			}
			if err := os.WriteFile(filepath.Join(source, "untracked"), []byte("dirty"), 0600); err != nil {
				t.Fatal(err)
			}
			if out, err := prepare(filepath.Join(root, "dirty"), commit); err == nil {
				t.Fatalf("accepted dirty source: %s", out)
			}
			if err := os.Remove(filepath.Join(source, "untracked")); err != nil {
				t.Fatal(err)
			}
			// A source-owned CI directory symlink must never be traversed or
			// replaced silently by the overlay, even in the private clone.
			run("", "-C", source, "update-index", "--add", "--cacheinfo", "120000,"+link+",.gitlab")
			linkedTree := run("", "-C", source, "write-tree")
			linkedCommit := run("linked CI fixture\n", "-C", source, "commit-tree", linkedTree)
			run("", "-C", source, "checkout", "--force", "--detach", linkedCommit)
			if out, err := prepare(filepath.Join(root, "linked-ci"), linkedCommit); err == nil {
				t.Fatalf("accepted CI directory symlink: %s", out)
			}
		})
	}
}
