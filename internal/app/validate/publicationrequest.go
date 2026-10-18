// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package validate

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domaingit "github.com/diggsweden/forge-ci/internal/domain/git"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/version"
)

// PublicationRequestInput binds a publisher to the human request and the exact
// prepared release commit. AuthorizationDir is the reviewed checkout root,
// which also holds the committed signer allowlists for monorepo publications.
type PublicationRequestInput struct {
	RequestTag          string
	ReleaseSHA          string
	AuthorizationDir    string
	ReleaseGPGPublicKey string
}

// PublicationRequest rechecks authority in the publishing job, including on a
// rerun that does not execute the earlier preflight job. No credentials are
// imported and no project build tool is executed by this check.
func PublicationRequest(ctx context.Context, repo gitOps, out io.Writer, annot output.Annotator, in PublicationRequestInput) error {
	request := strings.TrimPrefix(in.RequestTag, "refs/tags/")

	final, ok := version.ReleaseRequestVersion(request)
	if !ok || !version.IsStableSemverTag(final) {
		return fmt.Errorf("publication requires a signed release-request/vMAJOR.MINOR.PATCH tag; snapshot triggers cannot authorize a permanent release: %w", errs.ErrPermissionDenied)
	}

	if !domaingit.ValidCommitSHA(in.ReleaseSHA) {
		return fmt.Errorf("publication requires the full prepared --release-sha, not a moving branch or tag: %w", errs.ErrInvalidConfig)
	}

	for _, ref := range []string{"HEAD", "refs/tags/" + final + "^{commit}"} {
		sha, err := repo.RevParse(ctx, ref)
		if err != nil {
			return fmt.Errorf("resolve publication source: %w", err)
		}

		if sha != in.ReleaseSHA {
			return fmt.Errorf("%s does not name the prepared release commit %s: %w", ref, in.ReleaseSHA, errs.ErrPermissionDenied)
		}
	}

	if err := requireReleasedFromRequest(ctx, repo, out, request, final); err != nil {
		return err
	}

	input := TagSignatureInput{
		Tag: request, RequireAllowlistedSigner: true,
		AllowedSignersPath: filepath.Join(in.AuthorizationDir, defaultAllowedSignersPath),
		AllowedGPGKeysPath: filepath.Join(in.AuthorizationDir, defaultAllowedGPGKeysPath),
	}
	if err := TagSignature(ctx, repo, out, annot, input); err != nil {
		return err
	}

	input.Tag = final
	input.RequireAllowlistedSigner = false
	input.RequireValidSignature = true
	input.ReleaseGPGPublicKey = []byte(in.ReleaseGPGPublicKey)

	return TagSignature(ctx, repo, out, annot, input)
}
