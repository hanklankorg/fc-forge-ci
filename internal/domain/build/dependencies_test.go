// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/build"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// The auth line scopes the token to the registry's host and path and names
// it by environment reference, so the temporary npm configuration never
// holds the credential itself and sets no registry or scope.
func TestNPMDependencyAuthConfig_ScopesAnEnvironmentReference(t *testing.T) {
	t.Parallel()

	for registry, want := range map[string]string{
		"https://npm.pkg.github.com":                         "//npm.pkg.github.com/:_authToken=${DEPENDENCY_TOKEN}\n",
		"https://npm.pkg.github.com/":                        "//npm.pkg.github.com/:_authToken=${DEPENDENCY_TOKEN}\n",
		"https://forge.example:8443/api/packages/owner/npm/": "//forge.example:8443/api/packages/owner/npm/:_authToken=${DEPENDENCY_TOKEN}\n",
		"https://forge.example/api/packages/an owner/npm":    "//forge.example/api/packages/an%20owner/npm/:_authToken=${DEPENDENCY_TOKEN}\n",
		"http://localhost:4873":                              "//localhost:4873/:_authToken=${DEPENDENCY_TOKEN}\n",
		"http://127.0.0.1:4873/private":                      "//127.0.0.1:4873/private/:_authToken=${DEPENDENCY_TOKEN}\n",
		"http://[::1]:4873":                                  "//[::1]:4873/:_authToken=${DEPENDENCY_TOKEN}\n",
	} {
		t.Run(registry, func(t *testing.T) {
			t.Parallel()

			got, err := build.NPMDependencyAuthConfig(registry)
			require.NoError(t, err)
			require.Equal(t, want, got)
		})
	}
}

func TestNPMDependencyAuthConfig_RefusesInsecureOrCredentialedRegistries(t *testing.T) {
	t.Parallel()

	for name, registry := range map[string]string{
		"empty":                   "",
		"no host":                 "/api/packages/npm",
		"http off loopback":       "http://registry.example",
		"http loopback lookalike": "http://localhost.example",
		"http private address":    "http://10.0.0.1:4873",
		"other scheme":            "ftp://registry.example",
		"scheme missing":          "//registry.example",
		"userinfo":                "https://reader:synthetic-private-token@registry.example",
		"query":                   "https://registry.example/?token=synthetic-private-token",
		"fragment":                "https://registry.example/#synthetic-private-token",
		"line break in path":      "https://registry.example/npm\n//other.example/:_authToken=x",
		"encoded line break":      "https://registry.example/npm%0A//other.example/",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := build.NPMDependencyAuthConfig(registry)
			require.ErrorIs(t, err, errs.ErrInvalidConfig)
			require.Empty(t, got)
			require.NotContains(t, err.Error(), "synthetic-private-token")
		})
	}
}
