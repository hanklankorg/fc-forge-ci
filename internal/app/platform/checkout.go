// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package platform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	domainci "github.com/diggsweden/forge-ci/internal/domain/ci"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domaingit "github.com/diggsweden/forge-ci/internal/domain/git"
	domainrepository "github.com/diggsweden/forge-ci/internal/domain/repository"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
	"github.com/diggsweden/forge-ci/internal/runcontext"
)

// CheckoutGit is the git operation subset Checkout drives. The adapter's
// *git.Repo (with Dir set to the workspace) satisfies it structurally; the
// port keeps app/ci free of any adapter import, and the credential
// mechanics stay encapsulated in the adapter's Fetch.
type CheckoutGit interface { //nolint:interfacebloat // existing checkout stages plus typed tag/branch existence queries; all belong to this use case.
	InitWithObjectFormat(ctx context.Context, format string) error
	RemoteAdd(ctx context.Context, name, url string) error
	Fetch(ctx context.Context, remoteURL string, refspecs []string, cred runcontext.Credential, depth int) error
	FetchTags(ctx context.Context, remoteURL string, cred runcontext.Credential) error
	FetchAllRefs(ctx context.Context, remoteURL string, cred runcontext.Credential) error
	EnablePartialClone(ctx context.Context) error
	SparseInit(ctx context.Context, cone bool) error
	SparseSet(ctx context.Context, patterns []string) error
	CheckoutDetach(ctx context.Context, ref string) error
	RevParse(ctx context.Context, rev string) (string, error)
	RemoteTagCommitIfExists(ctx context.Context, remote, tag string, cred runcontext.Credential) (string, bool, error)
	RemoteBranchCommit(ctx context.Context, remote, branch string, cred runcontext.Credential) (string, bool, error)
}

// CheckoutDestinations is the filesystem side of a checkout: the acquired
// directory Git works in and how that work reaches the caller's workspace.
// The adapter owns staging, identity, publication and rollback; Checkout
// decides only when the run is over and whether it worked.
type CheckoutDestinations interface {
	// Inspect refuses an unusable workspace without creating anything.
	Inspect(workspace string) error
	// Open returns the directory to run Git in and the call that ends its
	// lifetime, publishing on ok and rolling back otherwise.
	Open(workspace string) (dir string, finish func(ok bool) error, err error)
}

const (
	objectSHA1   = "sha1"
	objectSHA256 = "sha256"
)

// CheckoutInput drives Checkout.
type CheckoutInput struct {
	Repository   string                // "owner/name"
	ServerURL    string                // e.g. https://forgejo.example.com
	Ref          string                // commit SHA, refs/tags/…, refs/heads/…, or a bare tag/branch name
	Workspace    string                // target directory (must not already contain .git)
	Token        runcontext.Credential // optional; absent means an anonymous checkout
	ObjectFormat string                // optional explicit override ("sha1"/"sha256")
	FetchBase    string                // optional extra branch to also fetch (diff/commit-range checks)
	FetchTags    bool                  // also fetch all tags (the JS actions/checkout fetch-tags:true)
	FetchAllRefs bool                  // fetch every branch into refs/remotes/origin/* plus all tags (actions/checkout fetch-depth:0)
	Depth        int                   // shallow history depth for the primary ref fetch; 0 = full history (mirrors the JS actions/checkout fetch-depth, where 0 means "all"). Ignored for fetch-all-refs/fetch-tags, which are inherently full.
	Sparse       []string              // cone patterns; non-empty restricts the working tree to these dirs
	OutputKey    string                // OutputSink key for the resolved SHA (default "checkout-sha")
}

// Checkout materializes a consumer repository at an exact ref into the
// workspace: a credential-free, sha256-aware git checkout for jobs where
// the JavaScript actions/checkout cannot run. It is the Go port of
// forgejo-ci's checkout-consumer.sh and reproduces its resolution order
// (full refs → SHA → bare-name tag-then-branch probe) and hardened
// behaviours (named errors, quiet probe misses, no interactive prompts,
// unsafe-ref rejection). The resolved commit SHA is written to the
// OutputSink and returned.
func Checkout(ctx context.Context, destinations CheckoutDestinations, newGit func(dir string) CheckoutGit, sink domainci.OutputSink, w io.Writer, in CheckoutInput) (string, error) { //nolint:varnamelen // idiomatic short name (testing/http/io conventions).
	// Refused before the destination is opened: a missing collaborator
	// used to panic only after the staging directory existed, or after the
	// fetch when the sink was the missing one. The writer is optional.
	if newGit == nil || sink == nil {
		return "", fmt.Errorf("checkout: git factory and output sink are required: %w", errs.ErrUsage)
	}

	// Metadata has now been resolved; only here does an absent format mean SHA-1.
	if in.ObjectFormat == "" {
		in.ObjectFormat = objectSHA1
	}

	if preflightErr := PreflightCheckout(destinations, in); preflightErr != nil {
		return "", preflightErr
	}

	dir, finish, err := destinations.Open(in.Workspace)
	if err != nil {
		return "", err
	}

	git := newGit(dir)
	in.Workspace = dir

	sha, err := runCheckout(ctx, git, sink, w, in)

	// Publication is the last thing that happens, and only when everything
	// before it worked.
	if finishErr := finish(err == nil); finishErr != nil {
		return "", errors.Join(err, finishErr)
	}

	return sha, err
}

// runCheckout performs the Git sequence in whichever directory the destination
// resolved to.
func runCheckout(ctx context.Context, git CheckoutGit, sink domainci.OutputSink, w io.Writer, in CheckoutInput) (string, error) { //nolint:varnamelen // writer convention.
	// The command resolves the override-or-provider object format and
	// passes it in; an empty value (no override, forge does not report
	// one) defaults to sha1.
	format := in.ObjectFormat

	if err := git.InitWithObjectFormat(ctx, format); err != nil {
		return "", fmt.Errorf("init %s repository in %s: %w", format, in.Workspace, err)
	}

	remoteURL, _ := checkoutRemoteURL(in.ServerURL, in.Repository)
	if err := git.RemoteAdd(ctx, "origin", remoteURL); err != nil {
		return "", fmt.Errorf("add origin %s: %w", remoteURL, err)
	}

	if err := configureSparse(ctx, git, in); err != nil {
		return "", err
	}

	if err := fetchAllRefs(ctx, git, remoteURL, in); err != nil {
		return "", err
	}

	if err := fetchAndCheckout(ctx, git, remoteURL, in); err != nil {
		return "", err
	}

	if err := fetchExtras(ctx, git, remoteURL, in); err != nil {
		return "", err
	}

	return reportCheckout(ctx, git, sink, w, in, format)
}

// fetchAllRefs performs the all-history checkout when requested: it populates
// every sibling branch (refs/remotes/origin/*) and all tags up front, so
// builds that read git topology — `git rev-list --count origin/main`,
// `git describe` — see the same refs the JS actions/checkout fetch-depth:0
// provides. The targeted fetchAndCheckout that follows still pins the exact
// ref. A no-op when not requested.
func fetchAllRefs(ctx context.Context, git CheckoutGit, remoteURL string, in CheckoutInput) error {
	if !in.FetchAllRefs {
		return nil
	}

	if err := git.FetchAllRefs(ctx, remoteURL, in.Token); err != nil {
		return fmt.Errorf("fetch all refs: %w", err)
	}

	return nil
}

// configureSparse sets up cone-mode sparse-checkout BEFORE any checkout, so the
// working tree is only ever populated with the requested cone — the subtree
// never lands on disk in full. (Partial-clone blob filtering is a download
// optimization that can be layered on later without changing this.) A no-op
// when no patterns are requested.
func configureSparse(ctx context.Context, git CheckoutGit, in CheckoutInput) error {
	if len(in.Sparse) == 0 {
		return nil
	}

	// Pair sparse-checkout with a blob:none partial clone (as the JS
	// actions/checkout does): only the cone's blobs download. Degrades to a
	// full fetch on servers without partial-clone support.
	if err := git.EnablePartialClone(ctx); err != nil {
		return fmt.Errorf("enable partial clone: %w", err)
	}

	if err := git.SparseInit(ctx, true); err != nil {
		return fmt.Errorf("sparse-checkout init: %w", err)
	}

	if err := git.SparseSet(ctx, in.Sparse); err != nil {
		return fmt.Errorf("sparse-checkout set: %w", err)
	}

	return nil
}

// An absent optional base branch is harmless; lookup or fetch failures are not
// evidence of absence. Supplemental fetches always request full history.
func fetchExtras(ctx context.Context, git CheckoutGit, remoteURL string, in CheckoutInput) error {
	if in.FetchBase != "" {
		_, exists, err := git.RemoteBranchCommit(ctx, "origin", in.FetchBase, in.Token)
		if err != nil {
			return fmt.Errorf("probe base branch: %w", err)
		}

		if exists {
			if err := git.Fetch(ctx, remoteURL, []string{"+refs/heads/" + in.FetchBase + ":refs/heads/" + in.FetchBase}, in.Token, 0); err != nil {
				return fmt.Errorf("fetch base branch: %w", err)
			}
		}
	}

	if in.FetchTags {
		if err := git.FetchTags(ctx, remoteURL, in.Token); err != nil {
			return fmt.Errorf("fetch tags: %w", err)
		}
	}

	return nil
}

// reportCheckout resolves the checked-out HEAD, records it on the output
// sink, and prints the summary line. Split out so Checkout stays within
// the cyclomatic budget.
func reportCheckout(ctx context.Context, git CheckoutGit, sink domainci.OutputSink, w io.Writer, in CheckoutInput, format string) (string, error) { //nolint:cyclop,varnamelen // canonical HEAD validation precedes sink-first publication and diagnostics.
	sha, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve checked-out HEAD: %w", err)
	}

	if !domaingit.ValidCommitSHA(sha) || (format == objectSHA1 && len(sha) != 40) || (format == objectSHA256 && len(sha) != 64) {
		return "", fmt.Errorf("checked-out HEAD does not match the selected object format: %w", errs.ErrValidation)
	}

	key := in.OutputKey
	if key == "" {
		key = "checkout-sha"
	}

	// The machine output is the commit point; diagnostics follow it. A writer
	// error is reported, but cannot undo an already accepted sink value.
	if err := sink.Set(ctx, key, sha); err != nil {
		return "", err
	}

	if w != nil {
		if _, err := fmt.Fprintf(w, "Checked out %s at %s (%s)\n", in.Repository, sha, format); err != nil {
			return "", fmt.Errorf("write checkout summary: %w", err)
		}
	}

	return sha, nil
}

// fetchAndCheckout reproduces checkout-consumer.sh's ref resolution order.
// The explicit kinds (full refs, SHA) fetch-or-fail with a named error; the
// bare-name case probes tag then branch, treating a probe miss as control
// flow (the adapter captures git's stderr, so a miss is silent) and naming
// the failure only when neither exists.
func fetchAndCheckout(ctx context.Context, git CheckoutGit, remoteURL string, in CheckoutInput) error {
	ref := in.Ref

	switch {
	case strings.HasPrefix(ref, "refs/tags/"):
		if err := git.Fetch(ctx, remoteURL, []string{"+" + ref + ":" + ref}, in.Token, in.Depth); err != nil {
			return fetchError(ref, remoteURL, err)
		}

		return git.CheckoutDetach(ctx, ref)

	case strings.HasPrefix(ref, "refs/heads/"):
		branch := strings.TrimPrefix(ref, "refs/heads/")

		dst := "refs/remotes/origin/" + branch
		if err := git.Fetch(ctx, remoteURL, []string{"+" + ref + ":" + dst}, in.Token, in.Depth); err != nil {
			return fetchError(ref, remoteURL, err)
		}

		return git.CheckoutDetach(ctx, dst)

	case isHexSHA(ref):
		if err := git.Fetch(ctx, remoteURL, []string{ref}, in.Token, in.Depth); err != nil {
			return fetchError(ref, remoteURL, err)
		}

		return git.CheckoutDetach(ctx, "FETCH_HEAD")

	default:
		return fetchBareName(ctx, git, remoteURL, in)
	}
}

func fetchBareName(ctx context.Context, git CheckoutGit, remoteURL string, in CheckoutInput) error {
	ref := in.Ref

	_, exists, err := git.RemoteTagCommitIfExists(ctx, "origin", ref, in.Token)
	if err != nil {
		return fmt.Errorf("probe checkout tag: %w", err)
	}

	tagDst := "refs/tags/" + ref
	if exists {
		if fetchErr := git.Fetch(ctx, remoteURL, []string{"+" + tagDst + ":" + tagDst}, in.Token, in.Depth); fetchErr != nil {
			return fetchError(ref, remoteURL, fetchErr)
		}

		return git.CheckoutDetach(ctx, tagDst)
	}

	_, exists, err = git.RemoteBranchCommit(ctx, "origin", ref, in.Token)
	if err != nil {
		return fmt.Errorf("probe checkout branch: %w", err)
	}

	branchDst := "refs/remotes/origin/" + ref
	if exists {
		if err := git.Fetch(ctx, remoteURL, []string{"+refs/heads/" + ref + ":" + branchDst}, in.Token, in.Depth); err != nil {
			return fetchError(ref, remoteURL, err)
		}

		return git.CheckoutDetach(ctx, branchDst)
	}

	return fmt.Errorf("ref %q not found as a tag or branch on %s: %w", ref, remoteURL, errs.ErrValidation)
}

func fetchError(ref, remoteURL string, err error) error {
	return fmt.Errorf("cannot fetch %q from %s (wrong ref, or the server refuses unadvertised SHAs): %w", ref, remoteURL, err)
}

// PreflightCheckout checks all locally decidable input and workspace constraints
// without creating directories or contacting Git, metadata providers or sinks.
// An empty ObjectFormat defers only SHA-length agreement until metadata resolves.
// Checkout repeats this check before mutation; it does not promise rollback or
// hold a descriptor across pathname-based external Git operations.
func PreflightCheckout(destinations CheckoutDestinations, in CheckoutInput) error {
	if destinations == nil {
		return fmt.Errorf("checkout: destination owner is required: %w", errs.ErrUsage)
	}

	if err := validateCheckoutInput(in); err != nil {
		return err
	}

	return destinations.Inspect(in.Workspace)
}

func validateCheckoutInput(in CheckoutInput) error { //nolint:cyclop // flat local guards precede every filesystem, Git and credential effect.
	if in.Repository == "" {
		return fmt.Errorf("repository (owner/name) is required: %w", errs.ErrUsage)
	}

	if in.ServerURL == "" {
		return fmt.Errorf("server-url is required: %w", errs.ErrUsage)
	}

	if err := validCheckoutWorkspace(in.Workspace); err != nil {
		return err
	}

	if _, err := checkoutRemoteURL(in.ServerURL, in.Repository); err != nil {
		return err
	}

	format := in.ObjectFormat
	if format != "" && format != objectSHA1 && format != objectSHA256 {
		return fmt.Errorf("unsupported object format: %w", errs.ErrUsage)
	}

	if isHexSHA(in.Ref) && ((format == objectSHA1 && len(in.Ref) != 40) || (format == objectSHA256 && len(in.Ref) != 64)) {
		return fmt.Errorf("ref and object format disagree: %w", errs.ErrValidation)
	}

	if in.Depth < 0 || (in.OutputKey != "" && !validOutputKey(in.OutputKey)) {
		return fmt.Errorf("invalid checkout depth or output key: %w", errs.ErrUsage)
	}

	if in.FetchBase != "" && (strings.HasPrefix(in.FetchBase, "-") || !domaingit.ValidRefName("refs/heads/"+in.FetchBase)) {
		return fmt.Errorf("invalid base branch: %w", errs.ErrValidation)
	}

	seen := map[string]bool{}
	for _, cone := range in.Sparse {
		if !pathsafe.Relative(cone) || cone == "." || filepath.ToSlash(filepath.Clean(cone)) != cone || strings.HasPrefix(cone, "-") || strings.ContainsAny(cone, "*?[]\\") || !utf8.ValidString(cone) || strings.ContainsFunc(cone, unicode.IsControl) || seen[cone] {
			return fmt.Errorf("sparse cones must be unique literal relative directories: %w", errs.ErrValidation)
		}

		seen[cone] = true
	}

	return validCheckoutRef(in.Ref)
}

// validCheckoutWorkspace keeps the destination spellable and component-safe
// before any root is opened. Cleaning the path first would erase a parent step
// whose linked component external Git would still traverse.
func validCheckoutWorkspace(workspace string) error {
	if strings.TrimSpace(workspace) == "" || !utf8.ValidString(workspace) || strings.ContainsFunc(workspace, unicode.IsControl) {
		return fmt.Errorf("a valid workspace path is required: %w", errs.ErrUsage)
	}

	for component := range strings.SplitSeq(filepath.ToSlash(workspace), "/") {
		if component == ".." {
			return fmt.Errorf("checkout workspace must not contain parent-directory steps: %w", errs.ErrValidation)
		}
	}

	return nil
}

// validCheckoutRef accepts literal Git names or canonical full object IDs,
// excluding options, globs, revision expressions and terminal control text.
func validCheckoutRef(ref string) error {
	switch {
	case ref == "":
		return fmt.Errorf("ref is required: %w", errs.ErrUsage)
	case strings.HasPrefix(ref, "-"):
		return fmt.Errorf("unsafe ref %q (leading dash): %w", ref, errs.ErrValidation)
	case strings.ContainsAny(ref, "\n\r"):
		return fmt.Errorf("unsafe ref (embedded newline): %w", errs.ErrValidation)
	}

	full := ref
	if !strings.HasPrefix(full, "refs/") {
		full = "refs/heads/" + full
	}

	if !isHexSHA(ref) && !domaingit.ValidRefName(full) {
		return fmt.Errorf("ref must be a literal Git reference or full object ID: %w", errs.ErrValidation)
	}

	return nil
}

// isHexSHA accepts exactly SHA-1 or SHA-256, not the lengths in between.
func isHexSHA(ref string) bool { return domaingit.ValidCommitSHA(ref) }

func checkoutRemoteURL(server, repository string) (string, error) { //nolint:cyclop // explicit URL authority, suffix and path-segment guards avoid ambiguous source coordinates.
	parsed, err := url.Parse(server)
	if err != nil {
		return "", fmt.Errorf("invalid checkout server URL: %w", errs.ErrUsage)
	}

	if !utf8.ValidString(server) || strings.ContainsFunc(server, unicode.IsControl) {
		return "", fmt.Errorf("invalid checkout server text: %w", errs.ErrUsage)
	}

	if (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" || strings.ContainsAny(server, "?#\\") {
		return "", fmt.Errorf("checkout server must be an HTTP(S) base URL without credentials, escapes, query or fragment: %w", errs.ErrUsage)
	}

	if port := parsed.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return "", fmt.Errorf("invalid checkout server port: %w", errs.ErrUsage)
		}
	} else if strings.HasSuffix(parsed.Host, ":") {
		return "", fmt.Errorf("empty checkout server port: %w", errs.ErrUsage)
	}

	if _, err := domainrepository.Parse(repository); err != nil {
		return "", err
	}

	if strings.HasSuffix(repository, ".git") {
		return "", fmt.Errorf("checkout repository must be owner/name without .git: %w", errs.ErrUsage)
	}

	if path := strings.Trim(parsed.Path, "/"); path != "" {
		for part := range strings.SplitSeq(path, "/") {
			if part == "" || part == "." || part == ".." || url.PathEscape(part) != part || strings.ContainsAny(part, "\\:") {
				return "", fmt.Errorf("invalid checkout repository or server path: %w", errs.ErrUsage)
			}
		}
	}

	return strings.TrimRight(server, "/") + "/" + repository + ".git", nil
}
