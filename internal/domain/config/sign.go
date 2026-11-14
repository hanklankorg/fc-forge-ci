// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package config

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
)

// SignConfig is the top-level `sign:` block in artifacts.yml. It
// selects which signing backend `release sign` uses. All fields are
// optional; the absence of the block (or an empty Method) defaults to
// gpg, which preserves the pre-cosign contract for existing repos.
//
// Validation invariants (see Validate):
//
//   - Method is one of [gpg, sigstore, kms] or empty (→ gpg default).
//   - FilesMethod is empty or gpg, and gpg only beside method=sigstore or
//     method=kms: release files are then signed with OpenPGP while Method
//     keeps signing the container images. Every other combination is
//     refused rather than guessed at.
//   - method=kms requires Key; method=sigstore + method=gpg forbid it.
//   - method=sigstore optionally accepts OIDCIssuer; method=gpg + kms forbid it.
//   - Transparency is one of [public, none] or empty (→ public default).
//     method=gpg forbids it (OpenPGP has no transparency log, and the
//     cosign adapter is never invoked). method=sigstore forbids `none`
//     — see release.TransparencyNone for why keyless cannot verify
//     without a log here. method=kms accepts either.
//   - When Method is kms, Key's URI scheme is restricted to a known
//     allowlist (see allowedKMSSchemes). This is a security-hardening
//     measure: the config file is committed to the repo, so a malicious
//     PR could in principle change `sign.key` to an attacker-controlled
//     URI or local path. The allowlist forces the scheme to be one of
//     the recognised KMS / HSM / Vault providers — local key-file
//     paths are accepted only when prefixed with `file:` to make the
//     intent explicit. The one other accepted key is exactly
//     CosignKeyEnvRef, a Cosign key the workflow hands the signing
//     step as a secret.
type SignConfig struct {
	Method domainrelease.SignMethod `yaml:"method,omitempty"`
	// FilesMethod signs the release files with another method than Method,
	// which then covers the container images alone. Empty means Method
	// signs both, as it always has. The one accepted value is gpg.
	FilesMethod domainrelease.SignMethod `yaml:"files-method,omitempty"`
	Key         string                   `yaml:"key,omitempty"`
	OIDCIssuer  string                   `yaml:"oidc-issuer,omitempty"`
	// Transparency selects whether cosign publishes each signature to
	// the public Sigstore Rekor log. Empty means public: opting out of
	// the public record is a decision that should be written down, not
	// arrived at by leaving a field blank.
	Transparency domainrelease.Transparency `yaml:"transparency,omitempty"`
}

// allowedKMSSchemes restricts which URI shapes can appear in sign.key.
// Adding a scheme here is a security-relevant decision — it allows a
// future PR to point sign.key at that target. The current list mirrors
// cosign's supported provider URIs; `file:` is explicit and intended
// only for local-development / integration-test usage.
var allowedKMSSchemes = []string{
	"awskms",
	"gcpkms",
	"azurekms",
	"hashivault",
	"pkcs11", // TPM / HSM via PKCS#11 URI
	"file",   // local key file (intentionally explicit prefix)
}

// CosignKeyEnvRef is the one environment key reference sign.key may
// name: the Cosign private key the layer's signing steps receive as the
// COSIGN_KEY secret, with COSIGN_PASSWORD beside it, as on GitLab and
// Forgejo. Only this exact value is accepted. The env scheme as a whole
// is not: cosign forwards the variable a reference names into its isolated
// environment, so `env://RELEASE_TOKEN` would hand it a token.
const CosignKeyEnvRef = "env://COSIGN_KEY"

// KMSKeySchemes returns the reviewed scheme vocabulary without exposing its storage.
func KMSKeySchemes() []string { return slices.Clone(allowedKMSSchemes) }

// EffectiveMethod returns the configured method or the package default
// when none is set. Callers that need to know whether the operator made
// an explicit choice should inspect SignConfig.Method directly.
func (s SignConfig) EffectiveMethod() domainrelease.SignMethod {
	if s.Method == "" {
		return domainrelease.DefaultSignMethod
	}

	return s.Method
}

// EffectiveFilesMethod returns the method that signs the release files:
// FilesMethod when the declaration sets it, the method for everything
// otherwise.
func (s SignConfig) EffectiveFilesMethod() domainrelease.SignMethod {
	if s.FilesMethod != "" {
		return s.FilesMethod
	}

	return s.EffectiveMethod()
}

// DeclaresGPG reports whether the declaration names gpg for its release
// files, as sign.method or as sign.files-method. A declaration that names no
// method does not, though gpg is its effective method: the Forgejo and GitLab
// releases sign their release files with GPG only when told to, and with
// their Cosign key otherwise.
func (s SignConfig) DeclaresGPG() bool {
	return s.Method == domainrelease.SignMethodGPG || s.FilesMethod == domainrelease.SignMethodGPG
}

// EffectiveTransparency returns the configured transparency or the
// package default when none is set. Meaningful only for the cosign
// methods; Validate rejects the field outright for gpg.
func (s SignConfig) EffectiveTransparency() domainrelease.Transparency {
	if s.Transparency == "" {
		return domainrelease.DefaultTransparency
	}

	return s.Transparency
}

// Validate enforces the per-method invariants documented on SignConfig.
// Returns nil when the config is well-formed (including the all-empty
// case, which defaults to gpg). Errors wrap errs.ErrInvalidConfig so the
// CLI exits with sysexits EX_CONFIG (78).
//
//nolint:cyclop // straight-line invariant check — fewer branches would obscure which field failed.
func (s SignConfig) Validate() error {
	method := s.EffectiveMethod()

	if _, err := domainrelease.ParseSignMethod(string(method)); err != nil {
		return err
	}

	if s.Transparency != "" {
		if _, err := domainrelease.ParseTransparency(string(s.Transparency)); err != nil {
			return err
		}
	}

	if err := s.validateFilesMethod(method); err != nil {
		return err
	}

	switch method {
	case domainrelease.SignMethodGPG:
		if s.Key != "" {
			return fmt.Errorf("sign.key is forbidden for method=gpg: %w", errs.ErrInvalidConfig)
		}

		if s.OIDCIssuer != "" {
			return fmt.Errorf("sign.oidc-issuer is forbidden for method=gpg: %w", errs.ErrInvalidConfig)
		}

		if s.Transparency != "" {
			return fmt.Errorf(
				"sign.transparency is forbidden for method=gpg (got %q): OpenPGP has no transparency log: %w",
				s.Transparency, errs.ErrInvalidConfig)
		}
	case domainrelease.SignMethodSigstore:
		if s.Key != "" {
			return fmt.Errorf("sign.key is forbidden for method=sigstore (keyless has no key): %w", errs.ErrInvalidConfig)
		}

		if s.OIDCIssuer != "" {
			if err := validateOIDCIssuer(s.OIDCIssuer); err != nil {
				return err
			}
		}

		if s.Transparency == domainrelease.TransparencyNone {
			return fmt.Errorf(
				"sign.transparency=none is forbidden for method=sigstore: a keyless signature is made with a "+
					"short-lived Fulcio certificate, so a verifier needs a Rekor inclusion proof to know the "+
					"certificate was valid when it signed; without one the signature is unverifiable. "+
					"Use method=kms to sign without a transparency log: %w",
				errs.ErrInvalidConfig)
		}
	case domainrelease.SignMethodKMS:
		if s.Key == "" {
			return fmt.Errorf("sign.key is required for method=kms (e.g. hashivault://transit/keys/release): %w", errs.ErrInvalidConfig)
		}

		if s.OIDCIssuer != "" {
			return fmt.Errorf("sign.oidc-issuer is forbidden for method=kms: %w", errs.ErrInvalidConfig)
		}

		if err := validateKMSKey(s.Key); err != nil {
			return err
		}
	}

	return nil
}

// validateFilesMethod admits the one split a declaration can state: gpg for
// the release files beside a cosign method for the images. A second cosign
// method would need a second key, issuer and transparency choice, and gpg
// beside gpg says nothing, so both are refused.
func (s SignConfig) validateFilesMethod(method domainrelease.SignMethod) error {
	if s.FilesMethod == "" {
		return nil
	}

	if s.FilesMethod != domainrelease.SignMethodGPG {
		return fmt.Errorf(
			"sign.files-method accepts only gpg (got %q); omit it to sign release files with sign.method: %w",
			s.FilesMethod, errs.ErrInvalidConfig)
	}

	if method == domainrelease.SignMethodGPG {
		return fmt.Errorf(
			"sign.files-method requires sign.method sigstore or kms, which then signs the container images; "+
				"sign.method=gpg already signs release files with gpg: %w",
			errs.ErrInvalidConfig)
	}

	return nil
}

// validateKMSKey enforces the allowlist of sign.key URI schemes. The
// goal is twofold:
//
//  1. Reject bare local paths (e.g. `/etc/...` or `../somefile`), so a
//     malicious PR cannot quietly point the signing job at a file. A
//     local key must be prefixed `file:` to make the intent explicit;
//     that form is an opt-in and its path is not confined.
//  2. Limit the URI to recognised KMS / HSM provider schemes so that a
//     PR can't quietly redirect signing to an attacker-controlled host
//     via, say, a `http://attacker.example/key` URI that cosign might
//     resolve via plugins.
func validateKMSKey(key string) error {
	if key == CosignKeyEnvRef {
		return nil
	}

	colon := strings.Index(key, ":")
	if colon <= 0 {
		return fmt.Errorf(
			"sign.key does not start with a recognised scheme; expected one of %s (e.g. hashivault://transit/keys/<name>, awskms:///alias/<name>, file:<path>), or exactly "+CosignKeyEnvRef+": %w",
			strings.Join(allowedKMSSchemes, ", "), errs.ErrInvalidConfig,
		)
	}

	scheme := key[:colon]
	if slices.Contains(allowedKMSSchemes, scheme) {
		return nil
	}

	return fmt.Errorf(
		"sign.key scheme is not in the allowlist %s, and the key is not exactly "+CosignKeyEnvRef+" — to extend the allowlist, edit internal/domain/config/sign.go (security-relevant change): %w",
		strings.Join(allowedKMSSchemes, ", "), errs.ErrInvalidConfig,
	)
}

// validateOIDCIssuer enforces that sign.oidc-issuer is a well-formed
// HTTPS URL. We don't allowlist hostnames — operators may run their
// own Fulcio (Sigstore self-hosted) or use a non-standard issuer for
// Forgejo. But we do reject HTTP and malformed URLs, since either
// would degrade the trust model.
// The authority must have a hostname, no userinfo, and an optional port
// in 1..65535. net/url validates HTTPS IP-literal and colon syntax.
func validateOIDCIssuer(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("sign.oidc-issuer is not a valid URL: %w", errs.ErrInvalidConfig)
	}

	if parsed.Scheme != "https" {
		return fmt.Errorf(
			"sign.oidc-issuer must use https:// — OIDC tokens must travel over TLS: %w",
			errs.ErrInvalidConfig,
		)
	}

	if parsed.Hostname() == "" {
		return fmt.Errorf("sign.oidc-issuer has no host component: %w", errs.ErrInvalidConfig)
	}

	if parsed.User != nil {
		return fmt.Errorf("sign.oidc-issuer must not contain userinfo: %w", errs.ErrInvalidConfig)
	}

	if port := parsed.Port(); port != "" || strings.HasSuffix(parsed.Host, ":") {
		number, portErr := strconv.ParseUint(port, 10, 16)
		if portErr != nil || number == 0 {
			return fmt.Errorf("sign.oidc-issuer port must be in 1..65535: %w", errs.ErrInvalidConfig)
		}
	}

	return nil
}
