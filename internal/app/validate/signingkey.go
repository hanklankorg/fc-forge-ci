// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package validate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// publicKeyDeriver is the cosign surface this check needs: derive the public
// half of a signing key. Declared here rather than imported from the adapter
// so the check can be tested without cosign on PATH.
type publicKeyDeriver interface {
	PublicKey(ctx context.Context, keyRef string, out, errOut io.Writer) error
}

// SigningKeyInput drives `forge-ci validate signing-key`.
//
// The question it answers is the one a signer must ask before it signs
// anything: is the private key this job was handed the key whose signatures
// consumers already trust? A secret can be rotated, given the wrong scope, or pasted from
// the wrong vault, and none of that is visible from inside the job. Deriving
// the public half and comparing it to the public key the project publishes is
// what turns "a key was provided" into "the expected key was provided".
type SigningKeyInput struct {
	// KeyRef is the cosign key reference, e.g. env://COSIGN_KEY. Required.
	KeyRef string

	// TrustedPublicKeyPath is the public key the derived half must equal.
	// Which key is trusted is the caller's policy; this check only enforces
	// that the signing key matches whatever the caller pinned. Required.
	TrustedPublicKeyPath string

	// TrustedDigestPath optionally names a sidecar recording the trusted
	// key's SHA-256, so a swapped trust anchor is caught before it is used
	// as the comparison target. Its first field is the digest, in the format
	// sha256sum writes.
	TrustedDigestPath string
}

// VerifySigningKey derives the public half of the signing key and fails unless
// it matches the trusted public key.
//
// Only digests are reported. The derived public key is not written anywhere the
// caller did not ask for, and the private material never leaves the cosign
// subprocess, which is the reason this lives here rather than in workflow
// shell: a shell implementation has to stage both halves through files the
// job's later steps can read.
func VerifySigningKey(ctx context.Context, cosign publicKeyDeriver, out io.Writer, in SigningKeyInput) error {
	if in.KeyRef == "" || in.TrustedPublicKeyPath == "" {
		return fmt.Errorf("validate signing-key: key and trusted public key are required: %w", errs.ErrUsage)
	}

	trusted, err := os.ReadFile(in.TrustedPublicKeyPath) //nolint:gosec // caller-pinned trust anchor path.
	if err != nil {
		return fmt.Errorf("validate signing-key: read trusted public key: %w", err)
	}

	if len(bytes.TrimSpace(trusted)) == 0 {
		return fmt.Errorf("validate signing-key: trusted public key is empty: %s: %w", in.TrustedPublicKeyPath, errs.ErrValidation)
	}

	trustedDigest := sha256.Sum256(trusted)
	trustedHex := hex.EncodeToString(trustedDigest[:])

	if in.TrustedDigestPath != "" {
		if err := verifyRecordedDigest(in.TrustedDigestPath, trustedHex); err != nil {
			return err
		}
	}

	var derived bytes.Buffer
	if err := cosign.PublicKey(ctx, in.KeyRef, &derived, io.Discard); err != nil {
		return fmt.Errorf("validate signing-key: derive public key: %w", err)
	}

	if derived.Len() == 0 {
		return fmt.Errorf("validate signing-key: deriving the public key produced no output: %w", errs.ErrValidation)
	}

	derivedDigest := sha256.Sum256(derived.Bytes())
	derivedHex := hex.EncodeToString(derivedDigest[:])

	if derivedHex != trustedHex {
		return fmt.Errorf(
			"validate signing-key: signing key does not match the trusted public key\n  trusted public-key sha256: %s\n  derived public-key sha256: %s: %w",
			trustedHex, derivedHex, errs.ErrValidation)
	}

	_, _ = fmt.Fprintf(out, "Signing key matches the trusted public key (%s).\n", trustedHex)

	return nil
}

// verifyRecordedDigest fails unless the sidecar records exactly the digest the
// trusted key file actually has. A malformed sidecar is a failure, not a skip:
// a trust anchor whose recorded digest cannot be read has not been checked.
func verifyRecordedDigest(path, actual string) error {
	body, err := os.ReadFile(path) //nolint:gosec // caller-pinned trust anchor sidecar.
	if err != nil {
		return fmt.Errorf("validate signing-key: read trusted public key digest: %w", err)
	}

	fields := strings.Fields(string(body))
	if len(fields) == 0 {
		return fmt.Errorf("validate signing-key: trusted public key digest is empty: %s: %w", path, errs.ErrValidation)
	}

	recorded := fields[0]
	if len(recorded) != sha256.Size*2 || strings.ToLower(recorded) != recorded {
		return fmt.Errorf("validate signing-key: trusted public key digest is malformed: %s: %w", path, errs.ErrValidation)
	}

	if _, err := hex.DecodeString(recorded); err != nil {
		return fmt.Errorf("validate signing-key: trusted public key digest is malformed: %s: %w", path, errs.ErrValidation)
	}

	if recorded != actual {
		return fmt.Errorf(
			"validate signing-key: trusted public key does not match its recorded digest\n  recorded: %s\n  actual:   %s: %w",
			recorded, actual, errs.ErrValidation)
	}

	return nil
}
