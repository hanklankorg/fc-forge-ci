// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

func TestSSHCommands_RefuseUnselectedDestinationWithoutEffects(t *testing.T) {
	for _, verb := range []string{"setup", "cleanup"} {
		t.Run(verb, func(t *testing.T) {
			env := testenv.New(t)
			t.Chdir(t.TempDir())
			t.Setenv("SSH_SIGNING_KEY", "synthetic-new-key")

			shared := filepath.Join(env.Temp, "forge-ci-ssh-signing-key")
			require.NoError(t, os.WriteFile(shared, []byte("other-invocation-key"), 0o600))
			git := mockbinary.New(t)
			git.Add("git", "exit 97")
			git.OnPath()

			command := sshSetupCmd()
			if verb == "cleanup" {
				command = sshCleanupCmd()
			}

			err := command.Run(t.Context(), []string{verb})
			require.ErrorIs(t, err, errs.ErrUsage)
			assert.Equal(t, errs.ExitCodeUsage, errs.ExitCodeFromError(err))
			assert.Contains(t, err.Error(), "--key-path")
			assert.Contains(t, err.Error(), "CI_TEMP_DIR")
			assert.Contains(t, err.Error(), "RUNNER_TEMP")

			body, readErr := os.ReadFile(shared)
			require.NoError(t, readErr)
			assert.Equal(t, "other-invocation-key", string(body))
			assert.Empty(t, git.Invocations("git"))
			assert.Equal(t, "synthetic-new-key", os.Getenv("SSH_SIGNING_KEY"))
		})
	}
}

// Serial: tests replace process stdin and environment, restoring both at cleanup.
func TestSSHSetup_AdmitsDestinationBeforeReadingStdin(t *testing.T) {
	for _, keyArgs := range [][]string{nil, {"--key-path", "-"}} {
		t.Run("destination="+strings.Join(keyArgs, " "), func(t *testing.T) {
			env := testenv.New(t)
			t.Chdir(t.TempDir())
			input := filepath.Join(t.TempDir(), "stdin-key")
			require.NoError(t, os.WriteFile(input, []byte("synthetic-stdin-key"), 0o600))
			reader, err := os.Open(input)
			require.NoError(t, err)

			previous := os.Stdin
			os.Stdin = reader

			t.Cleanup(func() {
				os.Stdin = previous
				_ = reader.Close()
			})
			git := mockbinary.New(t)
			git.Add("git", "exit 97")
			git.OnPath()

			err = sshSetupCmd().Run(t.Context(), append([]string{"setup", "--private-key-file", "-"}, keyArgs...))
			require.ErrorIs(t, err, errs.ErrUsage)

			offset, seekErr := reader.Seek(0, io.SeekCurrent)
			require.NoError(t, seekErr)
			assert.Zero(t, offset, "refusal must not consume the supplied key")
			assert.Empty(t, git.Invocations("git"))

			_, statErr := os.Stat(filepath.Join(env.Temp, "forge-ci-ssh-signing-key"))
			require.ErrorIs(t, statErr, os.ErrNotExist)
		})
	}
}

func TestSSHCleanup_RejectsStdoutWithoutDeletingAFile(t *testing.T) {
	testenv.New(t)
	t.Chdir(t.TempDir())
	require.NoError(t, os.WriteFile("-", []byte("unrelated-file"), 0o600))

	err := sshCleanupCmd().Run(t.Context(), []string{"cleanup", "--key-path", "-"})
	require.ErrorIs(t, err, errs.ErrUsage)

	body, readErr := os.ReadFile("-")
	require.NoError(t, readErr)
	assert.Equal(t, "unrelated-file", string(body))
}

func TestSSHCommands_SelectedDestinationRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		flag, env, ci, runner bool
		want                  string
	}{
		{name: "flag overrides environment", flag: true, env: true, ci: true, runner: true, want: "flag/key with spaces"},
		{name: "environment overrides CI", env: true, ci: true, runner: true, want: "env/key"},
		{name: "CI alias wins", ci: true, runner: true, want: "ci/forge-ci-ssh-signing-key"},
		{name: "runner directory", runner: true, want: "runner/forge-ci-ssh-signing-key"},
		{name: "explicit path off a runner", flag: true, want: "flag/key with spaces"},
		{name: "environment path off a runner", env: true, want: "env/key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := testenv.New(t)
			root := t.TempDir()
			t.Chdir(root)
			t.Setenv("SSH_SIGNING_KEY", "synthetic-signing-key")

			if tc.env {
				t.Setenv("SSH_SIGNING_KEY_PATH", filepath.Join(root, "env", "key"))
			}

			if tc.ci {
				t.Setenv("CI_TEMP_DIR", filepath.Join(root, "ci"))
			}

			if tc.runner {
				t.Setenv("RUNNER_TEMP", filepath.Join(root, "runner"))
			}

			git := mockbinary.New(t)
			git.Add("git", `[ -z "${SSH_SIGNING_KEY-}" ] || exit 98`)
			git.OnPath()

			unrelated := filepath.Join(root, "unrelated-key")
			require.NoError(t, os.WriteFile(unrelated, []byte("keep"), 0o600))

			flags := []string{}
			if tc.flag {
				flags = append(flags, "--key-path", filepath.Join(root, "flag", "key with spaces"))
			}

			require.NoError(t, sshSetupCmd().Run(t.Context(), append([]string{"setup"}, flags...)))

			want := filepath.Join(root, tc.want)
			body, err := os.ReadFile(want)
			require.NoError(t, err)
			require.Equal(t, "synthetic-signing-key\n", string(body))

			info, err := os.Stat(want)
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

			parent, err := os.Stat(filepath.Dir(want))
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o700), parent.Mode().Perm())

			invocations := git.Invocations("git")
			require.Len(t, invocations, 2)
			require.Equal(t, []string{"config", "gpg.format", "ssh"}, invocations[0].Args)
			require.Equal(t, []string{"config", "user.signingkey", want}, invocations[1].Args)

			for range 2 {
				require.NoError(t, sshCleanupCmd().Run(t.Context(), append([]string{"cleanup"}, flags...)))
			}

			_, err = os.Stat(want)
			require.ErrorIs(t, err, os.ErrNotExist)
			body, err = os.ReadFile(unrelated)
			require.NoError(t, err)
			require.Equal(t, "keep", string(body))
			require.Len(t, git.Invocations("git"), 2)

			_, err = os.Stat(filepath.Join(env.Temp, "forge-ci-ssh-signing-key"))
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestSSHCommands_RefuseBlankCITempSelection(t *testing.T) {
	for _, makeCommand := range []func() *cli.Command{sshSetupCmd, sshCleanupCmd} {
		command := makeCommand()
		t.Run(command.Name, func(t *testing.T) {
			env := testenv.New(t)
			t.Chdir(t.TempDir())
			t.Setenv("PATH", t.TempDir())
			t.Setenv("SSH_SIGNING_KEY", "synthetic-signing-key")
			t.Setenv("CI_TEMP_DIR", " \t")
			t.Setenv("RUNNER_TEMP", filepath.Join(env.Temp, "runner"))
			err := command.Run(t.Context(), []string{command.Name})
			require.ErrorIs(t, err, errs.ErrUsage)
			_, err = os.Stat(filepath.Join(env.Temp, "forge-ci-ssh-signing-key"))
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}
