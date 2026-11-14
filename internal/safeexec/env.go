// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package safeexec

import (
	"slices"
	"strings"
)

// EnvKeep returns the entries of env whose variable name keep accepts. An
// entry without "=" names no variable and is dropped.
func EnvKeep(env []string, keep func(name string) bool) []string {
	out := make([]string, 0, len(env))

	for _, entry := range env {
		if name, _, ok := strings.Cut(entry, "="); ok && keep(name) {
			out = append(out, entry)
		}
	}

	return out
}

// EnvWithout returns env without the named variables.
func EnvWithout(env []string, names ...string) []string {
	return EnvKeep(env, func(name string) bool { return !slices.Contains(names, name) })
}

// gateToolEnv is the whole environment a scanner inside a gate is given.
// Trivy and Syft take most of their settings from TRIVY_* and SYFT_*
// variables, and a forge injects project and group variables into every job,
// so an inherited environment could skip directories, swap a database or add
// exclusions behind the engine's own flags.
//
// Kept: tool lookup, home and temporary storage, the cache (XDG_CACHE_HOME),
// registry credentials the job logged in with (DOCKER_CONFIG,
// REGISTRY_AUTH_FILE and XDG_RUNTIME_DIR for the containers auth file), and
// the network route and trust a proxied or private-CA runner needs. No token
// is kept: trivy would send GITHUB_TOKEN to ghcr.io, and on a Forgejo job it
// holds the forge token.
var gateToolEnv = map[string]bool{
	"PATH":               true,
	"HOME":               true,
	"TMPDIR":             true,
	"XDG_CACHE_HOME":     true,
	"XDG_RUNTIME_DIR":    true,
	"DOCKER_CONFIG":      true,
	"REGISTRY_AUTH_FILE": true,
	"SSL_CERT_FILE":      true,
	"SSL_CERT_DIR":       true,
	"HTTP_PROXY":         true,
	"HTTPS_PROXY":        true,
	"NO_PROXY":           true,
	"http_proxy":         true,
	"https_proxy":        true,
	"no_proxy":           true,
}

// GateToolEnv returns the entries of env a scanner inside a gate runs with.
func GateToolEnv(env []string) []string {
	return EnvKeep(env, func(name string) bool { return gateToolEnv[name] })
}

// ImageBuilderEnv keeps runtime, registry and host container-storage settings,
// not unrelated credentials or tool-specific trust/destination overrides.
// SOURCE_DATE_EPOCH remains available for unpinned builds; a request with an
// explicit timestamp removes it before invoking Buildah.
func ImageBuilderEnv(env []string) []string {
	return EnvKeep(env, func(name string) bool {
		if gateToolEnv[name] {
			return true
		}

		switch name {
		case "XDG_CONFIG_HOME", "SOURCE_DATE_EPOCH", "BUILDAH_ISOLATION",
			"CONTAINERS_CONF", "CONTAINERS_REGISTRIES_CONF", "CONTAINERS_STORAGE_CONF",
			"CONTAINERS_POLICY", "STORAGE_DRIVER", "STORAGE_OPTS",
			"_CONTAINERS_USERNS_CONFIGURED", "_CONTAINERS_ROOTLESS_UID":
			return true
		default:
			return false
		}
	})
}
