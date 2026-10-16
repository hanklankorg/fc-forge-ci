// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/git"
	appversion "github.com/diggsweden/forge-ci/internal/app/version"
	"github.com/diggsweden/forge-ci/internal/cli/cienv"
	"github.com/diggsweden/forge-ci/internal/cli/clitoken"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/cli/dryrun"
	"github.com/diggsweden/forge-ci/internal/cli/secret"
	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func commitChangelogReleaseCmd() *cli.Command {
	return &cli.Command{
		Name:  "commit-changelog-release",
		Usage: "SSH-sign a pre-rendered changelog commit, push main without force, create the final release tag once",
		Description: `EXAMPLE:
   forge-ci version commit-changelog-release --tag v1.2.3 --repository owner/repo --git-url ssh://git@git.example/owner/repo.git`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: flagTag, Required: true, Sources: cli.EnvVars("RELEASE_TAG", "TAG_NAME"), Usage: "final stable release tag to create (vMAJOR.MINOR.PATCH)"},
			&cli.StringFlag{Name: "repository", Required: true, Sources: cienv.Repository(), Usage: "repository in namespace/project form, including GitLab subgroups"},
			&cli.StringFlag{Name: flagBranch, Value: "main", Usage: "branch to push the signed changelog commit to"},
			&cli.StringFlag{Name: "changelog", Value: defaultChangelogFile, Sources: cli.EnvVars("CHANGELOG_PATH"), Usage: "pre-generated changelog file to commit"},
			&cli.StringFlag{Name: "commit-message-file", Value: defaultCommitMessageFile, Sources: cli.EnvVars("COMMIT_MESSAGE_FILE"), Usage: "pre-generated commit message file used with git commit -F"},
			&cli.StringFlag{Name: "include-paths-file", Usage: "file naming further repository-relative files to commit beside the changelog, one per line (apply-bump --paths-file)"},
			&cli.StringFlag{Name: "commit-body-path", Value: defaultCommitBodyFile, Sources: cli.EnvVars("COMMIT_BODY_PATH"), Usage: "rendered commit body removed after the release commit"},
			&cli.StringFlag{Name: "author-name", Sources: cli.EnvVars("GIT_USER_NAME", "COMMIT_AUTHOR_NAME"), Usage: "git user.name for the release bump commit (required; no org default)"},
			&cli.StringFlag{Name: "author-email", Sources: cli.EnvVars("GIT_USER_EMAIL", "COMMIT_AUTHOR_EMAIL"), Usage: "git user.email for the release bump commit (required; no org default)"},
			&cli.StringFlag{Name: "private-key-file", Usage: "path to the OpenSSH private signing key (use '-' for stdin; defaults to $SSH_SIGNING_KEY)"},
			&cli.StringFlag{Name: "git-url", Required: true, Sources: cli.EnvVars("RELEASE_GIT_URL"), Usage: "SSH origin URL in ssh://git@host[:port]/owner/repository.git form"},
			&cli.StringFlag{Name: "host-key-type", Value: defaultHostKeyType, Sources: cli.EnvVars("RELEASE_GIT_HOST_KEY_TYPE"), Usage: "host key type passed to ssh-keyscan"},
			&cli.StringFlag{Name: "host-key-fingerprint", Sources: cli.EnvVars("RELEASE_GIT_HOST_KEY_FINGERPRINT"), Usage: "expected SSH host key fingerprint, required (a trust anchor, never defaulted; get it with: ssh-keyscan [-p <port>] -t <type> <host> | ssh-keygen -lf -)"},
			&cli.BoolFlag{Name: "no-sign", Usage: "skip final tag signing (intended for tests; production always signs)"},
			&cli.BoolFlag{Name: "signed", Value: true, Sources: cli.EnvVars("TAG_RELEASE_SIGNED"), Usage: "create a signed final tag; set TAG_RELEASE_SIGNED=false for unsigned annotated test tags"},
			clitoken.FileFlag("Optional HTTP token; environment selection prefers $RELEASE_TOKEN. SSH remotes use the signing key."),
			dryrun.Flag("git mutations (changelog commit, push, release tag, checkout)"),
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			in, err := changelogReleaseInput(cmd)
			if err != nil {
				return err
			}

			return deps.FromCmd(ctx, cmd, func(d *deps.Deps) error { //nolint:varnamelen // idiomatic short name (testing/http/io conventions).
				repo := git.New()

				gitEndpoint, err := appversion.ParseChangelogReleaseGitURL(in.GitURL, in.Repository)
				if err != nil {
					return err
				}

				if in.DryRun {
					// The SSH signing key, pinned host key, and GIT_SSH_COMMAND
					// only serve the commit/push that dry-run skips — set none
					// of it up (and leave the commit-message files in place for
					// the real run).
					_, _ = fmt.Fprintln(os.Stderr, "[dry-run] skipping SSH signing key and host-key setup (commit and push are skipped)")

					_, releaseErr := appversion.ChangelogRelease(ctx, repo, d.OutputSink, os.Stderr, in)

					return releaseErr
				}

				privateKey, err := secret.Resolve(cmd.String("private-key-file"), "SSH_SIGNING_KEY")
				if err != nil {
					return err
				}

				if privateKey == "" {
					return errs.CredentialRequired(errs.Credential{What: "SSH signing key", Flag: "private-key-file", Env: "SSH_SIGNING_KEY"})
				}

				_ = os.Unsetenv("SSH_SIGNING_KEY")

				// The host key fingerprint is a trust anchor: the engine ships
				// no default, so require it explicitly before pinning the host.
				fingerprint := cmd.String("host-key-fingerprint")
				if fingerprint == "" {
					return fmt.Errorf("commit-changelog: --host-key-fingerprint is required (a trust anchor; get it with: %s | ssh-keygen -lf -): %w",
						strings.Join(git.HostKeyScanArgs(gitEndpoint.Host, gitEndpoint.Port, cmd.String("host-key-type")), " "), errs.ErrUsage)
				}

				ssh, cleanup, err := git.SetupPinnedSSH(ctx, git.PinnedSSHInput{
					PrivateKey:         privateKey,
					Host:               gitEndpoint.Host,
					Port:               gitEndpoint.Port,
					HostKeyType:        cmd.String("host-key-type"),
					HostKeyFingerprint: fingerprint,
				})
				if err != nil {
					return err
				}
				defer cleanup()
				defer cleanupCommitMessageFiles(cmd.String("commit-body-path"), cmd.String("commit-message-file"))

				restoreSSHCommand, err := cienv.SetTemporarily("GIT_SSH_COMMAND", ssh.Command)
				if err != nil {
					return err
				}
				defer restoreSSHCommand()

				in.SigningKeyPath = ssh.SigningKeyPath
				_, err = appversion.ChangelogRelease(ctx, repo, d.OutputSink, os.Stderr, in)

				return err
			})
		},
	}
}

func changelogReleaseInput(cmd *cli.Command) (appversion.ChangelogReleaseInput, error) {
	if cmd.String("private-key-file") == cliio.StdSentinel && cmd.String("token-file") == cliio.StdSentinel {
		return appversion.ChangelogReleaseInput{}, fmt.Errorf("--private-key-file and --token-file cannot both read stdin: %w", errs.ErrUsage)
	}

	if _, err := deps.ParseRepository(cmd.String("repository")); err != nil {
		return appversion.ChangelogReleaseInput{}, err
	}

	token, err := clitoken.ResolveRelease(cmd)
	if err != nil {
		return appversion.ChangelogReleaseInput{}, err
	}

	var includePaths []string

	if file := cmd.String("include-paths-file"); file != "" {
		body, err := cliio.ReadRegularFile(file)
		if err != nil {
			return appversion.ChangelogReleaseInput{}, fmt.Errorf("read --include-paths-file: %w", err)
		}

		// One path per line, exactly as apply-bump writes them; a path is
		// never trimmed, so it reaches the validation as written.
		includePaths = strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	}

	in := appversion.ChangelogReleaseInput{
		Tag:               cmd.String(flagTag),
		Repository:        cmd.String("repository"),
		GitURL:            cmd.String("git-url"),
		Branch:            cmd.String(flagBranch),
		ChangelogPath:     cmd.String("changelog"),
		CommitMessageFile: cmd.String("commit-message-file"),
		IncludePaths:      includePaths,
		AuthorName:        cmd.String("author-name"),
		AuthorEmail:       cmd.String("author-email"),
		TagSigned:         cmd.Bool("signed") && !cmd.Bool("no-sign"),
		Token:             token,
		DryRun:            dryrun.Enabled(cmd),
	}

	return in, appversion.ChangelogReleasePreflight(in)
}

// defaultHostKeyType is the SSH host key type scanned and pinned unless the
// caller names another.
const defaultHostKeyType = "ed25519"

// cleanupCommitMessageFiles removes the rendered commit body and message.
// A file that is already gone is fine; any other failure is reported so a
// leftover release file does not go unnoticed.
func cleanupCommitMessageFiles(paths ...string) {
	for _, path := range paths {
		if path == "" {
			continue
		}

		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			_, _ = fmt.Fprintf(os.Stderr, "warning: remove %s: %v\n", path, err)
		}
	}
}
