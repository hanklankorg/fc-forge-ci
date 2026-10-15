// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domaingit "github.com/diggsweden/forge-ci/internal/domain/git"
	"github.com/diggsweden/forge-ci/internal/domain/version"
	"github.com/diggsweden/forge-ci/internal/runcontext"
)

// releaseRequestGit is the git surface VerifyReleaseRequest needs. Remote
// methods query origin at the trust boundary; local methods inspect the checked
// out repository after fetching the immutable request tag when needed.
type releaseRequestGit interface {
	RevParse(ctx context.Context, ref string) (string, error)
	FetchTagFromRemote(ctx context.Context, remote, tag string, cred runcontext.Credential) error
	RemoteTagObject(ctx context.Context, remote, tag string, cred runcontext.Credential) (string, error)
	RemoteTagExists(ctx context.Context, remote, tag string, cred runcontext.Credential) (bool, error)
	CatFileType(ctx context.Context, ref string) (string, error)
	CatFileTag(ctx context.Context, ref string) (string, error)
	VerifyTagSSHAgainstAllowedSigners(ctx context.Context, tag, allowedSignersPath string) (ok bool, output string, err error)
}

// VerifyReleaseRequestInput drives VerifyReleaseRequest.
type VerifyReleaseRequestInput struct {
	ReleaseRequest     string // release-request/vMAJOR.MINOR.PATCH
	ReleaseTag         string // vMAJOR.MINOR.PATCH
	AllowedSignersPath string // OpenSSH allowed_signers file
	Remote             string // empty defaults to origin
	ExpectedCommit     string // when set, the commit the request tag and the checkout must be at
	SkipSignature      bool   // adopt-time escape hatch; every other check still runs
	// Token authenticates every read of origin, so a private repository's
	// request can be verified. Absent means anonymous reads.
	Token runcontext.Credential
}

// VerifyReleaseRequest verifies the signed human authorisation tag that asks CI
// to create a final immutable release tag. It is the Go port of
// forgejo-ci's verify-release-request.sh and preserves that boundary:
// stable request/tag names only, local request object must match origin, the
// final tag must not already exist, and the request tag must be annotated and
// SSH-signed by an allowed signer.
//
//nolint:cyclop // mirrors a linear trust-boundary shell script; splitting would hide the contract.
func VerifyReleaseRequest(ctx context.Context, git releaseRequestGit, out io.Writer, in VerifyReleaseRequestInput) error {
	if err := validateRequestInputs(in); err != nil {
		return err
	}

	remote := in.Remote
	if remote == "" {
		remote = "origin"
	}

	requestRef := "refs/tags/" + in.ReleaseRequest
	if _, err := git.RevParse(ctx, requestRef); err != nil {
		if err := git.FetchTagFromRemote(ctx, remote, in.ReleaseRequest, in.Token); err != nil {
			return fmt.Errorf("verify-release-request: fetch release request tag: %w", err)
		}
	}

	localRequestObject, err := git.RevParse(ctx, requestRef)
	if err != nil {
		return fmt.Errorf("verify-release-request: resolve local release request tag: %w", err)
	}

	remoteRequestObject, err := git.RemoteTagObject(ctx, remote, in.ReleaseRequest, in.Token)
	if err != nil {
		return fmt.Errorf("verify-release-request: resolve origin release request tag: %w", err)
	}

	if localRequestObject != remoteRequestObject {
		return fmt.Errorf("verify-release-request: local release request tag object does not match origin\n  local:  %s\n  origin: %s: %w", localRequestObject, remoteRequestObject, errs.ErrValidation)
	}

	objType, err := git.CatFileType(ctx, requestRef)
	if err != nil {
		return fmt.Errorf("verify-release-request: inspect release request tag object: %w", err)
	}

	if objType != "tag" {
		return fmt.Errorf("verify-release-request: release request must be an annotated signed tag: %s: %w", in.ReleaseRequest, errs.ErrValidation)
	}

	if nameErr := requireSelfNamedTag(ctx, git, requestRef, in.ReleaseRequest); nameErr != nil {
		return nameErr
	}

	if commitErr := requireAuthorisedCommit(ctx, git, requestRef, in.ExpectedCommit); commitErr != nil {
		return commitErr
	}

	finalExists, err := git.RemoteTagExists(ctx, remote, in.ReleaseTag, in.Token)
	if err != nil {
		return fmt.Errorf("verify-release-request: check final release tag on origin: %w", err)
	}

	if finalExists {
		return fmt.Errorf("verify-release-request: final release tag already exists on origin; refusing to recreate it: %s: %w", in.ReleaseTag, errs.ErrValidation)
	}

	if !in.SkipSignature {
		verified, verifyOutput, signatureErr := git.VerifyTagSSHAgainstAllowedSigners(ctx, in.ReleaseRequest, in.AllowedSignersPath)
		if signatureErr != nil {
			return fmt.Errorf("verify-release-request: verify release request SSH signature: %w", signatureErr)
		}

		if !verified {
			return fmt.Errorf("verify-release-request: SSH signature rejected by allowed_signers: %s: %w", strings.TrimSpace(verifyOutput), errs.ErrPermissionDenied)
		}
	}

	// A tag is mutable on the forge. Re-read origin after the checks above, so
	// a request replaced while they ran does not pass on the earlier object.
	recheckObject, recheckErr := git.RemoteTagObject(ctx, remote, in.ReleaseRequest, in.Token)
	if recheckErr != nil {
		return fmt.Errorf("verify-release-request: re-resolve origin release request tag: %w", recheckErr)
	}

	if recheckObject != remoteRequestObject {
		return fmt.Errorf("verify-release-request: release request tag object on origin changed during validation\n  before: %s\n  after:  %s: %w", remoteRequestObject, recheckObject, errs.ErrValidation)
	}

	if in.SkipSignature {
		_, _ = fmt.Fprintf(out, "Release request validated WITHOUT signature verification: %s -> %s\n", in.ReleaseRequest, in.ReleaseTag)
	} else {
		_, _ = fmt.Fprintf(out, "Release request signature verified: %s -> %s\n", in.ReleaseRequest, in.ReleaseTag)
	}

	return nil
}

// validateRequestInputs checks the caller's values before any repository or
// remote is touched: the names are single-line, the request and tag are stable
// semver naming the same version, and the allowed-signers file is readable.
//
//nolint:cyclop // one branch per refusal, in the order the boundary states them; splitting would hide the contract.
func validateRequestInputs(in VerifyReleaseRequestInput) error {
	if in.ReleaseRequest == "" || in.ReleaseTag == "" || (in.AllowedSignersPath == "" && !in.SkipSignature) {
		return fmt.Errorf("verify-release-request: release-request, tag, and allowed-signers-file are required: %w", errs.ErrUsage)
	}

	if in.ExpectedCommit != "" && !domaingit.ValidCommitSHA(in.ExpectedCommit) {
		return fmt.Errorf("verify-release-request: expected-commit must be a full commit OID: %s: %w", in.ExpectedCommit, errs.ErrUsage)
	}

	if hasLineBreak(in.ReleaseRequest) || hasLineBreak(in.ReleaseTag) || hasLineBreak(in.AllowedSignersPath) {
		return fmt.Errorf("verify-release-request: release request inputs must be single-line values: %w", errs.ErrValidation)
	}

	requestTag, ok := version.ReleaseRequestVersion(in.ReleaseRequest)
	if !ok || in.ReleaseRequest != version.ReleaseRequestPrefix+requestTag || !version.IsStableSemverTag(requestTag) {
		return fmt.Errorf("verify-release-request: release request must look like release-request/vMAJOR.MINOR.PATCH: %s: %w", in.ReleaseRequest, errs.ErrValidation)
	}

	if !version.IsStableSemverTag(in.ReleaseTag) {
		return fmt.Errorf("verify-release-request: release tag must look like stable vMAJOR.MINOR.PATCH: %s: %w", in.ReleaseTag, errs.ErrValidation)
	}

	if requestTag != in.ReleaseTag {
		return fmt.Errorf("verify-release-request: release request %s does not authorize %s: %w", in.ReleaseRequest, in.ReleaseTag, errs.ErrValidation)
	}

	if !in.SkipSignature {
		if err := requireNonEmptyFile(in.AllowedSignersPath); err != nil {
			return err
		}
	}

	return nil
}

// requireSelfNamedTag fails unless the tag object names itself. A tag object
// built elsewhere and pushed under this name carries the name it was signed
// with, so the signature can be valid while the object authorises another
// release.
func requireSelfNamedTag(ctx context.Context, git releaseRequestGit, requestRef, request string) error {
	tagObject, err := git.CatFileTag(ctx, requestRef)
	if err != nil {
		return fmt.Errorf("verify-release-request: read release request tag object: %w", err)
	}

	if name, ok := taggedName(tagObject); !ok || name != request {
		return fmt.Errorf("verify-release-request: release request tag object names %q, not %s: %w", name, request, errs.ErrValidation)
	}

	return nil
}

// requireAuthorisedCommit binds the request tag and the checkout to the commit
// the caller took from its trusted event context. An empty expected commit
// leaves both unbound, which is the behaviour of callers that validate the
// request before checking anything out.
func requireAuthorisedCommit(ctx context.Context, git releaseRequestGit, requestRef, expected string) error {
	if expected == "" {
		return nil
	}

	if err := requireCommit(ctx, git, requestRef, expected, "release request tag"); err != nil {
		return err
	}

	return requireCommit(ctx, git, "HEAD", expected, "checkout")
}

// taggedName returns the name the tag object carries in its own header.
func taggedName(tagObject string) (string, bool) {
	for line := range strings.SplitSeq(tagObject, "\n") {
		if after, ok := strings.CutPrefix(line, "tag "); ok {
			return after, true
		}

		if line == "" {
			break
		}
	}

	return "", false
}

// requireCommit fails unless ref peels to want.
func requireCommit(ctx context.Context, git releaseRequestGit, ref, want, subject string) error {
	got, err := git.RevParse(ctx, ref+"^{commit}")
	if err != nil {
		return fmt.Errorf("verify-release-request: resolve %s commit: %w", subject, err)
	}

	if got != want {
		return fmt.Errorf("verify-release-request: %s is at %s, not the authorised %s: %w", subject, got, want, errs.ErrValidation)
	}

	return nil
}

func hasLineBreak(value string) bool {
	return strings.ContainsAny(value, "\n\r")
}

func requireNonEmptyFile(path string) error {
	file, err := openReleaseFile(path)
	if err != nil {
		return fmt.Errorf("verify-release-request: allowed signers must be a non-empty regular file without symlinks: %s: %w", path, errs.ErrValidation)
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil || info.Size() == 0 {
		return fmt.Errorf("verify-release-request: release request SSH allowed signers file is missing or empty: %s: %w", path, errs.ErrValidation)
	}

	return nil
}
