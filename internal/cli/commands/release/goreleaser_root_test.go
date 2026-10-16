// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	rootcli "github.com/diggsweden/forge-ci/internal/cli"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// Execute the signing arguments shipped to GoReleaser through the real CLI and
// cosign adapter. The fixture signer has no network or real credentials. Nearby
// files catch accidental broadening from one checksum manifest to the defaults.
func TestGoReleaserChecksumSigning(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(reporoot.Path(t), ".goreleaser.yml"))
	require.NoError(t, err)

	var config struct {
		Signs []struct {
			Cmd       string   `yaml:"cmd"`
			Signature string   `yaml:"signature"`
			Artifacts string   `yaml:"artifacts"`
			Args      []string `yaml:"args"`
		} `yaml:"signs"`
	}
	require.NoError(t, yaml.Unmarshal(body, &config))
	require.Len(t, config.Signs, 1)
	sign := config.Signs[0]
	require.Equal(t, "forge-ci", sign.Cmd)
	require.Equal(t, "checksum", sign.Artifacts)

	const artifact = "dist/checksums.txt"

	signature := strings.ReplaceAll(sign.Signature, "${artifact}", artifact)

	for _, tc := range []struct {
		name, firstFailure string
		attempts           int
		missing, wantError bool
	}{
		{name: "success", attempts: 1},
		{name: "transient failure", firstFailure: "context deadline exceeded", attempts: 2},
		{name: "permanent failure", firstFailure: "permission denied", attempts: 1, wantError: true},
		{name: "missing checksum manifest", missing: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := provenanceRootEnv(t, "github")
			env.MkdirAll("dist")
			env.MkdirAll("release-artifacts")

			for _, name := range []string{artifact, "checksums.sha256", "release-artifacts/other.tgz", "dist/app.tgz"} {
				if name == artifact && tc.missing {
					continue
				}

				require.NoError(t, os.WriteFile(env.Path(name), []byte("unchanged\n"), 0o600))
			}

			signer := `#!/bin/sh
set -eu
[ "$#" -eq 5 ] && [ "$1" = sign-blob ] && [ "$2" = --yes ] &&
  [ "$3" = --bundle ] && [ "$4" = dist/checksums.txt.bundle ] &&
  [ "$5" = dist/checksums.txt ] || exit 64
if [ ! -f sign-attempts ]; then
  printf 'attempt\n' >>sign-attempts
  if [ -n 'FIRST_FAILURE' ]; then
    printf '%s\n' 'FIRST_FAILURE' >&2
    exit 1
  fi
else
  printf 'attempt\n' >>sign-attempts
fi
printf 'fixture bundle\n' >"$4"
`
			signer = strings.ReplaceAll(signer, "FIRST_FAILURE", tc.firstFailure)
			require.NoError(t, os.WriteFile(env.Path("empty-path", "cosign"), []byte(signer), 0o700)) //nolint:gosec // executable fixture in the isolated test directory.

			args := make([]string, 1, 1+len(sign.Args))
			args[0] = sign.Cmd

			expand := strings.NewReplacer("${artifact}", artifact, "${signature}", signature)
			for _, arg := range sign.Args {
				args = append(args, expand.Replace(arg))
			}

			command := rootcli.New(rootcli.BuildInfo{Version: "dev"})
			command.Writer, command.ErrWriter = os.Stdout, os.Stderr

			runErr := command.Run(t.Context(), args)
			if tc.wantError {
				require.Error(t, runErr)
				require.NoFileExists(t, env.Path(signature))
			} else {
				require.NoError(t, runErr)

				bundle, readErr := os.ReadFile(env.Path(signature))
				require.NoError(t, readErr)
				require.Equal(t, "fixture bundle\n", string(bundle))
			}

			if tc.attempts == 0 {
				require.NoFileExists(t, env.Path("sign-attempts"))
			} else {
				attempts, readErr := os.ReadFile(env.Path("sign-attempts"))
				require.NoError(t, readErr)
				require.Equal(t, strings.Repeat("attempt\n", tc.attempts), string(attempts))
			}

			for _, name := range []string{"checksums.sha256", "release-artifacts/other.tgz", "dist/app.tgz"} {
				require.NoFileExists(t, env.Path(name+".bundle"))
				original, readErr := os.ReadFile(env.Path(name))
				require.NoError(t, readErr)
				require.Equal(t, "unchanged\n", string(original))
			}
		})
	}
}
