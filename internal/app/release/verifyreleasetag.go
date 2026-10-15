// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"context"
	"fmt"
	"io"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domaingit "github.com/diggsweden/forge-ci/internal/domain/git"
	"github.com/diggsweden/forge-ci/internal/domain/version"
	"github.com/diggsweden/forge-ci/internal/runcontext"
)

// tagVerifyGit is the git surface VerifyReleaseTag needs. RevParse reads
// the local checkout; the Remote* calls re-query the published remote at
// the trust boundary.
type tagVerifyGit interface {
	RevParse(ctx context.Context, ref string) (string, error)
	CatFileType(ctx context.Context, ref string) (string, error)
	RemoteTagObjectAtURL(ctx context.Context, repoURL, tag string, cred runcontext.Credential) (string, error)
	RemoteTagCommit(ctx context.Context, repoURL, tag string, cred runcontext.Credential) (string, error)
	RemoteVersionTags(ctx context.Context, repoURL string, cred runcontext.Credential) ([]string, error)
	CommitParents(ctx context.Context, commitSHA string) ([]string, error)
	ChangedPaths(ctx context.Context, before, after string) ([]string, error)
}

// VerifyReleaseTagInput drives VerifyReleaseTag.
type VerifyReleaseTagInput struct {
	ReleaseSHA string // the commit the release is built from
	Tag        string // the release tag, e.g. v1.2.3
	RepoURL    string // original repository argument for ls-remote; empty defaults to origin
	Token      runcontext.Credential
	// ExpectedParent, when set, requires the release commit to be the single
	// child of that commit: the caller prepared exactly one commit on top of
	// the source it authorised.
	ExpectedParent string
	// ChangelogOnly admits exactly a CHANGELOG.md change and binds the local
	// release-request/<tag> to ExpectedParent, for a tested prepare candidate.
	ChangelogOnly bool
}

// VerifyReleaseTag re-verifies, at the signing trust boundary, that:
//
//  1. the checkout (HEAD) is the release commit,
//  2. the published tag points to that same commit on the remote, and
//  3. the tag has not been superseded by a newer vX.Y.Z tag, and
//  4. with --expected-parent, the release commit is the single child of the
//     authorised source commit.
//
// Port of forgejo-ci's verify-release-tag.sh. Any mismatch is a
// validation error — the signer must refuse to publish a stale or
// hijacked release.
//
//nolint:cyclop // three trust-boundary invariants, one branch each — flatter is clearer than splitting.
func VerifyReleaseTag(ctx context.Context, git tagVerifyGit, out io.Writer, in VerifyReleaseTagInput) error {
	if in.ReleaseSHA == "" || in.Tag == "" {
		return fmt.Errorf("verify-release-tag: release-sha and tag are required: %w", errs.ErrUsage)
	}

	if !domaingit.ValidCommitSHA(in.ReleaseSHA) {
		return fmt.Errorf("verify-release-tag: release-sha must be a full lowercase commit ID: %w", errs.ErrValidation)
	}

	if !version.IsStableSemverTag(in.Tag) {
		return fmt.Errorf("verify-release-tag: release tag must be stable vMAJOR.MINOR.PATCH: %s: %w", in.Tag, errs.ErrValidation)
	}

	if in.ExpectedParent != "" && !domaingit.ValidCommitSHA(in.ExpectedParent) {
		return fmt.Errorf("verify-release-tag: expected-parent must be a full lowercase commit ID: %w", errs.ErrUsage)
	}

	if in.ChangelogOnly && in.ExpectedParent == "" {
		return fmt.Errorf("verify-release-tag: --changelog-only requires --expected-parent: %w", errs.ErrUsage)
	}

	head, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		return fmt.Errorf("verify-release-tag: resolve HEAD: %w", err)
	}

	if head != in.ReleaseSHA {
		return fmt.Errorf("verify-release-tag: checkout %s does not match release-sha %s: %w", head, in.ReleaseSHA, errs.ErrValidation)
	}

	objType, err := git.CatFileType(ctx, "refs/tags/"+in.Tag)
	if err != nil {
		return fmt.Errorf("verify-release-tag: inspect local tag object: %w", err)
	}

	if objType != "tag" {
		return fmt.Errorf("verify-release-tag: final tag %s is not an annotated tag object: %w", in.Tag, errs.ErrValidation)
	}

	repoURL := in.RepoURL
	if repoURL == "" {
		// Keep the original argument. Replaying an effective RemoteURL result
		// through Git could rewrite it again and inspect a different repository.
		repoURL = "origin"
	}

	localObject, err := git.RevParse(ctx, "refs/tags/"+in.Tag)
	if err != nil {
		return fmt.Errorf("verify-release-tag: resolve local tag object: %w", err)
	}

	remoteObject, err := git.RemoteTagObjectAtURL(ctx, repoURL, in.Tag, in.Token)
	if err != nil {
		return fmt.Errorf("verify-release-tag: resolve remote tag object: %w", err)
	}

	if !domaingit.ValidCommitSHA(localObject) || !domaingit.ValidCommitSHA(remoteObject) || remoteObject != localObject {
		return fmt.Errorf("verify-release-tag: local tag object %s does not match remote object %s: %w", localObject, remoteObject, errs.ErrValidation)
	}

	tagCommit, err := git.RemoteTagCommit(ctx, repoURL, in.Tag, in.Token)
	if err != nil {
		return fmt.Errorf("verify-release-tag: resolve remote tag: %w", err)
	}

	if tagCommit != in.ReleaseSHA {
		return fmt.Errorf("verify-release-tag: remote tag %s points to %s, not release-sha %s: %w", in.Tag, tagCommit, in.ReleaseSHA, errs.ErrValidation)
	}

	if err = verifyPreparedSource(ctx, git, in); err != nil {
		return err
	}

	tags, err := git.RemoteVersionTags(ctx, repoURL, in.Token)
	if err != nil {
		return fmt.Errorf("verify-release-tag: list remote tags: %w", err)
	}

	if latest := version.LatestSemverTag(tags); latest != "" && latest != in.Tag {
		return fmt.Errorf("verify-release-tag: tag %s has been superseded by %s: %w", in.Tag, latest, errs.ErrValidation)
	}

	_, _ = fmt.Fprintf(out, "release tag %s verified at %s (latest, points to release-sha)\n", in.Tag, in.ReleaseSHA)

	return nil
}

func verifyPreparedSource(ctx context.Context, git tagVerifyGit, in VerifyReleaseTagInput) error {
	if in.ExpectedParent == "" {
		return nil
	}

	parents, err := git.CommitParents(ctx, in.ReleaseSHA)
	if err != nil {
		return fmt.Errorf("verify-release-tag: read release commit parents: %w", err)
	}

	if len(parents) != 1 || parents[0] != in.ExpectedParent {
		return fmt.Errorf("verify-release-tag: release commit %s is not the single child of the authorised source %s (parents: %v): %w",
			in.ReleaseSHA, in.ExpectedParent, parents, errs.ErrValidation)
	}

	if in.ChangelogOnly {
		return verifyChangelogOnly(ctx, git, in)
	}

	return nil
}

func verifyChangelogOnly(ctx context.Context, git tagVerifyGit, in VerifyReleaseTagInput) error {
	request, err := git.RevParse(ctx, "refs/tags/release-request/"+in.Tag+"^{commit}")
	if err != nil {
		return fmt.Errorf("verify-release-tag: resolve release request: %w", err)
	}

	if request != in.ExpectedParent {
		return fmt.Errorf("verify-release-tag: release request does not match the tested commit: %w", errs.ErrValidation)
	}

	changed, err := git.ChangedPaths(ctx, in.ExpectedParent, in.ReleaseSHA)
	if err != nil {
		return fmt.Errorf("verify-release-tag: inspect prepared changes: %w", err)
	}

	if len(changed) != 1 || changed[0] != "CHANGELOG.md" {
		return fmt.Errorf("verify-release-tag: prepared release must change only CHANGELOG.md; changed paths: %q: %w", changed, errs.ErrValidation)
	}

	return nil
}
