// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/git"
	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/cli/secret"
	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/runcontext"
)

// sshGroup wires `forge-ci release ssh <verb>` — the SSH sibling of the
// gpg group. setup configures git to sign commits/tags with an SSH key (no
// long-lived OpenPGP key on the runner); cleanup removes the key file.
//
// Both verbs require the same explicit key path or CI temporary directory.
// With that destination selected, cleanup is idempotent under `if: always()`.
func sshGroup() *cli.Command {
	return &cli.Command{
		Name:  "ssh",
		Usage: "configure SSH-based git commit/tag signing (setup / cleanup)",
		Description: `Use the same job-specific destination for setup and cleanup. --key-path or
$SSH_SIGNING_KEY_PATH selects it explicitly; otherwise $CI_TEMP_DIR, then
$RUNNER_TEMP supplies the directory. Without either selection, both commands
refuse rather than using a shared OS temporary pathname.`,
		Commands: []*cli.Command{
			sshSetupCmd(),
			sshCleanupCmd(),
		},
	}
}

func sshSetupCmd() *cli.Command {
	return &cli.Command{
		Name:  commandSetup,
		Usage: "write the SSH signing key and configure git (gpg.format=ssh, user.signingkey)",
		Description: `EXAMPLE:
   # Key from a file (or stdin via "-"); defaults to $SSH_SIGNING_KEY
   forge-ci release ssh setup --private-key-file id_ed25519 --key-path /private/job/signing-key --git-commit-sign`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "private-key-file",
				Usage: "path to the OpenSSH private signing key (use \"-\" for stdin; defaults to $SSH_SIGNING_KEY)",
			},
			&cli.StringFlag{
				Name:    "key-path",
				Sources: cli.EnvVars("SSH_SIGNING_KEY_PATH"),
				Usage:   "destination key file; defaults to forge-ci-ssh-signing-key under $CI_TEMP_DIR, then $RUNNER_TEMP; required without either directory",
			},
			&cli.StringFlag{Name: "author-name", Sources: cli.EnvVars("COMMIT_AUTHOR_NAME"),
				Usage: "write user.name"},
			&cli.StringFlag{Name: "author-email", Sources: cli.EnvVars("COMMIT_AUTHOR_EMAIL"),
				Usage: "write user.email"},
			&cli.BoolFlag{Name: "git-commit-sign", Sources: cli.EnvVars("GIT_COMMIT_GPGSIGN"),
				Usage: "additionally write commit.gpgsign=true"},
			&cli.BoolFlag{Name: "git-config-global", Sources: cli.EnvVars("FORGE_CI_GIT_CONFIG_GLOBAL"),
				Usage: "use --global on the git config writes (env $FORGE_CI_GIT_CONFIG_GLOBAL — NOT git's reserved $GIT_CONFIG_GLOBAL, which is a path)"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			keyPath, err := resolveSSHKeyPath(cmd.String("key-path"))
			if err != nil {
				return err
			}

			privateKey, err := secret.Resolve(cmd.String("private-key-file"), "SSH_SIGNING_KEY")
			if err != nil {
				return err
			}

			if privateKey == "" {
				return errs.CredentialRequired(errs.Credential{What: "SSH signing key", Flag: "private-key-file", Env: "SSH_SIGNING_KEY"})
			}

			// The key now lives in this process only; git config children must
			// not inherit it.
			_ = os.Unsetenv("SSH_SIGNING_KEY")

			writeKey := func(key []byte) (string, error) {
				if mkErr := os.MkdirAll(filepath.Dir(keyPath), 0o700); mkErr != nil {
					return "", fmt.Errorf("mkdir for ssh signing key: %w", mkErr)
				}

				if wErr := cliio.WriteFile(keyPath, key, 0o600); wErr != nil {
					return "", fmt.Errorf("write ssh signing key: %w", wErr)
				}

				return keyPath, nil
			}

			return apprelease.SSHSigningSetup(ctx, git.New(), writeKey, apprelease.SSHSigningInput{
				PrivateKey:      privateKey,
				AuthorName:      cmd.String("author-name"),
				AuthorEmail:     cmd.String("author-email"),
				GitCommitSign:   cmd.Bool("git-commit-sign"),
				GitConfigGlobal: cmd.Bool("git-config-global"),
			}, os.Stderr)
		},
	}
}

func sshCleanupCmd() *cli.Command {
	return &cli.Command{
		Name:  commandCleanup,
		Usage: "remove the selected SSH signing key file (a missing file is success)",
		Description: `EXAMPLE:
   forge-ci release ssh cleanup --key-path /private/job/signing-key`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "key-path",
				Sources: cli.EnvVars("SSH_SIGNING_KEY_PATH"),
				Usage:   "key file to remove; defaults to forge-ci-ssh-signing-key under $CI_TEMP_DIR, then $RUNNER_TEMP; required without either directory",
			},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			keyPath, err := resolveSSHKeyPath(cmd.String("key-path"))
			if err != nil {
				return err
			}

			// Idempotent: a missing key (setup never ran, or already cleaned)
			// is success, so this is safe under if: always().
			if err := os.Remove(keyPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("remove ssh signing key: %w", err)
			}

			return nil
		},
	}
}

// resolveSSHKeyPath binds setup and cleanup to an operator-selected path or CI
// scratch directory. TempRoot's OS fallback is unsuitable for this fixed secret
// filename: independent invocations could overwrite or remove each other's key.
func resolveSSHKeyPath(override string) (string, error) {
	// cliio treats "-" as stdout. Reject it for both verbs before reading a key
	// or deleting an unrelated file whose literal name happens to be "-".
	if override == cliio.StdSentinel {
		return "", fmt.Errorf("ssh signing key path cannot be stdout: %w", errs.ErrUsage)
	}

	if override != "" {
		return override, nil
	}

	base := runcontext.TempDir().Resolve(os.Getenv)
	if strings.TrimSpace(base) == "" {
		return "", fmt.Errorf("ssh signing requires --key-path, $SSH_SIGNING_KEY_PATH, $CI_TEMP_DIR or $RUNNER_TEMP: %w", errs.ErrUsage)
	}

	return filepath.Join(base, "forge-ci-ssh-signing-key"), nil
}
