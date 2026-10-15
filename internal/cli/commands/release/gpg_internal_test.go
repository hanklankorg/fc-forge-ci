// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
)

var errFromCallback = errors.New("callback failed")

// The GPG subcommands run under an ephemeral GNUPGHOME. Three things have
// to hold and none had a test: the keyring home is private (gpg refuses
// one others can read, and a private key is imported into it), the
// callback sees it, and the process environment is put back afterwards —
// this mutates a global, so a leak follows the runner into whatever runs
// next.

// standInAgent puts a gpg-connect-agent on PATH that records the GNUPGHOME it
// was run under and its arguments, one line a call, and returns that record.
// Every test here uses it: the cleanup stops the agent, and the real tool must
// not run against the test's temporary home.
func standInAgent(t *testing.T) func() []string {
	t.Helper()

	log := filepath.Join(t.TempDir(), "agent-calls")
	mock := mockbinary.New(t)
	mock.OnPath()
	mock.Add("gpg-connect-agent", `printf '%s %s\n' "${GNUPGHOME:-}" "$*" >>'`+log+`'`)

	return func() []string {
		data, err := os.ReadFile(log)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}

		if err != nil {
			t.Fatal(err)
		}

		return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	}
}

// gpg starts an agent for the home it works in, and the agent outlives the
// command and holds the unlocked key. The cleanup stops it while GNUPGHOME
// still names the temporary home, whether signing worked, failed, or was
// interrupted, and before the home is removed.
func TestWithEphemeralGNUPGHome_StopsTheAgentOfItsOwnHome(t *testing.T) {
	for name, test := range map[string]struct {
		cancelled bool
		result    error
	}{
		"after signing":            {},
		"after a failed signing":   {result: errFromCallback},
		"after an interrupted one": {cancelled: true, result: context.Canceled},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("RUNNER_TEMP", t.TempDir())
			t.Setenv("GNUPGHOME", "/pre-existing/home")

			calls := standInAgent(t)

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			var home string

			err := withEphemeralGNUPGHome(ctx, func() error {
				home = os.Getenv("GNUPGHOME")

				if test.cancelled {
					cancel()
				}

				return test.result
			})
			if !errors.Is(err, test.result) {
				t.Fatalf("err = %v, want the callback's result %v", err, test.result)
			}

			if got, want := calls(), []string{home + " KILLAGENT /bye"}; len(got) != 1 || got[0] != want[0] {
				t.Fatalf("agent calls = %q, want %q", got, want)
			}

			if _, statErr := os.Stat(home); !os.IsNotExist(statErr) {
				t.Errorf("GNUPGHOME survived the call (stat err = %v)", statErr)
			}
		})
	}
}

func TestWithEphemeralGNUPGHome_PrivateHomeUnderRunnerTemp(t *testing.T) {
	standInAgent(t)

	runnerTemp := t.TempDir()
	t.Setenv("RUNNER_TEMP", runnerTemp)
	t.Setenv("GNUPGHOME", "/pre-existing/home")

	var seen string

	err := withEphemeralGNUPGHome(t.Context(), func() error {
		seen = os.Getenv("GNUPGHOME")

		info, statErr := os.Stat(seen)
		if statErr != nil {
			return statErr
		}

		// 0700: gpg refuses a keyring home others can read.
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Errorf("GNUPGHOME mode = %v, want 0700", perm)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Created under the runner temp dir, so it is inside whatever the
	// runner cleans up rather than the shared system temp.
	if !strings.HasPrefix(seen, runnerTemp) {
		t.Errorf("GNUPGHOME %q is not under $RUNNER_TEMP %q", seen, runnerTemp)
	}

	if _, statErr := os.Stat(seen); !os.IsNotExist(statErr) {
		t.Errorf("GNUPGHOME survived the call (stat err = %v)", statErr)
	}

	if got := os.Getenv("GNUPGHOME"); got != "/pre-existing/home" {
		t.Errorf("GNUPGHOME = %q afterwards, want the pre-existing value restored", got)
	}
}

func TestWithEphemeralGNUPGHome_UnsetStaysUnset(t *testing.T) {
	standInAgent(t)
	t.Setenv("RUNNER_TEMP", t.TempDir())
	t.Setenv("GNUPGHOME", "placeholder") // so t.Setenv restores it afterwards

	_ = os.Unsetenv("GNUPGHOME")

	if err := withEphemeralGNUPGHome(t.Context(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}

	// Restoring must not invent a value where there was none: an empty
	// GNUPGHOME sends gpg to ~/.gnupg, which is the opposite of the
	// isolation this function exists to provide.
	if _, ok := os.LookupEnv("GNUPGHOME"); ok {
		t.Errorf("GNUPGHOME is now set to %q; it was unset before", os.Getenv("GNUPGHOME"))
	}
}

func TestWithEphemeralGNUPGHome_CleansUpAfterAFailedCallback(t *testing.T) {
	standInAgent(t)

	runnerTemp := t.TempDir()
	t.Setenv("RUNNER_TEMP", runnerTemp)
	t.Setenv("GNUPGHOME", "/pre-existing/home")

	var seen string

	err := withEphemeralGNUPGHome(t.Context(), func() error {
		seen = os.Getenv("GNUPGHOME")

		return errFromCallback
	})
	if !errors.Is(err, errFromCallback) {
		t.Fatalf("err = %v, want the callback's error", err)
	}

	// A failed signing run must not leave a keyring behind.
	if _, statErr := os.Stat(seen); !os.IsNotExist(statErr) {
		t.Errorf("GNUPGHOME survived a failed callback (stat err = %v)", statErr)
	}

	if got := os.Getenv("GNUPGHOME"); got != "/pre-existing/home" {
		t.Errorf("GNUPGHOME = %q after a failure, want the pre-existing value restored", got)
	}

	entries, readErr := os.ReadDir(runnerTemp)
	if readErr != nil {
		t.Fatal(readErr)
	}

	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "forge-ci-gnupg-") {
			t.Errorf("left %q behind in the runner temp dir", e.Name())
		}
	}
}

// The GPG key and its passphrase are two inputs and stdin can supply one. The
// refusal comes from the flags, before either is read: the test's stdin is not
// a pipe, so a read would fail with a different error.
func TestGPGKeyAndPassphrase_RefusedWhenBothReadStdin(t *testing.T) {
	both := []string{"--private-key-file", "-", "--passphrase-file", "-"}

	for _, command := range []string{
		"gpg import",
		"gpg sign-packages --fingerprint 0123456789ABCDEF0123456789ABCDEF01234567",
		"sign --method gpg --file app.tgz",
	} {
		t.Run(command, func(t *testing.T) {
			t.Chdir(t.TempDir())

			if err := os.WriteFile("app.tgz", []byte("fixture"), 0o600); err != nil {
				t.Fatal(err)
			}

			err := New().Run(t.Context(), append(append([]string{New().Name}, strings.Fields(command)...), both...))
			if !errors.Is(err, errs.ErrUsage) || !strings.Contains(err.Error(), "--private-key-file and --passphrase-file both read stdin") {
				t.Fatalf("err = %v, want the two-stdin-readers usage error", err)
			}
		})
	}
}
