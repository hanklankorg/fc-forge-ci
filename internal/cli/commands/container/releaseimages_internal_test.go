// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/runcontext"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

func TestReleaseImagesGroupExposesBoundaryCommands(t *testing.T) {
	t.Parallel()

	commands := releaseImagesGroup().Commands

	found := make([]string, 0, len(commands))
	for _, cmd := range commands {
		found = append(found, cmd.Name)
		require.Empty(t, cmd.Aliases)
	}

	require.ElementsMatch(t, []string{"plan", "import", "sign", "validate", "promote", "rollback", "cleanup"}, found)
}

// Only the verbs that act on every ledger entry can be told that a release
// has no images. Rollback follows its journal and validate is a check.
func TestReleaseImagesAllowMissingLedger_IsOfferedBySignPromoteAndCleanupOnly(t *testing.T) {
	t.Parallel()

	offered := map[string]bool{}

	for _, cmd := range releaseImagesGroup().Commands {
		for _, flag := range cmd.Flags {
			if flag.Names()[0] != flagAllowMissingLedger {
				continue
			}

			offered[cmd.Name] = true

			boolFlag, ok := flag.(*cli.BoolFlag)
			require.True(t, ok, cmd.Name)
			require.False(t, boolFlag.Value, cmd.Name)
		}
	}

	require.Equal(t, map[string]bool{subCmdSign: true, subCmdPromote: true, subCmdCleanup: true}, offered)
}

// A release without a ledger ends the command before it reads a credential or
// prepares a registry login: no username is set, the password file does not
// exist, and the runner temp directory gains no Docker config. The flag admits
// nothing else: a ledger that exists is held to every rule, a named ledger and
// the dist directory have to exist, and an empty ledger flag or variable names
// nothing.
func TestReleaseImagesAllowMissingLedger_SkipsOnlyAnAbsentDefaultLedger(t *testing.T) {
	const (
		absent   = "absent"
		noDist   = "no dist directory"
		dangling = "dangling symlink"
	)

	allowed := []string{"--allow-missing-ledger"}
	explicitAllowed := []string{"--ledger", "dist/release-images.json", "--allow-missing-ledger"}

	for _, verb := range []string{subCmdSign, subCmdPromote, subCmdCleanup} {
		for _, tc := range []struct {
			name, ledger string // ledger is a file body unless it is one of the constants
			args         []string
			want         error // nil: the command reports no ledger and succeeds
			wantMessage  string
			// cleanupLogsIn marks a ledger cleanup admits with no entries: it goes
			// on to the registry login, as the repository owner on the forge's
			// registry, and stops at the missing password file.
			cleanupLogsIn bool
		}{
			{name: "default absent", ledger: absent, want: os.ErrNotExist},
			{name: "default absent, allowed", ledger: absent, args: allowed},
			{name: "default absent, empty ledger flag, allowed", ledger: absent, args: append([]string{"--ledger", ""}, allowed...)},
			{name: "explicit absent, allowed", ledger: absent, args: explicitAllowed, want: os.ErrNotExist},
			{name: "no dist directory, allowed", ledger: noDist, args: allowed, want: os.ErrNotExist, wantMessage: "open dist dir"},
			{name: "dangling symlink, allowed", ledger: dangling, args: allowed, want: errs.ErrValidation, wantMessage: "regular file"},
			{name: "empty array", ledger: "[]", want: errs.ErrValidation, wantMessage: "at least one entry", cleanupLogsIn: true},
			{name: "empty array, allowed", ledger: "[]", args: allowed, want: errs.ErrValidation, wantMessage: "at least one entry", cleanupLogsIn: true},
			{name: "explicit empty array, allowed", ledger: "[]", args: explicitAllowed, want: errs.ErrValidation, wantMessage: "at least one entry", cleanupLogsIn: true},
			{name: "null, allowed", ledger: "null", args: allowed, want: errs.ErrMalformedInput, wantMessage: "parse ledger"},
			{name: "zero bytes", ledger: "", want: errs.ErrMalformedInput, wantMessage: "parse ledger"},
			{name: "zero bytes, allowed", ledger: "", args: allowed, want: errs.ErrMalformedInput, wantMessage: "parse ledger"},
			{name: "whitespace, allowed", ledger: " \n", args: allowed, want: errs.ErrMalformedInput, wantMessage: "parse ledger"},
			{name: "malformed, allowed", ledger: "{", args: allowed, want: errs.ErrMalformedInput},
		} {
			t.Run(verb+"/"+tc.name, func(t *testing.T) {
				env := testenv.New(t)
				t.Chdir(env.Temp)
				runnerTemp := env.MkdirAll("runner-temp")
				t.Setenv("CI_TEMP_DIR", runnerTemp)
				// Set but empty in every case: it names no ledger, so the
				// default path stays the default.
				t.Setenv("RELEASE_IMAGES_PATH", "")

				switch tc.ledger {
				case noDist:
				case absent:
					env.MkdirAll("dist")
				case dangling:
					env.MkdirAll("dist")
					require.NoError(t, os.Symlink("gone.json", "dist/release-images.json"))
				default:
					env.MkdirAll("dist")
					require.NoError(t, os.WriteFile("dist/release-images.json", []byte(tc.ledger), 0o600))
				}

				args := []string{"--registry-password-file", env.Path("no-such-password-file")}
				args = append(args, tc.args...)

				output, runErr := runReleaseImagesVerb(t, env, verb, args...)

				want, wantMessage := tc.want, tc.wantMessage
				if verb == subCmdCleanup && tc.cleanupLogsIn {
					want, wantMessage = os.ErrNotExist, "no-such-password-file"
				}

				if want != nil {
					require.ErrorIs(t, runErr, want)
					require.ErrorContains(t, runErr, wantMessage)
					require.NotContains(t, output, "nothing to")
				} else {
					require.NoError(t, runErr)
					require.Equal(t, "release images: no release-image ledger at dist/release-images.json; nothing to "+verb+".\n", output)
				}

				// The runner temp directory holds no Docker config: a skipped or
				// refused run never creates one, and the run that reached the
				// login removed its own.
				left, err := os.ReadDir(runnerTemp)
				require.NoError(t, err)
				require.Empty(t, left)
				require.NoDirExists(t, "state")
			})
		}
	}
}

// The tag deleter of rollback and cleanup is configured from the command line:
// the token of --provider-token-file goes to the --server-url forge, for every
// forge that deletes tags, while every variable the adapter reads them from
// names another token, server and repository. The process environment is the
// same before, during and after.
func TestReleaseImagesTagDeleter_IsConfiguredFromTheCommandLineOnEveryForge(t *testing.T) {
	const (
		givenToken = "given-token"
		imagePath  = "/owner/app:staging-v1.0.0"
	)

	for _, tc := range []struct {
		forge                                  provider.ForgeAPI
		envTokens, envServers, envRepositories []string
		authHeader, wantAuth                   string
		respond                                http.HandlerFunc
		wantRequests                           []string
	}{
		{
			// The adapter's own chains, which read CI_TOKEN and CI_SERVER_URL
			// ahead of the FORGEJO_* names.
			forge:     provider.ForgeForgejo,
			envTokens: runcontext.Token().Keys(), envServers: runcontext.ServerURL().Keys(), envRepositories: runcontext.Repository().Keys(),
			authHeader: "Authorization", wantAuth: "token " + givenToken,
			respond:      func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) },
			wantRequests: []string{"DELETE /api/v1/packages/owner/container/app/staging-v1.0.0"},
		},
		{
			forge:     provider.ForgeGitLab,
			envTokens: []string{"GITLAB_TOKEN", "CI_JOB_TOKEN"}, envServers: []string{"CI_SERVER_URL"}, envRepositories: []string{"CI_PROJECT_PATH"},
			authHeader: "Private-Token", wantAuth: givenToken,
			respond: func(response http.ResponseWriter, r *http.Request) {
				body := `{}`
				if r.Method == http.MethodGet {
					body = `[{"id":42,"path":"owner/app"}]`
				}

				_, _ = response.Write([]byte(body))
			},
			wantRequests: []string{
				"GET /api/v4/projects/owner%2Fapp/registry/repositories",
				"DELETE /api/v4/projects/owner%2Fapp/registry/repositories/42/tags/staging-v1.0.0",
			},
		},
	} {
		t.Run(string(tc.forge), func(t *testing.T) {
			env := testenv.New(t)
			t.Chdir(env.Temp)

			var (
				mu       sync.Mutex
				requests []string
			)

			given := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				mu.Lock()

				requests = append(requests, request.Method+" "+request.URL.EscapedPath())

				mu.Unlock()

				if got := request.Header.Get(tc.authHeader); got != tc.wantAuth {
					t.Errorf("%s header = %q, want %q", tc.authHeader, got, tc.wantAuth)
				}

				tc.respond(response, request)
			}))
			t.Cleanup(given.Close)

			ambient := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("request reached the server named by the environment: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(ambient.Close)

			t.Setenv("FORGE_CI_PROVIDER", string(tc.forge))

			for _, name := range tc.envTokens {
				t.Setenv(name, "ambient-token")
			}

			for _, name := range tc.envServers {
				t.Setenv(name, ambient.URL)
			}

			for _, name := range tc.envRepositories {
				t.Setenv(name, "ambient/other")
			}

			tokenFile := env.Path("provider-token")
			require.NoError(t, os.WriteFile(tokenFile, []byte(givenToken+"\n"), 0o600))

			before := os.Environ()

			command := &cli.Command{
				Flags: append(releaseImagesCommonFlags(), releaseImagesProviderTokenFileFlag()),
				Action: func(ctx context.Context, cmd *cli.Command) error {
					common, err := releaseImagesCommonFromCmd(cmd)
					if err != nil {
						return err
					}

					deleter, err := releaseImagesTagDeleter(cmd, common, "")
					if err != nil {
						return err
					}

					forge, ok := deleter.(provider.Provider)
					require.True(t, ok)
					require.Equal(t, tc.forge, forge.Name())

					event, err := forge.ResolveContext(ctx)
					require.NoError(t, err)
					require.Equal(t, "owner/app", event.Repo)

					err = deleter.DeleteTag(ctx, common.Registry+imagePath)

					require.Equal(t, before, os.Environ(), "environment while the deleter is in use")

					return err
				},
			}

			require.NoError(t, command.Run(t.Context(), []string{
				subCmdCleanup, "--tag", "v1.0.0",
				"--server-url", given.URL, "--repository", "owner/app",
				"--provider-token-file", tokenFile,
			}))

			require.Equal(t, before, os.Environ(), "environment after the command")

			mu.Lock()
			defer mu.Unlock()

			require.Equal(t, tc.wantRequests, slices.Clone(requests))
		})
	}
}

// Without a provider token the deleter is refused before any forge is built,
// and a forge without the tag-deletion role is refused by name. The forge is
// the detected one: --server-url configures its adapter and does not select
// it, so a GitHub-style runner is Forgejo only when the job says so.
func TestReleaseImagesTagDeleter_RefusesAMissingTokenAndAForgeWithoutTheRole(t *testing.T) {
	githubStyleRunner := map[string]string{"GITHUB_ACTIONS": "true", "GITHUB_SERVER_URL": "https://github.com"}

	for _, tc := range []struct {
		name, forge, token string
		env                map[string]string
		want               error // nil: a Forgejo deleter is returned
		wantMessage        string
	}{
		{name: "no token", forge: "forgejo", want: errs.ErrUsage, wantMessage: "provider tag-deletion token"},
		{name: "github", forge: "github", token: "t", want: errs.ErrUnsupported, wantMessage: `container tag deletion is not supported on platform "github"`},
		{name: "local", forge: "local", token: "t", want: errs.ErrUnsupported, wantMessage: `container tag deletion is not supported on platform "local"`},
		{
			name: "github-style runner with only --server-url", token: "t", env: githubStyleRunner,
			want: errs.ErrUnsupported, wantMessage: `container tag deletion is not supported on platform "github"`,
		},
		{name: "github-style runner that selects forgejo", forge: "forgejo", token: "t", env: githubStyleRunner},
		{
			name: "github-style runner that names its forgejo server", token: "t",
			env: map[string]string{"GITHUB_ACTIONS": "true", "GITHUB_SERVER_URL": "https://github.com", "FORGEJO_SERVER_URL": "https://forge.example"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testenv.New(t)

			for name, value := range tc.env {
				t.Setenv(name, value)
			}

			t.Setenv("FORGE_CI_PROVIDER", tc.forge)
			t.Setenv("FORGE_CI_PACKAGE_API_TOKEN", tc.token)

			command := &cli.Command{
				Flags: []cli.Flag{releaseImagesProviderTokenFileFlag()},
				Action: func(_ context.Context, cmd *cli.Command) error {
					deleter, err := releaseImagesTagDeleter(cmd, releaseImagesCommon{ServerURL: "https://forge.example", Repository: "owner/app"}, "")
					if err != nil {
						return err
					}

					forge, ok := deleter.(provider.Provider)
					require.True(t, ok)
					require.Equal(t, provider.ForgeForgejo, forge.Name())

					return nil
				},
			}

			err := command.Run(t.Context(), []string{subCmdCleanup})
			if tc.want == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.want)
			require.ErrorContains(t, err, tc.wantMessage)
		})
	}
}

// The tag-deletion token comes from the forge-neutral sources first, then from
// the detected forge's own token variables. A token of another forge is not
// read, so a job that holds only that is refused as one without a token.
func TestReleaseImagesProviderToken_ReadsNeutralSourcesThenTheDetectedForgeOnly(t *testing.T) {
	const (
		neutral = "FORGE_CI_PACKAGE_API_TOKEN"
		file    = "token-from-file"
	)

	for _, tc := range []struct {
		forge string
		env   []string // each set to "<name>-value"
		file  bool
		want  string // empty: refused as missing
	}{
		{forge: "forgejo", env: []string{neutral, "FORGEJO_TOKEN", "GITEA_TOKEN", "GITHUB_TOKEN"}, file: true, want: file},
		{forge: "forgejo", env: []string{neutral, "FORGEJO_TOKEN", "GITEA_TOKEN", "GITHUB_TOKEN"}, want: neutral + "-value"},
		{forge: "forgejo", env: []string{"FORGEJO_TOKEN", "GITEA_TOKEN", "GITHUB_TOKEN"}, want: "FORGEJO_TOKEN-value"},
		{forge: "forgejo", env: []string{"GITEA_TOKEN", "GITHUB_TOKEN"}, want: "GITEA_TOKEN-value"},
		{forge: "forgejo", env: []string{"GITHUB_TOKEN", "GH_TOKEN", "GITLAB_TOKEN", "CI_TOKEN", "CI_JOB_TOKEN"}},
		{forge: "gitlab", env: []string{neutral, "GITLAB_TOKEN"}, file: true, want: file},
		{forge: "gitlab", env: []string{neutral, "GITLAB_TOKEN"}, want: neutral + "-value"},
		{forge: "gitlab", env: []string{"GITLAB_TOKEN", "FORGEJO_TOKEN", "GITHUB_TOKEN"}, want: "GITLAB_TOKEN-value"},
		{forge: "gitlab", env: []string{"FORGEJO_TOKEN", "GITEA_TOKEN", "GITHUB_TOKEN", "GH_TOKEN", "CI_TOKEN", "CI_JOB_TOKEN"}},
		{forge: "github", env: []string{neutral, "GITHUB_TOKEN"}, want: neutral + "-value"},
		{forge: "github", env: []string{"GITHUB_TOKEN", "FORGEJO_TOKEN"}, want: "GITHUB_TOKEN-value"},
		{forge: "github", env: []string{"FORGEJO_TOKEN", "GITEA_TOKEN", "GITLAB_TOKEN", "CI_TOKEN"}},
		{forge: "local", env: []string{neutral, "GITHUB_TOKEN"}, want: neutral + "-value"},
		{forge: "local", env: []string{"FORGEJO_TOKEN", "GITEA_TOKEN", "GITLAB_TOKEN", "GITHUB_TOKEN", "GH_TOKEN", "CI_TOKEN"}},
	} {
		name := tc.forge + "/" + strings.Join(tc.env, "+")
		if tc.file {
			name += "+file"
		}

		t.Run(name, func(t *testing.T) {
			env := testenv.New(t)
			t.Setenv("FORGE_CI_PROVIDER", tc.forge)

			for _, variable := range tc.env {
				t.Setenv(variable, variable+"-value")
			}

			var args []string

			if tc.file {
				tokenFile := env.Path("provider-token")
				require.NoError(t, os.WriteFile(tokenFile, []byte(file+"\n"), 0o600))

				args = []string{"--provider-token-file", tokenFile}
			}

			var got string

			command := &cli.Command{
				Flags: []cli.Flag{releaseImagesProviderTokenFileFlag()},
				Action: func(_ context.Context, cmd *cli.Command) error {
					token, err := releaseImagesProviderToken(cmd)
					got = token

					return err
				},
			}

			err := command.Run(t.Context(), append([]string{subCmdCleanup}, args...))

			if tc.want == "" {
				require.ErrorIs(t, err, errs.ErrUsage)
				require.ErrorContains(t, err, "provider tag-deletion token")
				require.Empty(t, got)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// runReleaseImagesVerb runs one release-images verb against a fixed forge and
// repository with no registry username, and returns what it wrote to stderr.
func runReleaseImagesVerb(t *testing.T, env *testenv.Env, verb string, extra ...string) (string, error) {
	t.Helper()

	args := []string{verb, "--tag", "v1.0.0", "--server-url", "https://registry.example", "--repository", "owner/app"}
	if verb == subCmdPromote {
		args = append(args, "--state-dir", "state")
	}

	var command *cli.Command

	for _, cmd := range releaseImagesGroup().Commands {
		if cmd.Name == verb {
			command = cmd
		}
	}

	require.NotNil(t, command, verb)

	stderrFile, err := os.Create(env.Path("stderr"))
	require.NoError(t, err)

	stderr := os.Stderr
	os.Stderr = stderrFile

	t.Cleanup(func() {
		os.Stderr = stderr
	})

	runErr := command.Run(t.Context(), append(args, extra...))

	os.Stderr = stderr

	require.NoError(t, stderrFile.Close())

	output, err := os.ReadFile(env.Path("stderr"))
	require.NoError(t, err)

	return string(output), runErr
}

// Rollback and cleanup read two secrets from files, and stdin can supply only
// one of them. The refusal comes from the flags, before the ledger, the journal
// or either secret is read: nothing the command would read exists here.
func TestReleaseImages_RefusesProviderTokenAndRegistryPasswordBothFromStdin(t *testing.T) {
	for _, verb := range []string{subCmdRollback, subCmdCleanup} {
		t.Run(verb, func(t *testing.T) {
			env := testenv.New(t)
			t.Chdir(env.Temp)

			_, err := runReleaseImagesVerb(t, env, verb, "--provider-token-file", "-", "--registry-password-file", "-")
			require.ErrorIs(t, err, errs.ErrUsage)
			require.ErrorContains(t, err, "--provider-token-file and --registry-password-file both read stdin")
		})
	}
}

// Images on another registry are not the forge's packages: the deleter is
// that registry's, through the login's auth file, and no provider token is
// read.
func TestReleaseImagesTagDeleter_AnotherRegistryUsesTheImagesRegistry(t *testing.T) {
	t.Parallel()

	deleter, err := releaseImagesTagDeleter(&cli.Command{}, releaseImagesCommon{
		ServerURL:               "https://forge.example",
		Registry:                "registry.example",
		Repository:              "owner/app",
		RegistryUsername:        "registry-user",
		ExpectedImageRepository: "registry.example/owner/app",
		Foreign:                 true,
	}, "/tmp/auth.json")
	require.NoError(t, err)

	_, isAdapter := deleter.(*ociregistry.Adapter)
	require.True(t, isAdapter, "deleter = %T, want the plain OCI adapter", deleter)
}

// Where the images are is derived from the repository's host: the forge's
// own registry is the server's host, or --registry for a forge that serves it
// apart (GitLab). The login always goes to the repository's host.
func TestResolveImageRegistry_DerivesTheForeignRegistryFromTheHost(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, forgeRegistry, serverHost, repository string
		want                                        imageRegistry
	}{
		{name: "forge's own", serverHost: "forge.example:8443", repository: "forge.example:8443/owner/app", want: imageRegistry{Host: "forge.example:8443"}},
		// Host names are case-insensitive; the default repository is lowercased.
		{name: "forge's own, server URL in capitals", serverHost: "Forge.Example", repository: "forge.example/owner/app", want: imageRegistry{Host: "forge.example"}},
		{name: "GHCR", serverHost: "forge.example", repository: "ghcr.io/owner/app", want: imageRegistry{Host: "ghcr.io", Foreign: true}},
		{name: "the forge's host on another port", serverHost: "forge.example:8443", repository: "forge.example/owner/app", want: imageRegistry{Host: "forge.example", Foreign: true}},
		{name: "GitLab's registry", forgeRegistry: "registry.gitlab.example", serverHost: "gitlab.example", repository: "registry.gitlab.example/group/app", want: imageRegistry{Host: "registry.gitlab.example"}},
		{name: "GitLab beside GHCR", forgeRegistry: "registry.gitlab.example", serverHost: "gitlab.example", repository: "ghcr.io/owner/app", want: imageRegistry{Host: "ghcr.io", Foreign: true}},
	} {
		got, err := resolveImageRegistry(tc.forgeRegistry, tc.serverHost, tc.repository)
		require.NoError(t, err, tc.name)
		require.Equal(t, tc.want, got, tc.name)
	}
}

// On the forge's own registry the token logs in as the repository owner when
// no user is named; another registry has its own users, so there the user
// must be named, and the refusal says which input names it.
func TestRegistryLoginUser_DefaultsToTheOwnerOnTheForgeOnly(t *testing.T) {
	t.Parallel()

	got, err := registryLoginUser("", "owner/app", imageRegistry{Host: "forge.example"})
	require.NoError(t, err)
	require.Equal(t, "owner", got)

	got, err = registryLoginUser("someone", "owner/app", imageRegistry{Host: "ghcr.io", Foreign: true})
	require.NoError(t, err)
	require.Equal(t, "someone", got)

	_, err = registryLoginUser("", "owner/app", imageRegistry{Host: "ghcr.io", Foreign: true})
	require.ErrorIs(t, err, errs.ErrUsage)
	require.ErrorContains(t, err, "registry-user")
	require.ErrorContains(t, err, "ghcr.io")
}

// A login names its registry by host, URL or an image repository on it: a
// consumer can hand the repository its images go to.
func TestRegistryForLogin_ReducesARepositoryToItsHost(t *testing.T) {
	t.Parallel()

	for registry, want := range map[string]string{
		"ghcr.io":                        "ghcr.io",
		"ghcr.io/example-org/nanolinter": "ghcr.io",
		"https://forge.example:8443":     "forge.example:8443",
		"forge.example:8443/owner/app":   "forge.example:8443",
	} {
		got, err := registryForLogin(registry, "https://forge.example:8443", true)
		require.NoError(t, err, registry)
		require.Equal(t, want, got, registry)
	}
}
