// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"context"
	"fmt"
	"io"
	pathpkg "path"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domaingit "github.com/diggsweden/forge-ci/internal/domain/git"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
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
	PathsDiff(ctx context.Context, before, after string, paths []string) (string, error)
	// FileAt returns a regular file's bytes, its Git mode and whether
	// commit has the path.
	FileAt(ctx context.Context, commit, path string) (string, string, bool, error)
}

// VerifyReleaseTagInput drives VerifyReleaseTag.
type VerifyReleaseTagInput struct {
	ReleasePolicy string
	ReleaseSHA    string // the commit the release is built from
	Tag           string // the release tag, e.g. v1.2.3
	RepoURL       string // original repository argument for ls-remote; empty defaults to origin
	Token         runcontext.Credential
	// ExpectedParent, when set, requires the release commit to be the single
	// child of that commit: the caller prepared exactly one commit on top of
	// the source it authorised. Also re-read release-request/<Tag> on the
	// original remote: its object must match the local request, and its peeled
	// commit must equal this explicitly authorised SHA. This is freshness and
	// source binding, not a replacement for signed-request authorization.
	ExpectedParent string
	// RequestIsRelease selects the explicit no-bump route: the release must
	// equal ExpectedParent, rather than be its child. Request freshness still
	// applies. It cannot be combined with a prepared-change profile.
	RequestIsRelease bool
	// ChangelogOnly admits exactly a CHANGELOG.md change and binds the local
	// release-request/<tag> to ExpectedParent, for a tested prepare candidate.
	ChangelogOnly bool
	// PreparePlan, with ChangelogOnly, also admits the version bump of the
	// plan's projects when its version_bump target runs: the changelog copy
	// in each project's directory, each native project file only as a
	// version substitution to the tag's version (the text check the prepare
	// job applied before committing it), and the engine-written version files
	// of the other types by name.
	PreparePlan *pipeline.ReleasePrepareStagePlan
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

	parsed, err := version.ValidateReleaseTag(in.Tag, in.ReleasePolicy)
	if err != nil {
		return fmt.Errorf("verify-release-tag: %w", err)
	}

	if in.ExpectedParent != "" && !domaingit.ValidCommitSHA(in.ExpectedParent) {
		return fmt.Errorf("verify-release-tag: expected-parent must be a full lowercase commit ID: %w", errs.ErrUsage)
	}

	if err := validateRequestRoute(in); err != nil {
		return err
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

	if err = verifyPreparedSource(ctx, git, in, repoURL); err != nil {
		return err
	}

	tags, err := git.RemoteVersionTags(ctx, repoURL, in.Token)
	if err != nil {
		return fmt.Errorf("verify-release-tag: list remote tags: %w", err)
	}

	latestPolicy := in.ReleasePolicy
	if parsed.Prerelease == "" {
		latestPolicy = version.ReleasePolicyStable
	}

	if latest := version.LatestReleaseTag(tags, latestPolicy); latest != "" && latest != in.Tag {
		return fmt.Errorf("verify-release-tag: tag %s has been superseded by %s: %w", in.Tag, latest, errs.ErrValidation)
	}

	_, _ = fmt.Fprintf(out, "release tag %s verified at %s (latest, points to release-sha)\n", in.Tag, in.ReleaseSHA)

	return nil
}

func validateRequestRoute(in VerifyReleaseTagInput) error {
	if in.RequestIsRelease {
		if in.ExpectedParent == "" || in.ChangelogOnly || in.PreparePlan != nil {
			return fmt.Errorf("verify-release-tag: request-is-release requires expected-parent and no prepared-change profile: %w", errs.ErrUsage)
		}

		if in.ExpectedParent != in.ReleaseSHA {
			return fmt.Errorf("verify-release-tag: no-bump release must equal the authorised request source: %w", errs.ErrValidation)
		}
	} else if in.ExpectedParent == in.ReleaseSHA {
		return fmt.Errorf("verify-release-tag: prepared release must differ from the authorised request source: %w", errs.ErrValidation)
	}

	return nil
}

func verifyPreparedSource(ctx context.Context, git tagVerifyGit, in VerifyReleaseTagInput, repository string) error {
	if in.ExpectedParent == "" {
		return nil
	}

	if in.RequestIsRelease {
		return verifyPreparedRequest(ctx, git, in, repository)
	}

	parents, err := git.CommitParents(ctx, in.ReleaseSHA)
	if err != nil {
		return fmt.Errorf("verify-release-tag: read release commit parents: %w", err)
	}

	if len(parents) != 1 || parents[0] != in.ExpectedParent {
		return fmt.Errorf("verify-release-tag: release commit %s is not the single child of the authorised source %s (parents: %v): %w",
			in.ReleaseSHA, in.ExpectedParent, parents, errs.ErrValidation)
	}

	if err := verifyPreparedRequest(ctx, git, in, repository); err != nil {
		return err
	}

	if in.ChangelogOnly {
		return verifyChangelogOnly(ctx, git, in)
	}

	return nil
}

func verifyPreparedRequest(ctx context.Context, git tagVerifyGit, in VerifyReleaseTagInput, repository string) error {
	requestTag := version.ReleaseRequestPrefix + in.Tag
	requestRef := "refs/tags/" + requestTag

	request, err := git.RevParse(ctx, requestRef+"^{commit}")
	if err != nil {
		return fmt.Errorf("verify-release-tag: resolve release request: %w", err)
	}

	if request != in.ExpectedParent {
		return fmt.Errorf("verify-release-tag: release request does not match the tested commit: %w", errs.ErrValidation)
	}

	localObject, err := git.RevParse(ctx, requestRef)
	if err != nil {
		return fmt.Errorf("verify-release-tag: resolve local request object: %w", err)
	}

	remoteObject, err := git.RemoteTagObjectAtURL(ctx, repository, requestTag, in.Token)
	if err != nil {
		return fmt.Errorf("verify-release-tag: resolve remote request object: %w", err)
	}

	if !domaingit.ValidCommitSHA(localObject) || !domaingit.ValidCommitSHA(remoteObject) || localObject != remoteObject {
		return fmt.Errorf("verify-release-tag: local release request object does not match remote object: %w", errs.ErrValidation)
	}

	remoteCommit, err := git.RemoteTagCommit(ctx, repository, requestTag, in.Token)
	if err != nil {
		return fmt.Errorf("verify-release-tag: resolve remote request commit: %w", err)
	}

	if remoteCommit != in.ExpectedParent {
		return fmt.Errorf("verify-release-tag: remote release request does not match the authorised source: %w", errs.ErrValidation)
	}

	return nil
}

func verifyChangelogOnly(ctx context.Context, git tagVerifyGit, in VerifyReleaseTagInput) error {
	changed, err := git.ChangedPaths(ctx, in.ExpectedParent, in.ReleaseSHA)
	if err != nil {
		return fmt.Errorf("verify-release-tag: inspect prepared changes: %w", err)
	}

	if in.PreparePlan == nil || !in.PreparePlan.Targets.VersionBump.Runs {
		if len(changed) != 1 || changed[0] != changelogFile {
			return fmt.Errorf("verify-release-tag: prepared release must change only CHANGELOG.md; changed paths: %q: %w", changed, errs.ErrValidation)
		}

		return verifyChangelogFile(ctx, git, in, changelogFile)
	}

	return verifyChangelogAndPlannedBumps(ctx, git, in, changed)
}

const changelogFile = "CHANGELOG.md"

// plannedBumpScopes gives each planned project's bump scope, and the
// version file of each project the engine bumps as text with its kind.
func plannedBumpScopes(items []pipeline.PlannedArtifact) ([]version.BumpScope, map[string]projecttype.Type) {
	scopes := make([]version.BumpScope, len(items))
	text := make(map[string]projecttype.Type)

	for index, item := range items {
		scopes[index] = version.BumpScope{ProjectType: item.ProjectType, WorkingDir: pathpkg.Clean(item.WorkingDirectory)}
		if item.WorkingDirectory == "" {
			scopes[index].WorkingDir = "."
		}

		if kind, file, ok := version.TextBumpTarget(item.ProjectType, item.BuildType == config.BuildTypeLibrary, plannedGradleVersionFile(item), ""); ok {
			text[pathpkg.Join(scopes[index].WorkingDir, file)] = kind
		}
	}

	return scopes, text
}

// verifyChangelogAndPlannedBumps admits CHANGELOG.md and what the plan's
// version bump writes, nothing else: each native project's files as a
// verified version substitution, the changelog copies, and each other
// planned project's version file with exactly the bytes the engine's text
// bump writes over the parent's.
func verifyChangelogAndPlannedBumps(ctx context.Context, git tagVerifyGit, in VerifyReleaseTagInput, changed []string) error {
	scopes, text := plannedBumpScopes(in.PreparePlan.Targets.VersionBump.Items)
	release := strings.TrimPrefix(in.Tag, "v")
	changelog, native := false, make(map[int][]string)

	for _, path := range changed {
		kind, textFile := text[path]

		switch {
		case path == changelogFile:
			changelog = true

			if err := verifyChangelogFile(ctx, git, in, path); err != nil {
				return err
			}
		case pathpkg.Base(path) == changelogFile && slices.ContainsFunc(scopes, func(scope version.BumpScope) bool { return scope.WorkingDir == pathpkg.Dir(path) }):
			if err := verifyChangelogFile(ctx, git, in, path); err != nil {
				return err
			}
		case version.PlannedBumpScope(scopes, path) >= 0:
			index := version.PlannedBumpScope(scopes, path)
			native[index] = append(native[index], path)
		case textFile:
			if err := verifyTextBump(ctx, git, in, kind, path, release); err != nil {
				return err
			}
		default:
			return fmt.Errorf("verify-release-tag: prepared release must change only CHANGELOG.md and the planned version bumps; changed paths: %q: %w",
				changed, errs.ErrValidation)
		}
	}

	if !changelog {
		return fmt.Errorf("verify-release-tag: prepared release must change CHANGELOG.md; changed paths: %q: %w", changed, errs.ErrValidation)
	}

	for index, paths := range native {
		files, _ := version.BumpPatchFiles(scopes[index].ProjectType)

		patch, err := git.PathsDiff(ctx, in.ExpectedParent, in.ReleaseSHA, paths)
		if err != nil {
			return fmt.Errorf("verify-release-tag: read the version bump: %w", err)
		}

		if _, err := version.VerifyBumpPatch([]byte(patch), files, release); err != nil {
			return fmt.Errorf("verify-release-tag: %w", err)
		}
	}

	return nil
}

// plannedGradleVersionFile is the Gradle version file a planned project names.
func plannedGradleVersionFile(item pipeline.PlannedArtifact) string {
	switch {
	case item.Gradle != nil:
		return item.Gradle.GradleVersionFile
	case item.GradleAndroid != nil:
		return item.GradleAndroid.GradleVersionFile
	default:
		return ""
	}
}

// verifyChangelogFile admits a changelog the release commit changes only as
// a regular file at its parent's mode, or a plain one where the release
// creates it: the name alone admits no link, deletion or executable.
func verifyChangelogFile(ctx context.Context, git tagVerifyGit, in VerifyReleaseTagInput, path string) error {
	_, beforeMode, existed, err := git.FileAt(ctx, in.ExpectedParent, path)
	if err != nil {
		return fmt.Errorf("verify-release-tag: read %s before the release: %w", path, err)
	}

	_, afterMode, written, err := git.FileAt(ctx, in.ReleaseSHA, path)
	if err != nil {
		return fmt.Errorf("verify-release-tag: read %s in the release: %w", path, err)
	}

	if !existed {
		beforeMode = "100644"
	}

	if !written || afterMode != beforeMode {
		return fmt.Errorf("verify-release-tag: %s must be a regular file at its parent's mode in the release: %w", path, errs.ErrValidation)
	}

	return nil
}

// verifyTextBump requires the release's version file to hold exactly what the
// engine's text bump writes over the parent's: a version file is build code,
// so a name match alone admits nothing.
func verifyTextBump(ctx context.Context, git tagVerifyGit, in VerifyReleaseTagInput, kind projecttype.Type, path, release string) error {
	before, beforeMode, exists, err := git.FileAt(ctx, in.ExpectedParent, path)
	if err != nil {
		return fmt.Errorf("verify-release-tag: read %s before the bump: %w", path, err)
	}

	after, afterMode, written, err := git.FileAt(ctx, in.ReleaseSHA, path)
	if err != nil {
		return fmt.Errorf("verify-release-tag: read %s after the bump: %w", path, err)
	}

	bump, err := version.TextBump(kind, before, exists, release)
	if err != nil {
		return fmt.Errorf("verify-release-tag: %s: %w", path, err)
	}

	// The bump writes bytes, never the mode: a kept file keeps the parent's,
	// and a file the bump creates is a plain one.
	if !exists {
		beforeMode = "100644"
	}

	if !written || !bump.Writes || after != bump.Body || afterMode != beforeMode {
		return fmt.Errorf("verify-release-tag: %s holds more than the version bump to %s: %w", path, release, errs.ErrValidation)
	}

	return nil
}
