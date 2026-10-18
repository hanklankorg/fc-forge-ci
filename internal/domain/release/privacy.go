// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"fmt"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// ValidateTransparencyConsent gates public disclosure before signing. Unknown
// visibility is not public. An explicit public choice is consent; key-based
// signing without transparency and OpenPGP need no public-log consent.
func ValidateTransparencyConsent(visibility string, transparency Transparency, explicit, enabled bool) error {
	switch visibility {
	case "", "unknown", "public", "private", "internal":
	default:
		return fmt.Errorf("repository visibility must be public, private, internal or unknown: %w", errs.ErrInvalidConfig)
	}

	if !enabled || transparency == "" || !transparency.PublishesToLog() || explicit || visibility == "public" {
		return nil
	}

	return fmt.Errorf("repository is non-public or its visibility is unknown; public transparency can permanently disclose signing identity and release metadata. Explicitly set sign.transparency: public to consent, or use key-based signing with sign.transparency: none: %w", errs.ErrInvalidConfig)
}
