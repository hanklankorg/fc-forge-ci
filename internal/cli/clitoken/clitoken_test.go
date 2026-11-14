// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package clitoken_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/cli/clitoken"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/runcontext"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

// tokenCmd parses the shared flag without invoking a use case.
func tokenCmd(t *testing.T, args ...string) *cli.Command {
	t.Helper()

	cmd := &cli.Command{Flags: []cli.Flag{clitoken.FileFlag("Git token.")}}
	if err := cmd.Run(t.Context(), append([]string{"x"}, args...)); err != nil {
		t.Fatal(err)
	}

	return cmd
}

func tokenFile(t *testing.T, value string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func resolvedToken(t *testing.T, resolve func(*cli.Command) (runcontext.Credential, error), args ...string) runcontext.Credential {
	t.Helper()

	cred, err := resolve(tokenCmd(t, args...))
	if err != nil {
		t.Fatal(err)
	}

	return cred
}

// TestResolve_ProvenLeakIsClosed is the end-to-end regression guard for a
// disclosure that was real, not theoretical.
//
// On a GitHub runner, `platform checkout` resolved --server-url and --token
// from two INDEPENDENT chains. A workflow checking out from a third-party
// Forgejo set $FORGEJO_SERVER_URL, and $FORGEJO_TOKEN was unset (an unset
// `${{ secrets.X }}` interpolates to "", and empty means absent, so it fell
// through) -- so the destination became the Forgejo host while the token fell
// through to $GITHUB_TOKEN. Git then sent GitHub's job token to that host as
// HTTP Basic auth.
//
// Nothing in a pair of strings required the two to agree. Now the credential
// carries the server that issued it, and the checkout is anonymous instead.
func TestResolve_ProvenLeakIsClosed(t *testing.T) {
	testenv.New(t)
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_TOKEN", "ghs_github_job_token")
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	t.Setenv("FORGEJO_SERVER_URL", "https://third-party.example")
	t.Setenv("FORGEJO_TOKEN", "")

	cred := resolvedToken(t, clitoken.Resolve)

	if got := cred.For("https://third-party.example/o/r"); got != "" {
		t.Error("GitHub's job token must not reach a third-party host")
	}

	if got := cred.For("https://github.com/o/r"); got != "ghs_github_job_token" {
		t.Error("the runner's token must still work at its own server")
	}
}

// Explicit input preserves the operator's ability to select a cross-forge
// credential without placing that credential in argv.
func TestResolve_ExplicitFileWinsAndIsUnrestricted(t *testing.T) {
	testenv.New(t)
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_TOKEN", "ghs_ambient")
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")

	cred := resolvedToken(t, clitoken.Resolve, "--token-file", tokenFile(t, "operator-token\r\n"))

	if got := cred.For("https://anywhere.example/o/r"); got != "operator-token" {
		t.Error("explicit file input must win and be honored at the operator's destination")
	}
}

// TestResolveRelease_PrefersDedicatedToken verifies release-token precedence.
// Token scope remains the operator's responsibility.
func TestResolveRelease_PrefersDedicatedToken(t *testing.T) {
	testenv.New(t)
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_TOKEN", "ghs_ambient")
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	t.Setenv("RELEASE_TOKEN", "dedicated_write_token")

	cred := resolvedToken(t, clitoken.ResolveRelease)

	if got := cred.For("https://github.com/o/r"); got != "dedicated_write_token" {
		t.Error("dedicated write token must win")
	}
}

func TestTokenTests_IsolateAmbientSources(t *testing.T) {
	t.Setenv("CI_TOKEN", "synthetic-ambient-canary")
	t.Setenv("RELEASE_TOKEN", "synthetic-release-canary")
	t.Run("implicit", TestResolve_ProvenLeakIsClosed)
	t.Run("explicit", TestResolve_ExplicitFileWinsAndIsUnrestricted)
	t.Run("dedicated", TestResolveRelease_PrefersDedicatedToken)
}

// File input follows the shared secret resolver's CR/LF-only policy. Ambient
// selection retains the same normalization without losing its issuer binding.
func TestResolve_TrailingLineEndingsAreTrimmed(t *testing.T) {
	t.Run("an ambient token is trimmed", func(t *testing.T) {
		for _, tc := range []struct{ name, stored, want string }{
			{name: "unix newline", stored: "ghs_tok\n", want: "ghs_tok"},
			{name: "windows newline", stored: "ghs_tok\r\n", want: "ghs_tok"},
			{name: "bare carriage return", stored: "ghs_tok\r", want: "ghs_tok"},
			{name: "several trailing newlines", stored: "ghs_tok\n\n", want: "ghs_tok"},
			{name: "no trailing newline", stored: "ghs_tok", want: "ghs_tok"},
			// Only TRAILING line endings come off; a token is opaque
			// otherwise, and trimming more would corrupt a valid one.
			{name: "leading space is preserved", stored: " ghs_tok\n", want: " ghs_tok"},
			{name: "trailing space is preserved", stored: "ghs_tok \n", want: "ghs_tok "},
		} {
			t.Run(tc.name, func(t *testing.T) {
				testenv.New(t)
				t.Setenv("GITHUB_ACTIONS", "true")
				t.Setenv("GITHUB_SERVER_URL", "https://github.com")
				t.Setenv("GITHUB_TOKEN", tc.stored)

				got := resolvedToken(t, clitoken.Resolve).For("https://github.com/o/r")
				if got != tc.want {
					t.Errorf("ambient token = %q, want %q", got, tc.want)
				}
			})
		}
	})

	t.Run("file input trims only trailing CR/LF", func(t *testing.T) {
		testenv.New(t)

		got := resolvedToken(t, clitoken.Resolve, "--token-file", tokenFile(t, " operator-token \r\n")).For("https://anywhere.example/o/r")
		if got != " operator-token " {
			t.Error("file input must preserve spaces while removing trailing line endings")
		}
	})

	t.Run("the release chain trims its ambient token too", func(t *testing.T) {
		testenv.New(t)
		t.Setenv("GITHUB_ACTIONS", "true")
		t.Setenv("GITHUB_SERVER_URL", "https://github.com")
		t.Setenv("RELEASE_TOKEN", "dedicated_write_token\r\n")

		got := resolvedToken(t, clitoken.ResolveRelease).For("https://github.com/o/r")
		if got != "dedicated_write_token" {
			t.Errorf("release token = %q, want it trimmed", got)
		}
	})
}

func TestResolve_ExplicitSourcesNeverFallBack(t *testing.T) {
	for name, resolve := range map[string]func(*cli.Command) (runcontext.Credential, error){
		"read": clitoken.Resolve, "release": clitoken.ResolveRelease,
	} {
		t.Run(name, func(t *testing.T) {
			testenv.New(t)
			t.Setenv("GITHUB_ACTIONS", "true")
			t.Setenv("GITHUB_SERVER_URL", "https://github.com")
			t.Setenv("GITHUB_TOKEN", "ambient-canary")
			t.Setenv("RELEASE_TOKEN", "release-canary")

			for _, tc := range []struct {
				name, path, want string
				err              error
			}{
				{"file", tokenFile(t, "explicit-token\r\n"), "explicit-token", nil},
				{"empty file", tokenFile(t, ""), "", nil},
				{"line endings only", tokenFile(t, "\r\n"), "", nil},
				{"missing file", filepath.Join(t.TempDir(), "missing"), "", errs.ErrMissingInput},
				{"empty path", "", "", errs.ErrUsage},
			} {
				t.Run(tc.name, func(t *testing.T) {
					cred, err := resolve(tokenCmd(t, "--token-file", tc.path))
					if !errors.Is(err, tc.err) {
						t.Fatalf("file resolution error = %v, want %v", err, tc.err)
					}

					for _, destination := range []string{"https://github.com/o/r", "https://elsewhere.example/o/r"} {
						if cred.For(destination) != tc.want {
							t.Error("explicit input fell back to environment or lost operator scope")
						}
					}
				})
			}
		})
	}
}

// Not parallel: stdin is process-global and is restored before closing the fixture.
func TestResolve_StdinOverridesAmbient(t *testing.T) {
	for name, resolve := range map[string]func(*cli.Command) (runcontext.Credential, error){
		"read": clitoken.Resolve, "release": clitoken.ResolveRelease,
	} {
		t.Run(name, func(t *testing.T) {
			for _, value := range []string{"piped-token\r\n", ""} {
				t.Run(value, func(t *testing.T) {
					testenv.New(t)
					t.Setenv("CI_TOKEN", "ambient-canary")
					t.Setenv("RELEASE_TOKEN", "release-canary")

					reader, writer, err := os.Pipe()
					if err != nil {
						t.Fatal(err)
					}

					previous := os.Stdin
					os.Stdin = reader

					t.Cleanup(func() {
						os.Stdin = previous
						_ = reader.Close()
						_ = writer.Close()
					})

					if _, err := writer.WriteString(value); err != nil {
						t.Fatal(err)
					}

					if err := writer.Close(); err != nil {
						t.Fatal(err)
					}

					want := ""
					if value != "" {
						want = "piped-token"
					}

					if resolvedToken(t, resolve, "--token-file", "-").For("https://elsewhere.example/o/r") != want {
						t.Error("stdin did not override environment credentials")
					}
				})
			}
		})
	}
}
