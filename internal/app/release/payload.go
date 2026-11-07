// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"context"
	"fmt"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// CanonicalPayloadDigest uses the existing dist digest algorithm with a stable
// '.' prefix over dist/ and premade-sboms/ together. Legacy APIs are unchanged.
func CanonicalPayloadDigest(dir string) (string, error) {
	root, err := pathsafe.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()

	if err := domainrelease.AdmitCanonicalPayload(root.FS()); err != nil {
		return "", err
	}

	return domainrelease.DistDigest(root.FS(), ".")
}

// VerifyCanonicalPayload checks the whole canonical workspace, including both
// producer ledgers and pinned base evidence, before a signer consumes it.
func VerifyCanonicalPayload(dir, expected string) error {
	if expected == "" {
		return fmt.Errorf("expected digest required: %w", errs.ErrUsage)
	}

	actual, err := CanonicalPayloadDigest(dir)
	if err != nil {
		return err
	}

	if actual != expected {
		return fmt.Errorf("canonical payload digest mismatch: got %s, want %s: %w", actual, expected, errs.ErrValidation)
	}

	return nil
}

// StageVerifiedCanonicalPayload installs a new workspace root. Signers may use
// it as their evidence root; the canonical ledger paths are never rewritten.
func StageVerifiedCanonicalPayload(ctx context.Context, source, destination, expected string) error {
	return stageVerifiedDist(ctx, source, destination, expected, ".", domainrelease.AdmitCanonicalPayload)
}
