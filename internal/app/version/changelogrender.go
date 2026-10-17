// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domainversion "github.com/diggsweden/forge-ci/internal/domain/version"
	"github.com/diggsweden/forge-ci/internal/runcontext"
)

// backendGitCliff is the default changelog renderer backend; backendGitChglog
// stays selectable.
const (
	backendGitChglog = "git-chglog"
	backendGitCliff  = "git-cliff"
)

type changelogRenderGit interface {
	FetchBranch(ctx context.Context, remote, branch string, cred runcontext.Credential) error
	Checkout(ctx context.Context, ref string) error
	StatusPorcelain(ctx context.Context, pathspec string) (string, error)
}

type changelogRenderer interface {
	RenderFull(ctx context.Context, backend, config, tag, outputPath, repositoryURL string) error
	RenderBody(ctx context.Context, backend, config, tag, repositoryURL string) (string, error)
}

// ChangelogRenderInput drives `forge-ci version render-changelog`.
type ChangelogRenderInput struct {
	Backend           string // git-cliff or git-chglog; empty defaults to git-cliff
	Tag               string // final stable release tag
	RepositoryURL     string // canonical HTTPS web URL used in generated changelog links
	Remote            string // empty defaults to origin
	Branch            string // empty defaults to main
	ChangelogConfig   string
	CommitBodyConfig  string
	ChangelogPath     string // empty defaults to CHANGELOG.md
	CommitBodyPath    string // empty defaults to commit-body.txt
	CommitMessagePath string // empty defaults to commit-msg.txt
	CommitTrailers    string
	Token             runcontext.Credential
}

// ChangelogRender renders CHANGELOG.md and commit-msg.txt before signing-key
// material is loaded. A release whose final tag already exists is not resumed:
// release validate-request refuses it before rendering, and the release commit
// refuses it again, so an interrupted release is followed by the next version.
func ChangelogRender(ctx context.Context, repo changelogRenderGit, renderer changelogRenderer, out io.Writer, in ChangelogRenderInput) error {
	if err := validateChangelogRenderInput(in); err != nil {
		return err
	}

	in = withChangelogRenderDefaults(in)

	if err := repo.FetchBranch(ctx, in.Remote, in.Branch, in.Token); err != nil {
		return fmt.Errorf("render-changelog: fetch %s %s: %w", in.Remote, in.Branch, err)
	}

	if err := repo.Checkout(ctx, in.Branch); err != nil {
		return fmt.Errorf("render-changelog: checkout %s: %w", in.Branch, err)
	}

	if err := renderChangelogFull(ctx, renderer, out, in); err != nil {
		return err
	}

	return finalizeChangelogCommitFiles(ctx, repo, renderer, out, in)
}

// finalizeChangelogCommitFiles writes the commit-message files when the
// rendered changelog actually changed; an unchanged changelog needs no
// release bump commit, so it is a successful no-op.
func finalizeChangelogCommitFiles(ctx context.Context, repo changelogRenderGit, renderer changelogRenderer, out io.Writer, in ChangelogRenderInput) error {
	status, err := repo.StatusPorcelain(ctx, in.ChangelogPath)
	if err != nil {
		return fmt.Errorf("render-changelog: inspect %s: %w", in.ChangelogPath, err)
	}

	if strings.TrimSpace(status) == "" {
		_, _ = fmt.Fprintf(out, "%s unchanged; no release bump commit needed\n", in.ChangelogPath)

		return nil
	}

	return writeCommitMessageFiles(ctx, renderer, in)
}

// renderChangelogFull renders the full changelog file and reports its size.
func renderChangelogFull(ctx context.Context, renderer changelogRenderer, out io.Writer, in ChangelogRenderInput) error {
	if err := renderer.RenderFull(ctx, in.Backend, in.ChangelogConfig, in.Tag, in.ChangelogPath, in.RepositoryURL); err != nil {
		return fmt.Errorf("render-changelog: render %s with %s: %w", in.ChangelogPath, in.Backend, err)
	}

	lineCount, err := countFileLines(in.ChangelogPath)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(out, "Generated %s (%d lines)\n", in.ChangelogPath, lineCount)

	return nil
}

// writeCommitMessageFiles renders the commit body and writes the commit-body
// and commit-message files consumed by the later signing step.
func writeCommitMessageFiles(ctx context.Context, renderer changelogRenderer, in ChangelogRenderInput) error {
	body, err := renderer.RenderBody(ctx, in.Backend, in.CommitBodyConfig, in.Tag, in.RepositoryURL)
	if err != nil {
		return fmt.Errorf("render-changelog: render commit body with %s: %w", in.Backend, err)
	}

	// Atomic replacement: a failed or interrupted write never leaves a
	// truncated message for the signing step, and a symlink planted at either
	// path in the checkout is replaced rather than followed.
	if err := cliio.WriteFile(in.CommitBodyPath, []byte(body), 0o644); err != nil {
		return fmt.Errorf("render-changelog: write %s: %w", in.CommitBodyPath, err)
	}

	if err := cliio.WriteFile(in.CommitMessagePath, []byte(commitMessage(in.Tag, body, in.CommitTrailers)), 0o644); err != nil {
		return fmt.Errorf("render-changelog: write %s: %w", in.CommitMessagePath, err)
	}

	return nil
}

func validateChangelogRenderInput(in ChangelogRenderInput) error {
	if err := requiredChangelogRenderFields(in); err != nil {
		return err
	}

	backend := in.Backend
	if backend == "" {
		backend = backendGitCliff
	}

	if backend != backendGitChglog && backend != backendGitCliff {
		return fmt.Errorf("render-changelog: backend must be git-chglog or git-cliff: %s: %w", backend, errs.ErrValidation)
	}

	return validateChangelogRepositoryURL(in.RepositoryURL)
}

// requiredChangelogRenderFields checks the inputs that must be present and
// well-formed before any of the rendering decisions matter.
func requiredChangelogRenderFields(in ChangelogRenderInput) error {
	switch {
	case in.Tag == "":
		return fmt.Errorf("render-changelog: tag is required: %w", errs.ErrUsage)
	case strings.ContainsAny(in.Tag, "\n\r") || !domainversion.IsStableSemverTag(in.Tag):
		return fmt.Errorf("render-changelog: tag must look like stable vMAJOR.MINOR.PATCH: %s: %w", in.Tag, errs.ErrValidation)
	case in.RepositoryURL == "":
		return fmt.Errorf("render-changelog: repository URL is required: %w", errs.ErrUsage)
	case in.ChangelogConfig == "":
		return fmt.Errorf("render-changelog: changelog config is required: %w", errs.ErrUsage)
	case in.CommitBodyConfig == "":
		return fmt.Errorf("render-changelog: commit body config is required: %w", errs.ErrUsage)
	}

	return nil
}

func validateChangelogRepositoryURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("render-changelog: invalid repository URL: %w", errs.ErrValidation)
	}

	if !canonicalHTTPSURL(raw, parsed) || !ownerAndRepositoryPath(parsed.Path) {
		return fmt.Errorf("render-changelog: repository URL must be canonical HTTPS without credentials, escapes, query, fragment, or trailing slash: %w", errs.ErrValidation)
	}

	return nil
}

// canonicalHTTPSURL reports whether the URL is the plain https://host/... form
// this renderer embeds in a changelog: no credentials, no query, no fragment,
// and no escaping that would make the rendered link differ from the input.
func canonicalHTTPSURL(raw string, parsed *url.URL) bool {
	return parsed.Scheme == "https" && parsed.Opaque == "" && parsed.Hostname() != "" && parsed.User == nil &&
		parsed.RawQuery == "" && !parsed.ForceQuery && parsed.Fragment == "" && parsed.RawPath == "" &&
		!strings.ContainsAny(raw, "\\?#") && parsed.EscapedPath() == parsed.Path
}

// ownerAndRepositoryPath reports whether the path names at least an owner and a
// repository, with no empty, dot or trailing segment.
func ownerAndRepositoryPath(path string) bool {
	if strings.HasSuffix(path, "/") {
		return false
	}

	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(segments) < 2 {
		return false
	}

	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}

	return true
}

func withChangelogRenderDefaults(in ChangelogRenderInput) ChangelogRenderInput {
	if in.Backend == "" {
		in.Backend = backendGitCliff
	}

	if in.Remote == "" {
		in.Remote = defaultRemoteName
	}

	if in.Branch == "" {
		in.Branch = "main"
	}

	if in.ChangelogPath == "" {
		in.ChangelogPath = defaultChangelogFile
	}

	if in.CommitBodyPath == "" {
		in.CommitBodyPath = "commit-body.txt"
	}

	if in.CommitMessagePath == "" {
		in.CommitMessagePath = "commit-msg.txt"
	}

	return in
}

func countFileLines(path string) (int, error) {
	body, err := os.ReadFile(path) //nolint:gosec // caller-configured changelog path.
	if err != nil {
		return 0, fmt.Errorf("render-changelog: read %s: %w", path, err)
	}

	if len(body) == 0 {
		return 0, nil
	}

	return strings.Count(string(body), "\n"), nil
}

func commitMessage(tag, body, trailers string) string {
	msg := fmt.Sprintf("chore(release): bump to %s\n\n%s\n[skip ci]\n", tag, body)
	if trailers != "" {
		msg += "\n" + trailers + "\n"
	}

	return msg
}
