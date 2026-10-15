// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package cli_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/cli"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
)

func TestNew_VersionString(t *testing.T) {
	t.Parallel()

	cmd := cli.New(cli.BuildInfo{Version: "1.2.3", Commit: "abc1234", Date: "2026-01-01T00:00:00Z"})

	require.Equal(t, "forge-ci", cmd.Name)
	require.Equal(t, "1.2.3 (commit abc1234, built 2026-01-01T00:00:00Z)", cmd.Version)
}

func TestNew_DocsLinkTracksInstalledVersion(t *testing.T) {
	t.Parallel()

	// A release build points the Docs link at its own tag so the web docs
	// match the installed binary (workflows pin forge-ci to a tag).
	tagged := cli.New(cli.BuildInfo{Version: "v0.90.0", Commit: "abc", Date: "d"})
	require.Contains(t, tagged.Description, "tree/v0.90.0/docs")

	// dev/snapshot builds (no real tag) fall back to main.
	for _, v := range []string{"dev", "", "snapshot-123"} {
		dev := cli.New(cli.BuildInfo{Version: v, Commit: "abc", Date: "d"})
		require.Contains(t, dev.Description, "tree/main/docs",
			"version %q should fall back to main", v)
	}
}

// TestRoot_RunHelp asserts what help produces and where, not only that it
// returns. It used to require NoError and nothing else, with the text going to
// the process's real stdout -- so help could print nothing, print to stderr,
// or lose every command and still pass. Explicit help is a successful request:
// the text belongs on stdout, stderr stays empty, and the operator has to be
// able to find the commands they came for.
func TestRoot_RunHelp(t *testing.T) {
	t.Parallel()

	cmd := cli.New(cli.BuildInfo{Version: "x", Commit: "y", Date: "z"})

	var stdout, stderr bytes.Buffer

	cmd.Writer = &stdout
	cmd.ErrWriter = &stderr

	err := cmd.Run(context.Background(), []string{"forge-ci", "--help"})
	require.NoError(t, err)
	require.Empty(t, stderr.String(), "help wrote to stderr")

	for _, want := range []string{"artifact", "build", "container", "release", "validate", "--log-level"} {
		require.Containsf(t, stdout.String(), want, "help does not mention %q", want)
	}
}

func TestRoot_RejectsBogusLogLevel(t *testing.T) {
	t.Parallel()

	cmd := cli.New(cli.BuildInfo{Version: "x", Commit: "y", Date: "z"})
	err := cmd.Run(context.Background(), []string{"forge-ci", "--log-level=bogus"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid log-level")
}

// TestRoot_AcceptsValidLogLevels drives the Before hook and reads the
// threshold it installs.
//
// Both tests this replaces passed --help, and --help short-circuits before
// Before runs: measured, `--log-level=bogus --help` returns nil and leaves the
// logger untouched, and `--quiet --help` never lowers anything. So they could
// not fail for any level string, and the one named for quiet never looked at
// quiet. Without --help the root still prints help after Before has run, which
// is the safe invocation that actually exercises the hook.
//
// Not parallel: configureLogger replaces the process-wide slog default, and a
// concurrent test would read another test's threshold. The default is restored
// afterwards so the rest of the package logs as it did.
func TestRoot_AcceptsValidLogLevels(t *testing.T) {
	previous := slog.Default()

	t.Cleanup(func() { slog.SetDefault(previous) })

	for _, tc := range []struct {
		args          []string
		enabled       slog.Level
		belowDisabled slog.Level
	}{
		{args: []string{"--log-level=debug"}, enabled: slog.LevelDebug},
		{args: []string{"--log-level=DEBUG"}, enabled: slog.LevelDebug},
		{args: []string{"--log-level=info"}, enabled: slog.LevelInfo, belowDisabled: slog.LevelDebug},
		{args: []string{"--log-level=INFO"}, enabled: slog.LevelInfo, belowDisabled: slog.LevelDebug},
		{args: []string{"--log-level=warn"}, enabled: slog.LevelWarn, belowDisabled: slog.LevelInfo},
		{args: []string{"--log-level=warning"}, enabled: slog.LevelWarn, belowDisabled: slog.LevelInfo},
		{args: []string{"--log-level=error"}, enabled: slog.LevelError, belowDisabled: slog.LevelWarn},
		// --quiet raises the threshold to error, even past an explicit level.
		{args: []string{"--quiet"}, enabled: slog.LevelError, belowDisabled: slog.LevelWarn},
		{args: []string{"--log-level=debug", "--quiet"}, enabled: slog.LevelError, belowDisabled: slog.LevelWarn},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			// A known starting point, so a hook that silently did nothing
			// cannot pass by inheriting the previous case's threshold.
			slog.SetDefault(slog.New(slog.DiscardHandler))

			cmd := cli.New(cli.BuildInfo{Version: "x", Commit: "y", Date: "z"})
			cmd.Writer = io.Discard
			cmd.ErrWriter = io.Discard

			require.NoError(t, cmd.Run(context.Background(), append([]string{"forge-ci"}, tc.args...)))

			ctx := context.Background()
			require.Truef(t, slog.Default().Enabled(ctx, tc.enabled), "level %v is not enabled", tc.enabled)

			if tc.enabled != slog.LevelDebug {
				require.Falsef(t, slog.Default().Enabled(ctx, tc.belowDisabled), "level %v is still enabled", tc.belowDisabled)
			}
		})
	}
}

func TestRoot_RejectsBogusProvider(t *testing.T) {
	t.Parallel()

	cmd := cli.New(cli.BuildInfo{Version: "x", Commit: "y", Date: "z"})
	// No --help: --help short-circuits before a Before-hook error
	// surfaces (same reason TestRoot_RejectsBogusLogLevel omits it).
	err := cmd.Run(context.Background(), []string{"forge-ci", "--provider=bananas"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid --provider")
}

func TestRoot_RejectsBogusRunner(t *testing.T) {
	t.Parallel()

	cmd := cli.New(cli.BuildInfo{Version: "x", Commit: "y", Date: "z"})
	err := cmd.Run(context.Background(), []string{"forge-ci", "--runner=spaceship"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid --runner")
}

func TestRoot_ProviderFlagBridgesToEnv(t *testing.T) {
	// Not parallel: the Before hook bridges argv → FORGE_CI_PROVIDER
	// via os.Setenv so platform.Detect (which reads env) observes it.
	// t.Setenv records the original and restores it after the test even
	// though the bridge overwrites it mid-run.
	t.Setenv("FORGE_CI_PROVIDER", "")

	cmd := cli.New(cli.BuildInfo{Version: "x", Commit: "y", Date: "z"})
	// No subcommand + valid flag: Before runs the bridge, then root
	// prints help and returns nil — leaving the bridged env observable.
	err := cmd.Run(context.Background(), []string{"forge-ci", "--provider=forgejo"})
	require.NoError(t, err)
	require.Equal(t, "forgejo", os.Getenv("FORGE_CI_PROVIDER"))
}

// A plan feeds flags ahead of the environment, and `container build` scans
// only when its plan says so. A plan that is named but cannot be read as one
// is therefore a refusal before any command runs, not a warning and a run on
// other values. A plan that is not there yet is no plan: `plan write` makes it.
func TestRoot_RefusesAnUnreadablePlanBeforeAnyCommandRuns(t *testing.T) {
	for name, test := range map[string]struct {
		plan    string
		written bool
		refused bool
	}{
		"a plan":                   {plan: `{"container build":{"context":"."}}`, written: true},
		"no file at the path":      {},
		"a file that is no plan":   {plan: "not json", written: true, refused: true},
		"a plan with a null scope": {plan: `{"container build":null}`, written: true, refused: true},
	} {
		t.Run(name, func(t *testing.T) {
			mock := mockbinary.New(t)
			mock.OnPath()
			mock.Add("gpg", "")
			mock.Add("gpg-connect-agent", "")

			path := filepath.Join(t.TempDir(), "plan.json")
			if test.written {
				require.NoError(t, os.WriteFile(path, []byte(test.plan), 0o600))
			}

			t.Setenv("FORGE_CI_PLAN", path)
			t.Setenv("GNUPGHOME", t.TempDir())

			cmd := cli.New(cli.BuildInfo{Version: "x", Commit: "y", Date: "z"})
			err := cmd.Run(t.Context(), []string{"forge-ci", "release", "gpg", "cleanup", "--fingerprint", "0123456789ABCDEF0123456789ABCDEF01234567"})

			if !test.refused {
				require.NoError(t, err)
				require.NotEmpty(t, mock.All(), "the command ran no tool, so the refusal rows prove nothing")

				return
			}

			require.ErrorIs(t, err, errs.ErrInvalidConfig)
			require.Contains(t, err.Error(), path)
			require.Empty(t, mock.All(), "a tool ran although the plan was refused")
		})
	}
}
