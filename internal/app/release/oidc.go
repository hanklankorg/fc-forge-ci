// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

// KeylessNeedsIssuer reports whether a sigstore (keyless) signing request
// will be left without an OIDC issuer: true when the operator supplied no
// explicit --oidc-issuer AND the active forge does not publish one
// (its PublicFulcioTrusted capability is false, e.g. Forgejo today). The CLI uses
// this to warn up front — keyless signing on such a forge otherwise fails
// later inside cosign with an opaque "no issuer" error. Pure predicate so
// the decision is testable without wiring cosign.
func KeylessNeedsIssuer(explicitIssuer string, keylessCapable bool) bool {
	return explicitIssuer == "" && !keylessCapable
}
