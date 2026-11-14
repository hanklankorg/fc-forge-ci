// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"testing"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
)

func TestKeylessNeedsIssuer_OnlyWhenTheForgePublishesNone(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		explicitIssuer string
		keylessCapable bool
		want           bool
	}{
		{"forge_keyless_no_explicit", "", true, false},                   // GitHub: forge publishes an issuer
		{"forge_not_keyless_no_explicit", "", false, true},               // Forgejo: warn
		{"forge_not_keyless_explicit_issuer", "https://x", false, false}, // operator supplied → trust them
		{"forge_keyless_explicit_issuer", "https://x", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := apprelease.KeylessNeedsIssuer(tc.explicitIssuer, tc.keylessCapable); got != tc.want {
				t.Errorf("KeylessNeedsIssuer(%q, %v) = %v, want %v", tc.explicitIssuer, tc.keylessCapable, got, tc.want)
			}
		})
	}
}
