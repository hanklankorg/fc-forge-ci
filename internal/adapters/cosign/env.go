// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package cosign

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

	body, err := os.ReadFile(a.AuthFile) //nolint:gosec // operator-selected registry auth file.
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
//
//nolint:gochecknoglobals // read-only allow-set; a const map is not expressible in Go.
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

// NewIsolated returns an Adapter whose subprocess environment is restricted to
// runtime settings and explicitly allowed variables. See IsolatedEnv.
func NewIsolated(allow ...string) *Adapter {
	return &Adapter{
		Env:          IsolatedEnv(allow...),
		Transparency: TransparencyFromEnv(),
	}
}
