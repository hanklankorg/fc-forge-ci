// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package git is the thin shell-out adapter around the `git` CLI.
//
// Each method is one git invocation, returning typed values + sentinel
// errors. Callers in app/ orchestrate sequences of these. Domain code
// never touches this package — git operations are I/O.
package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/diggsweden/forge-ci/internal/safeexec"
	gogit "github.com/go-git/go-git/v5"
)

// defaultRemote is the remote name assumed whenever a caller leaves the
// remote unspecified — the same "origin" every fetch/push method targets.
const (
	defaultRemote       = "origin"
	hooksDisabledConfig = "core.hooksPath=/dev/null"
)

// Repo is a handle for git operations against a working tree.
// The Dir field, when non-empty, sets the cwd for every invocation.
//
// Two execution paths coexist behind these methods:
//
//   - Read-only object lookups (rev-parse, tag-uniqueness, ancestor checks,
//     tag-object inspection) run in-process via go-git. One process for
//     the whole sequence, typed results, structured errors.
//   - Network ops (push, fetch) and signing-driven ops (commit, CreateTag,
//     VerifyTag) remain subprocess. These integrate with SSH agent,
//     credential helpers, and gpg-agent — pure-Go equivalents would
//     reimplement that integration with weaker security/UX.
type Repo struct {
	Dir    string
	GitBin string // override `git` binary path; empty → exec.LookPath("git")

	// gogitMu serialises every use of the memoised go-git repository,
	// not just its open: go-git's object storage (pack index, object
	// cache) is not safe for concurrent use, and release validation reads
	// one Repo from several goroutines. Shared reads raced and surfaced as
	// wrong answers, such as an annotated tag reported as lightweight.
	gogitMu     sync.Mutex
	gogitOpened bool
	gogitRepo   *gogit.Repository
	gogitErr    error
}

// New returns a Repo that runs git against the current working directory
// (Dir == ""). For test isolation, set Dir to t.TempDir().
func New() *Repo { return &Repo{} }

// Run executes `git <args...>` and returns its trimmed stdout. Errors
// are classified through safeexec.WrapError so missing-on-PATH maps to
// ExitCodeUnavailable and non-zero exit maps to ExitCodeValidation;
// git's own diagnostic output is appended for operator context.
func (r *Repo) Run(ctx context.Context, args ...string) (string, error) {
	bin := r.GitBin
	if bin == "" {
		bin = "git" //nolint:goconst // test fixture / generic identifier — extracting would explode setup boilerplate.
	}

	cmd := safeexec.Command(ctx, bin, args...)
	if r.Dir != "" {
		cmd.Dir = r.Dir
	}

	cmd.Env = messageStableEnv()

	out, err := cmd.CombinedOutput()
	if err != nil {
		wrapped := safeexec.WrapError(err, bin, firstGitArg(args))
		if len(out) == 0 {
			return "", wrapped
		}

		return "", fmt.Errorf("%w\n%s", wrapped, safeexec.RedactKeyMaterial(out))
	}

	return strings.TrimRight(string(out), "\n"), nil
}

// Output executes `git <args...>` and returns its standard output exactly, for
// output whose bytes matter: a patch keeps its final newline, and standard
// error, which Run folds in, only joins an error.
func (r *Repo) Output(ctx context.Context, args ...string) (string, error) {
	bin := r.GitBin
	if bin == "" {
		bin = "git"
	}

	cmd := safeexec.Command(ctx, bin, args...)
	if r.Dir != "" {
		cmd.Dir = r.Dir
	}

	cmd.Env = messageStableEnv()

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		wrapped := safeexec.WrapError(err, bin, firstGitArg(args))
		if stderr.Len() == 0 {
			return "", wrapped
		}

		return "", fmt.Errorf("%w\n%s", wrapped, safeexec.RedactKeyMaterial(stderr.Bytes()))
	}

	return string(out), nil
}

// firstGitArg returns the first non-flag arg as the subcommand label
// (e.g. "rev-parse", "tag", "commit"). Skipping leading "-*" tokens
// means options like "-c user.signingkey=… commit" still produce the
// useful "commit" label instead of "-c".
func firstGitArg(args []string) string {
	skipNext := false
	for _, arg := range args {
		if skipNext {
			skipNext = false

			continue
		}

		if arg == "-c" {
			skipNext = true

			continue
		}

		if !strings.HasPrefix(arg, "-") {
			return arg
		}
	}

	return ""
}

// messageStableEnv is the ambient environment with the message locale pinned
// to C.
//
// Callers classify git by reading its diagnostics: VerifyTagSSHAgainstAllowedSigners
// decides between "the allowed_signers file is missing" (exit 66, tells the
// operator to commit the file) and "the signer is not allowed" (exit 77, a
// refusal) by matching "Unable to open allowed keys file", "No such file or
// directory" and "No principal matched" in git's output. Those strings are
// git's and ssh-keygen's localised messages. Under a non-English LANG they come
// back translated, every match fails, and both cases collapse into the generic
// branch — a missing file reported as a plain validation failure with no hint
// about what to do.
//
// Nothing made that premise true; it held because CI runners happen to have no
// locale set. Pinning LC_ALL makes the premise a property of the call instead
// of a property of the host, and it is the narrow half of the fix: the wider
// half would be for git to expose a machine-readable reason, which it does not.
//
// The rest of the environment is inherited unchanged. These invocations reach
// ssh-agent, gpg-agent and credential helpers through it, so replacing it
// rather than extending it would break signing.
func messageStableEnv() []string {
	return append(os.Environ(), "LC_ALL=C")
}

// withGoGit runs fn with a memoised *gogit.Repository rooted at Dir (or
// cwd when Dir == ""), holding gogitMu until fn returns. Nothing fn
// obtains that reads storage lazily (a commit walk, an iterator) may
// outlive it. PlainOpenWithOptions{DetectDotGit:true} matches git CLI's
// "walk up looking for .git" behaviour so callers don't have to pass the
// exact .git path. An open error is returned without calling fn, and is
// memoised like the repository.
func (r *Repo) withGoGit(fn func(*gogit.Repository) error) error {
	r.gogitMu.Lock()
	defer r.gogitMu.Unlock()

	if !r.gogitOpened {
		r.gogitOpened = true
		r.gogitRepo, r.gogitErr = openGoGitAt(r.Dir)
	}

	if r.gogitErr != nil {
		return r.gogitErr
	}

	return fn(r.gogitRepo)
}

func openGoGitAt(path string) (*gogit.Repository, error) {
	if path == "" {
		var err error

		path, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("getwd: %w", err)
		}
	}

	repo, err := gogit.PlainOpenWithOptions(path, &gogit.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return nil, fmt.Errorf("open git repo at %q: %w", path, err)
	}

	return repo, nil
}
