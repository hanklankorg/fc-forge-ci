// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package deps

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/adapters/forgejo"
	"github.com/diggsweden/forge-ci/internal/adapters/github"
	"github.com/diggsweden/forge-ci/internal/adapters/gitlab"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/runcontext"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

const (
	givenToken      = "given-token"
	givenServer     = "https://given.example"
	givenRepository = "given/repo"
)

// chainNames is every variable the adapter can read the value from.
func chainNames(k providerEnvKey) []string {
	if slices.Contains(k.chain, k.name) {
		return k.chain
	}

	return append([]string{k.name}, k.chain...)
}

// adapterEnv returns the environment the adapter built by ProviderWith reads.
func adapterEnv(t *testing.T, cfg ProviderConfig) (provider.Provider, func(string) string) {
	t.Helper()

	built, err := ProviderWith(cfg)
	require.NoError(t, err)

	switch adapter := built.(type) {
	case *forgejo.Provider:
		return built, adapter.Env
	case *gitlab.Provider:
		return built, adapter.Env
	case *github.Provider:
		return built, adapter.Env
	default:
		require.Failf(t, "no Env seam", "%T", built)

		return nil, nil
	}
}

// A value in ProviderConfig is the one the adapter uses, with every variable
// the adapter could read it from set to another value in the job environment.
// An empty field leaves all of them to the environment.
func TestProviderWith_AnExplicitValueWinsOverEveryVariableOfItsChain(t *testing.T) {
	for _, forge := range []provider.ForgeAPI{provider.ForgeForgejo, provider.ForgeGitLab, provider.ForgeGitHub} {
		t.Run(string(forge), func(t *testing.T) {
			testenv.New(t)
			t.Setenv("FORGE_CI_PROVIDER", string(forge))

			keys := providerEnvKeysFor(forge)
			ambient := map[string]string{}

			for _, key := range []providerEnvKey{keys.token, keys.serverURL, keys.repository} {
				require.NotEmpty(t, key.name)

				for _, name := range chainNames(key) {
					ambient[name] = "ambient-" + name
					t.Setenv(name, ambient[name])
				}
			}

			for _, tc := range []struct {
				name string
				cfg  ProviderConfig
			}{
				{name: "nothing explicit", cfg: ProviderConfig{}},
				{name: "server only", cfg: ProviderConfig{ServerURL: givenServer}},
				{name: "all explicit", cfg: ProviderConfig{Token: givenToken, ServerURL: givenServer, Repository: givenRepository}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					_, env := adapterEnv(t, tc.cfg)

					for _, field := range []struct {
						key   providerEnvKey
						given string
					}{
						{keys.token, tc.cfg.Token},
						{keys.serverURL, tc.cfg.ServerURL},
						{keys.repository, tc.cfg.Repository},
					} {
						for _, name := range chainNames(field.key) {
							switch {
							case field.given == "":
								require.Equal(t, ambient[name], env(name), name)
							case name == field.key.name:
								require.Equal(t, field.given, env(name), name)
							default:
								require.Empty(t, env(name), name)
							}
						}
					}
				})
			}
		})
	}
}

// The Forgejo adapter resolves its token, server and repository through the
// runcontext chains, which read CI_TOKEN, CI_SERVER_URL and REPOSITORY ahead of
// the FORGEJO_* names. The explicit values are what those chains resolve, off a
// Forgejo runner and on one, where the token is bound to the runner's server.
func TestProviderWith_ForgejoChainsResolveTheExplicitValues(t *testing.T) {
	for _, tc := range []struct {
		name   string
		runner map[string]string
	}{
		{name: "off a Forgejo runner"},
		{name: "on a Forgejo runner", runner: map[string]string{"GITHUB_ACTIONS": "true", "FORGEJO_ACTIONS": "true"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testenv.New(t)
			t.Setenv("FORGE_CI_PROVIDER", string(provider.ForgeForgejo))

			for name, value := range tc.runner {
				t.Setenv(name, value)
			}

			for _, name := range runcontext.Token().Keys() {
				t.Setenv(name, "ambient-token")
			}

			for _, name := range runcontext.ServerURL().Keys() {
				t.Setenv(name, "https://ambient.example")
			}

			for _, name := range runcontext.Repository().Keys() {
				t.Setenv(name, "ambient/other")
			}

			built, env := adapterEnv(t, ProviderConfig{Token: givenToken, ServerURL: givenServer, Repository: givenRepository})

			require.Equal(t, givenServer, runcontext.ServerURL().Resolve(env))
			require.Equal(t, givenRepository, runcontext.Repository().Resolve(env))
			require.Equal(t, givenToken, runcontext.Token().Resolve(env).For(givenServer))

			event, err := built.ResolveContext(t.Context())
			require.NoError(t, err)
			require.Equal(t, givenRepository, event.Repo)
			require.Equal(t, givenServer+"/"+givenRepository, event.RepoURL)

			// Only the server is explicit: the job's own token is still read,
			// and the server is the given one.
			_, env = adapterEnv(t, ProviderConfig{ServerURL: givenServer})

			require.Equal(t, givenServer, runcontext.ServerURL().Resolve(env))
			require.Equal(t, "ambient/other", runcontext.Repository().Resolve(env))
			require.True(t, runcontext.Token().Resolve(env).Present())
		})
	}
}

// A forge's own token variables are ones its adapter reads, and no two forges
// share one: a verb that falls back to them cannot hand one forge's token to
// another forge's server.
func TestProviderTokenEnv_NamesTheDetectedForgeOnly(t *testing.T) {
	owner := map[string]provider.ForgeAPI{}

	for _, forge := range []provider.ForgeAPI{provider.ForgeForgejo, provider.ForgeGitLab, provider.ForgeGitHub, provider.ForgeLocal} {
		testenv.New(t)
		t.Setenv("FORGE_CI_PROVIDER", string(forge))

		keys := providerEnvKeysFor(forge)
		require.Equal(t, keys.ownTokens, ProviderTokenEnv(), forge)
		require.Equal(t, forge == provider.ForgeLocal, len(keys.ownTokens) == 0, forge)

		for _, name := range keys.ownTokens {
			require.Contains(t, chainNames(keys.token), name, forge)
			require.NotContains(t, owner, name, forge)

			owner[name] = forge
		}
	}
}
