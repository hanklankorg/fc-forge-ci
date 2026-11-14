// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package cosign

import (
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/keymaterial"
	"github.com/diggsweden/forge-ci/internal/safeexec"
)

// registryEnv gives an explicit auth file precedence over the default keychain.
// REGISTRY_AUTH_FILE alone cannot do that: cosign consults Docker config first.
// Keep the override local to the child, preserving an already-isolated Env.
func (a *Adapter) registryEnv() ([]string, func(), error) {
	noop := func() {}
	if a.AuthFile == "" {
		return a.Env, noop, nil
	}

	body, err := os.ReadFile(a.AuthFile)
	if err != nil {
		return nil, noop, fmt.Errorf("read registry auth file %s: %w", a.AuthFile, err)
	}
	defer clear(body)

	dir, err := os.MkdirTemp("", "forge-ci-cosign-auth-*")
	if err != nil {
		return nil, noop, fmt.Errorf("create private cosign registry config: %w", err)
	}

	cleanup := func() { _ = os.RemoveAll(dir) }
	if err := os.WriteFile(filepath.Join(dir, "config.json"), body, 0o600); err != nil { //nolint:gosec // G703: fixed basename inside the freshly created private directory.
		cleanup()

		return nil, noop, fmt.Errorf("write private cosign registry config: %w", err)
	}

	environment := a.Env
	if environment == nil {
		environment = os.Environ()
	}

	environment = safeexec.EnvWithout(environment, "DOCKER_CONFIG", "REGISTRY_AUTH_FILE")

	return append(environment, "DOCKER_CONFIG="+dir), cleanup, nil
}

// runtimeKeep supplies tool lookup, homes, temporary storage and network settings
// to an isolated cosign subprocess. Everything else must be explicitly allowed.
//
// TLS roots and proxies are kept together: ForKeyRef selects isolation for
// env:// keys, which still need the configured routes and trust for network
// operations. Keeping TLS settings while dropping proxies would break signing
// on a proxy-only network. Both proxy-name cases are supported.
//
// Proxy URLs may contain credentials; allowing them is part of this policy.
// transparency=none suppresses public Sigstore service use, not registry or KMS
// traffic (see withSigningConfig), so it does not make proxy settings redundant.
var runtimeKeep = map[string]bool{
	"PATH":            true,
	"HOME":            true,
	"TMPDIR":          true,
	"SSL_CERT_FILE":   true,
	"SSL_CERT_DIR":    true,
	"XDG_CONFIG_HOME": true,
	"XDG_CACHE_HOME":  true,
	"HTTP_PROXY":      true,
	"HTTPS_PROXY":     true,
	"NO_PROXY":        true,
	"http_proxy":      true,
	"https_proxy":     true,
	"no_proxy":        true,
}

// IsolatedEnv returns runtimeKeep and explicitly allowed variables from the
// current environment, excluding unrelated credential variables. The returned
// slice is suitable for exec.Cmd.Env.
func IsolatedEnv(allow ...string) []string {
	allowSet := make(map[string]bool, len(allow))
	for _, k := range allow {
		allowSet[k] = true
	}

	return filterEnv(os.Environ(), allowSet)
}

// filterEnv keeps each "KEY=VALUE" entry whose key is in runtimeKeep or
// allow, dropping everything else. Pure (operates on the passed slice)
// so the policy is testable without mutating the process environment.
func filterEnv(environ []string, allow map[string]bool) []string {
	return safeexec.EnvKeep(environ, func(name string) bool { return runtimeKeep[name] || allow[name] })
}

// verificationEnv prevents inherited Sigstore trust roots and alternate signature
// repositories from changing what a verification request accepts. Registry auth,
// runner TLS/proxy settings and an explicitly named env:// key remain available.
// Signing uses its own credential policy, since KMS and keyless signing need
// provider credentials that verification of a public key does not.
func verificationEnv(environment []string, args []string) []string {
	if environment == nil {
		environment = os.Environ()
	}

	allow := map[string]bool{
		"DOCKER_CONFIG": true, "REGISTRY_AUTH_FILE": true, "XDG_RUNTIME_DIR": true,
	}
	keyRef := ""

	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--key" {
			keyRef = args[i+1]
			if name := KeyEnvName(keyRef); name != "" {
				allow[name] = true
			}
		}
	}
	// Remote public-key lookup needs the selected provider's authentication,
	// not every provider's credentials. Keep the SDK namespace only for that
	// explicitly selected KMS; Sigstore trust/destination overrides are never
	// SDK authentication settings.
	for _, entry := range environment {
		name, _, ok := strings.Cut(entry, "=")
		if ok && verificationProviderEnv(keyRef, name) {
			allow[name] = true
		}
	}

	return filterEnv(environment, allow)
}

func verificationProviderEnv(keyRef, name string) bool {
	scheme, _, _ := strings.Cut(keyRef, ":")
	switch scheme {
	case "awskms":
		return strings.HasPrefix(name, "AWS_")
	case "gcpkms":
		return strings.HasPrefix(name, "GOOGLE_") || strings.HasPrefix(name, "GCE_") || name == "CLOUDSDK_AUTH_ACCESS_TOKEN"
	case "azurekms":
		return strings.HasPrefix(name, "AZURE_")
	case "hashivault":
		return strings.HasPrefix(name, "VAULT_")
	case "pkcs11":
		return strings.HasPrefix(name, "COSIGN_PKCS11_")
	default:
		return false
	}
}

// ForKeyRef returns an Adapter isolated for the signing key it is given.
//
// An env:// key names the one variable cosign needs, so the subprocess
// can be reduced to the runtime set plus that variable and
// COSIGN_PASSWORD. KMS, file and keyless refs each need credential
// material this package cannot enumerate -- AWS_*, VAULT_*, the runner's
// OIDC request variables -- and isolating them on a guess does not fail
// at review time, it fails during a release. Those get the ambient
// environment, deliberately.
//
// Callers add the variables their own path needs (for example
// DOCKER_CONFIG for registry auth) through extra; they are allowed only
// on the isolated path, since the ambient one already has them.
//
// This is the rule three signing call sites had each spelled out for
// themselves. Having it in one place is what stops a fourth from
// quietly not having it.
func ForKeyRef(keyRef string, extra ...string) *Adapter {
	variable := KeyEnvName(keyRef)
	if variable == "" {
		return New()
	}

	allow := append([]string{variable, envKeyPassword}, extra...)

	return NewIsolated(allow...)
}

// KeyEnvName returns the variable an env:// key reference names, and the
// empty string for any other reference. The operator chooses that name, so no
// fixed list of credential variables can hold it: a caller that runs another
// tool beside cosign removes it from that tool's environment by this name.
func KeyEnvName(keyRef string) string {
	variable, _ := strings.CutPrefix(keyRef, "env://")
	if variable == keyRef {
		return ""
	}

	return variable
}

// envKeyPassword is the variable cosign reads for a signing key's
// passphrase; always allowed alongside an env:// key.
const envKeyPassword = "COSIGN_PASSWORD"

// decodedKeyEnv unwraps standard base64 only for the explicitly selected
// env:// PEM key. Cosign still owns PEM parsing/decryption and invalid-key
// diagnostics. Other variables, file keys and KMS references are untouched.
func decodedKeyEnv(environment []string, args []string) ([]string, error) {
	name := ""

	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--key" {
			name = KeyEnvName(args[i+1])
		}
	}

	if name == "" {
		return environment, nil
	}

	if environment == nil {
		environment = os.Environ()
	}

	result := slices.Clone(environment)
	for index, entry := range result {
		key, value, found := strings.Cut(entry, "=")
		if !found || key != name {
			continue
		}

		decoded, err := keymaterial.Key(value)
		if err != nil {
			if errors.Is(err, keymaterial.ErrSizeLimit) {
				return nil, fmt.Errorf("cosign environment key %s: %w", name, err)
			}

			continue
		}

		block, _ := pem.Decode(decoded)
		if block != nil {
			result[index] = name + "=" + string(decoded)

			clear(block.Bytes)
		}

		clear(decoded)
	}

	return result, nil
}

// NewIsolated returns an Adapter whose subprocess environment is restricted to
// runtime settings and explicitly allowed variables. See IsolatedEnv.
func NewIsolated(allow ...string) *Adapter {
	return &Adapter{
		Env:          IsolatedEnv(allow...),
		Transparency: TransparencyFromEnv(),
	}
}
