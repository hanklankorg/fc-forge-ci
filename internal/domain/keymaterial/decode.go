// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package keymaterial decodes credential material at input boundaries. It does
// not parse keys or establish trust; the owning cryptographic parser does that.
package keymaterial

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// MaxBytes bounds decoded signing material, including mobile signing bundles.
const MaxBytes = 16 << 20

// ErrSizeLimit lets a caller preserve the decoder's bound even when another
// parser owns malformed-format diagnostics.
var ErrSizeLimit = errors.New("signing material exceeds size limit")

// Base64 accepts standard padded base64 with ASCII space, tab, CR and LF
// wrapping. It preserves decoded bytes exactly and never echoes secret input.
// A bound is checked before allocating the compacted string or decoded output.
func Base64(encoded string) ([]byte, error) {
	if len(encoded) > 2*MaxBytes {
		return nil, fmt.Errorf("%w: %w", ErrSizeLimit, errs.ErrMalformedInput)
	}

	// encoding/base64 already ignores CR and LF. Only the additional ASCII
	// wrapping accepted by forge secrets needs normalization here.
	compact := strings.NewReplacer(" ", "", "\t", "").Replace(encoded)

	body, err := base64.StdEncoding.DecodeString(compact)
	if len(body) > MaxBytes {
		clear(body)

		return nil, fmt.Errorf("%w: %w", ErrSizeLimit, errs.ErrMalformedInput)
	}

	if err != nil || len(body) == 0 {
		clear(body)

		return nil, fmt.Errorf("signing material must be nonempty standard base64 of at most 16 MiB: %w", errs.ErrMalformedInput)
	}

	return body, nil
}

// Key accepts an armored key verbatim, or decodes one standard base64 envelope.
// It intentionally leaves key-format and encryption checks with the caller.
func Key(text string) ([]byte, error) {
	if len(text) > 2*MaxBytes {
		return nil, fmt.Errorf("%w: %w", ErrSizeLimit, errs.ErrMalformedInput)
	}

	if strings.HasPrefix(strings.TrimLeft(text, " \t\r\n"), "-----") {
		if len(text) > MaxBytes {
			return nil, fmt.Errorf("%w: %w", ErrSizeLimit, errs.ErrMalformedInput)
		}

		return []byte(text), nil
	}

	return Base64(text)
}
