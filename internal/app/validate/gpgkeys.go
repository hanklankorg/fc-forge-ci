// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package validate

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/pgp"
)

// GPGKeysFormat checks that every armor block is a complete public-key
// block containing keys accepted by the release verifier's key-bundle parser.
func GPGKeysFormat(path string) error {
	data, err := os.ReadFile(path) //nolint:gosec // G304: explicitly selected local allowlist; read-only validation.
	if err != nil {
		return fmt.Errorf("read allowed GPG keys: %w", err)
	}

	blocks := pgp.SplitArmorBlocks(data)
	if len(blocks) == 0 {
		return fmt.Errorf("%s: no PGP PUBLIC KEY BLOCK found: %w", path, errs.ErrMalformedInput)
	}

	var problems []error

	for index, block := range blocks {
		block = bytes.TrimSpace(block)

		const (
			begin = "-----BEGIN PGP PUBLIC KEY BLOCK-----"
			end   = "-----END PGP PUBLIC KEY BLOCK-----"
		)

		// The armor reader tolerates missing end markers; require a complete
		// public envelope before using the same key parser as verification.
		if !bytes.HasPrefix(block, []byte(begin+"\n")) && !bytes.HasPrefix(block, []byte(begin+"\r\n")) ||
			!bytes.HasSuffix(block, []byte(end)) || bytes.Count(block, []byte("-----BEGIN ")) != 1 {
			problems = append(problems, fmt.Errorf("%s: block %d: expected complete PGP PUBLIC KEY BLOCK markers: %w", path, index+1, errs.ErrMalformedInput))

			continue
		}

		fingerprints, err := pgp.PrimaryFingerprints(block)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: block %d: %w", path, index+1, err))
		} else if len(fingerprints) == 0 {
			problems = append(problems, fmt.Errorf("%s: block %d: contains no public keys: %w", path, index+1, errs.ErrMalformedInput))
		}
	}

	return errors.Join(problems...)
}
