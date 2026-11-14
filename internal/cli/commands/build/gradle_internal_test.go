// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/testutil/ghaenv"
	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
)

// Private dependencies put the read credential in every Gradle invocation's
// environment, so each one runs without a reusable daemon. Without the
// opt-in the credential is withheld and the project's daemon choice stands.
func TestGradleRunPrivateDependenciesUseNoReusableDaemon(t *testing.T) {
	for _, private := range []bool{false, true} {
		t.Run(map[bool]string{false: "public", true: "private"}[private], func(t *testing.T) {
			ghaenv.Setup(t)

			fsys := testfs.NewReal(t)
			fsys.Chdir()
			fsys.WriteFile("gradle.properties", []byte("version=1.2.3\n"))
			fsys.WriteFile("gradlew", []byte(`#!/bin/sh
set -eu
printf '%s|%s\n' "${DEPENDENCY_TOKEN-}" "$*" >> invocations
`))

			t.Setenv("PRIVATE_DEPENDENCIES", "false")
			t.Setenv("DEPENDENCY_TOKEN", "read-only-token")

			args := []string{"gradle", "run", "--tasks", "assemble", "--sbom-tool-version", "3.2.1"}
			if private {
				args = append(args, "--private-dependencies")
			}

			require.NoError(t, gradleCmd().Run(t.Context(), args))

			body, err := os.ReadFile(fsys.Path("invocations"))
			require.NoError(t, err)

			invocations := strings.Split(strings.TrimSpace(string(body)), "\n")
			require.Len(t, invocations, 3, "tests, packaging and Build SBOM")

			for _, invocation := range invocations {
				token, argv, ok := strings.Cut(invocation, "|")
				require.True(t, ok)

				if private {
					require.Equal(t, "read-only-token", token)
					require.True(t, strings.HasSuffix(argv, " --no-daemon"), argv)
					require.Equal(t, 1, strings.Count(argv, "--no-daemon"), argv)
				} else {
					require.Empty(t, token)
					require.NotContains(t, argv, "--no-daemon")
				}

				require.NotContains(t, argv, "read-only-token")
			}
		})
	}
}
