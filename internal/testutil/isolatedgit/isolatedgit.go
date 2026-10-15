// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package isolatedgit creates a throwaway git repo in t.TempDir() with a
// known committer identity, plus helpers to add commits/tags and an
// optional bare remote. Replaces the bats `common_setup_with_isolated_git`
// + `init_remote_repo` helpers.
//
// A package that uses it isolates the whole test process once, from TestMain:
//
//	func TestMain(m *testing.M) { os.Exit(isolatedgit.Main(m.Run)) }
//
// Process-wide rather than per test because the product's git inherits
// os.Environ(): a scrub applied only to this helper's commands would leave the
// code under test reading the developer's ~/.gitconfig. Being process-wide, it
// also leaves the tests free to run in parallel.
package isolatedgit

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/isolatedenv"
)

// processIsolated is set by Main before any test runs and only read after.
var processIsolated bool //nolint:gochecknoglobals // one-way latch between TestMain and NewRepo.

// Main isolates the process environment (HOME, XDG, Git, GPG, SSH, proxies,
// locale, colour and every CI/forge/credential variable, as
// isolatedenv.Isolate does per test), runs the tests through run, removes the
// isolated home, and returns run's exit code.
func Main(run func() int) int {
	_, cleanup, err := isolatedenv.IsolateProcess()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isolatedgit: isolate the test process: %v\n", err)

		return 1
	}

	defer cleanup()

	processIsolated = true

	return run()
}

// Repo represents an isolated git working tree under t.TempDir().
type Repo struct {
	t      *testing.T
	Dir    string // working tree
	Home   string // private directory for the test's keys and signer files
	Remote string // bare remote dir, empty until AddBareRemote() is called
}

// NewRepo creates a fresh repo with a single empty initial commit on the
// `main` branch. Committer is set to "Test Bot <bot@example.invalid>".
//
// It refuses to run unless Main isolated the process, so no git command,
// the helper's or the product's, can read the developer's ~/.gitconfig or
// reach their SSH agent. It changes no environment itself: the identity and
// signing settings are repo-local, and the global gitconfig stays /dev/null,
// so a test that wants a writable one sets GIT_CONFIG_GLOBAL itself.
func NewRepo(t *testing.T) *Repo {
	t.Helper()

	if !processIsolated {
		t.Fatal("isolatedgit: the test process is not isolated; add " +
			"func TestMain(m *testing.M) { os.Exit(isolatedgit.Main(m.Run)) }")
	}

	r := &Repo{t: t, Dir: t.TempDir(), Home: t.TempDir()} //nolint:varnamelen // idiomatic short name (testing/http/io conventions).

	r.gitInWorkdir("init", "-q", "-b", "main")
	r.gitInWorkdir("config", "user.name", "Test Bot")
	r.gitInWorkdir("config", "user.email", "bot@example.invalid")
	r.gitInWorkdir("config", "commit.gpgsign", "false")
	r.gitInWorkdir("config", "tag.gpgsign", "false")

	r.AddCommit("initial commit")

	return r
}

// Git runs `git <args...>` inside the repo's working tree and returns stdout.
// On failure the test fatals with a clear error.
func (r *Repo) Git(args ...string) string {
	r.t.Helper()

	return r.gitInWorkdir(args...)
}

// AddCommit creates a commit (allow-empty) with the given message and returns the SHA.
func (r *Repo) AddCommit(msg string) string {
	r.t.Helper()
	r.gitInWorkdir("commit", "--allow-empty", "-q", "-m", msg)

	return r.gitInWorkdir("rev-parse", "HEAD")
}

// AddFile writes a file at relPath, stages it, and creates a commit.
func (r *Repo) AddFile(relPath, contents, commitMsg string) string {
	r.t.Helper()

	full := filepath.Join(r.Dir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil { //nolint:gosec // test infra; r.Dir is t.TempDir().
		r.t.Fatalf("mkdir %q: %v", filepath.Dir(full), err)
	}

	if err := os.WriteFile(full, []byte(contents), 0o644); err != nil { //nolint:gosec // test infra; full is r.Dir-rooted.
		r.t.Fatalf("write %q: %v", full, err)
	}

	r.gitInWorkdir("add", relPath)
	r.gitInWorkdir("commit", "-q", "-m", commitMsg)

	return r.gitInWorkdir("rev-parse", "HEAD")
}

// AddTag creates an annotated tag pointing at HEAD with the given message.
func (r *Repo) AddTag(name, msg string) {
	r.t.Helper()
	r.gitInWorkdir("tag", "-a", name, "-m", msg)
}

// AddBareRemote creates a bare remote in t.TempDir(), registers it as `origin`,
// and pushes the current main branch.
func (r *Repo) AddBareRemote() string {
	r.t.Helper()
	remote := r.t.TempDir()

	//nolint:gosec,noctx // test infra; remote is t.TempDir().
	cmd := exec.Command("git", "init", "--bare", "-q", "-b", "main", remote)
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("init bare remote: %v\n%s", err, out)
	}

	r.gitInWorkdir("remote", "add", "origin", remote)
	r.gitInWorkdir("push", "-q", "-u", "origin", "main")
	r.Remote = remote

	return remote
}

// HeadSHA returns the current HEAD commit SHA.
func (r *Repo) HeadSHA() string {
	return r.gitInWorkdir("rev-parse", "HEAD")
}

func (r *Repo) gitInWorkdir(args ...string) string {
	r.t.Helper()

	//nolint:gosec,noctx // test infra; args are caller-supplied within tests.
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir

	// Stdout only, as Git's doc comment has always said. This used to return
	// CombinedOutput, so a successful command that printed a hint or warning
	// on stderr handed that text back as part of its value -- a SHA from
	// HeadSHA, a subject from `log --format` -- and the caller compared the
	// contaminated string. stderr is kept for the failure message, where it
	// is the useful part.
	var stderr strings.Builder

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		r.t.Fatalf("git %s\nin %s\nstdout:\n%s\nstderr:\n%s\nerr: %v",
			strings.Join(args, " "), r.Dir, out, stderr.String(), err)
	}

	return strings.TrimRight(string(out), "\n")
}
