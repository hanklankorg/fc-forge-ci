// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/runcontext"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

// A layer passes an artifacts-config input left empty as an empty
// ARTIFACTS_CONFIG, which names no file: the repository's own image is
// released. A file named by the flag or the variable must exist.
func TestReleaseImagesPlan_AnEmptyFileNameNamesNoFile(t *testing.T) {
	for _, tc := range []struct {
		name, variable string
		args           []string
		want           error
	}{
		{name: "unset"},
		{name: "empty variable", variable: ""},
		{name: "empty flag", args: []string{"--file", ""}},
		{name: "named by the variable", variable: "images.yml", want: errs.ErrMissingInput},
		{name: "named by the flag", args: []string{"--file", "images.yml"}, want: errs.ErrMissingInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := testenv.New(t)
			t.Chdir(env.Temp)
			require.NoError(t, os.WriteFile("Containerfile", []byte("FROM scratch\n"), 0o600))

			// t.Setenv restores the variable after an unset too.
			t.Setenv("ARTIFACTS_CONFIG", tc.variable)

			if tc.name == "unset" {
				require.NoError(t, os.Unsetenv("ARTIFACTS_CONFIG"))
			}

			args := append([]string{"plan", "--image-repository", "ghcr.io/acme/app", "--repository", "acme/app", "--registry-username", "acme", "--release-tag", "v1.2.3"}, tc.args...)

			err := releaseImagesPlanCmd().Run(t.Context(), args)
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
				require.ErrorContains(t, err, "images.yml")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestReleaseImagesPlan_ResolvedPolicyAndNamedFileAreExclusive(t *testing.T) {
	t.Setenv("ARTIFACTS_CONFIG", "")
	t.Setenv("IMAGE_PLAN_JSON", "")
	err := releaseImagesPlanCmd().Run(t.Context(), []string{
		"plan", "--release-tag", "v1.2.3", "--file", "images.yml", "--image-plan-json", "{}",
	})
	require.ErrorIs(t, err, errs.ErrUsage)
	require.ErrorContains(t, err, "mutually exclusive")
}

// An empty --image-repository is the forge's own registry for this
// repository, as the signer defaults it; without a forge to name it, the
// plan refuses.
func TestReleaseImagesPlan_ImageRepositoryDefaultsToTheForgesRegistry(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "named", args: []string{"--image-repository", "ghcr.io/acme/app", "--server-url", "https://forge.example"}, want: "ghcr.io/acme/app"},
		{name: "forge registry", args: []string{"--image-repository", "", "--server-url", "https://forge.example:8443/", "--repository", "Acme/App"}, want: "forge.example:8443/acme/app"},
		{name: "no forge", args: []string{"--repository", "acme/app"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, variable := range append(runcontext.ServerURL().Keys(), "IMAGE_REPOSITORY") {
				t.Setenv(variable, "")
			}

			var got string

			cmd := releaseImagesPlanCmd()
			cmd.Action = func(_ context.Context, c *cli.Command) error {
				repository, err := releasePlanImageRepository(c)
				got = repository

				return err
			}

			err := cmd.Run(t.Context(), append([]string{"plan", "--release-tag", "v1.2.3"}, tc.args...))
			if tc.want == "" {
				require.ErrorIs(t, err, errs.ErrUsage)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// The plan decides the user the publishing job logs in as, by the signer's
// rule: the repository owner on the forge's own registry, the named user on
// another one, where an unnamed user stops the release before it builds.
func TestReleaseImagesPlan_RegistryUserDefaultsOnTheForgeOnly(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string // empty: refused
	}{
		{name: "forge registry", args: []string{"--server-url", "https://forge.example:8443"}, want: "Acme"},
		{name: "forge registry, named user", args: []string{"--server-url", "https://forge.example", "--registry-username", "bot"}, want: "bot"},
		{name: "GHCR, named user", args: []string{"--server-url", "https://forge.example", "--image-repository", "ghcr.io/acme/app", "--registry-username", "acme-bot"}, want: "acme-bot"},
		{name: "GHCR, no user", args: []string{"--server-url", "https://forge.example", "--image-repository", "ghcr.io/acme/app"}},
		{name: "no forge, no user", args: []string{"--image-repository", "forge.example/acme/app"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, variable := range []string{"IMAGE_REPOSITORY", "GITHUB_SERVER_URL", "CI_SERVER_URL", "FORGEJO_SERVER_URL", "SERVER_URL", "REGISTRY_USER"} {
				t.Setenv(variable, "")
			}
			// A retired spelling must neither override the forge owner nor
			// satisfy an external registry's requirement for an explicit user.
			t.Setenv("REGISTRY_USERNAME", "retired-user")

			var got string

			cmd := releaseImagesPlanCmd()
			cmd.Action = func(_ context.Context, command *cli.Command) error {
				repository, err := releasePlanImageRepository(command)
				if err != nil {
					return err
				}

				got, err = releasePlanRegistryUser(command, repository)

				return err
			}

			err := cmd.Run(t.Context(), append([]string{"plan", "--release-tag", "v1.2.3", "--repository", "Acme/App"}, tc.args...))
			if tc.want == "" {
				require.ErrorIs(t, err, errs.ErrUsage)
				require.ErrorContains(t, err, "registry-user")

				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
