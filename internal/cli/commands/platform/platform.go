// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package platform wires `forge-ci platform <subcmd>` — git + workspace
// operations against the CI runtime (GitHub Actions / GitLab CI / local) the
// binary is executing inside: check out a repository, resolve a ref to a SHA,
// and inspect the workspace.
package platform

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/git"
	appplatform "github.com/diggsweden/forge-ci/internal/app/platform"
	"github.com/diggsweden/forge-ci/internal/cli/cienv"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// Flag names shared by the platform commands and their tests. goconst counts a
// literal across the whole package, test files included.
const (
	commandCheckout = "checkout"

	flagServerURL  = "server-url"
	flagRepository = "repository"
	flagRef        = "ref"
	flagOutputKey  = "output-key"
	flagFetchBase  = "fetch-base"
	flagSparse     = "sparse"
	flagPath       = "path"
	flagWorkspace  = "workspace"

	// outputKeySHA256 is the default output key for a published digest.
	outputKeySHA256 = "sha256"
)

// New returns the `platform` subgroup command tree.
func New() *cli.Command {
	return &cli.Command{
		Name:  "platform",
		Usage: "CI-runtime git + workspace operations (check out a repo, resolve a ref, inspect the workspace)",
		Commands: []*cli.Command{
			debugWorkspaceCmd(),
			resolveRefCmd(),
			checkoutCmd(),
			stagePublicKeyCmd(),
		},
	}
}

// stagePublicKeyCmd wires `forge-ci platform stage-public-key`: copy a
// reviewed public key into the workspace for a later verification step, under
// the digest its companion file records.
//
// It stages a trust anchor; it does not establish trust. The sidecar detects
// drift inside a reviewed checkout, and which key is trusted stays the
// caller's policy, as it is for `validate signing-key`.
func stagePublicKeyCmd() *cli.Command {
	return &cli.Command{
		Name:  "stage-public-key",
		Usage: "copy a reviewed public key into the workspace at its recorded digest",
		Description: `EXAMPLE:
   forge-ci platform stage-public-key --source keys/cosign.pub      --digest-file keys/cosign.pub.sha256 --destination .trust/cosign.pub`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "source", Required: true, Usage: "reviewed public key file, read without following a symlink"},
			&cli.StringFlag{Name: "digest-file", Required: true, Usage: "sha256sum-format sidecar recording the source key's digest"},
			&cli.StringFlag{Name: "destination", Required: true, Usage: "workspace-relative path to write the key to"},
			&cli.StringFlag{Name: flagWorkspace, Sources: cienv.Workspace(), Usage: "workspace root the destination is confined to (default: the working directory)"},
			&cli.StringFlag{Name: flagOutputKey, Value: outputKeySHA256, Sources: cli.EnvVars("OUTPUT_KEY"), Usage: "key written to the platform output sink"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return deps.FromCmd(ctx, cmd, func(d *deps.Deps) error {
				return appplatform.StagePublicKey(ctx, d.OutputSink, os.Stderr, appplatform.StagePublicKeyInput{
					Source:      cmd.String("source"),
					DigestFile:  cmd.String("digest-file"),
					Destination: cmd.String("destination"),
					Workspace:   cmd.String(flagWorkspace),
					OutputKey:   cmd.String(flagOutputKey),
				})
			})
		},
	}
}

func resolveRefCmd() *cli.Command {
	return &cli.Command{
		Name:  "resolve-ref",
		Usage: "resolve a remote git ref to a commit SHA output",
		Description: `EXAMPLE:
   forge-ci platform resolve-ref --remote-url https://github.com/org/app --ref v1.2.3`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "remote-url", Sources: cli.EnvVars("REMOTE_URL"), Usage: "remote git URL queried with 'git ls-remote' (default: this repository, derived from --server-url + --repository)"},
			&cli.StringFlag{Name: flagServerURL, Sources: cienv.ServerURL(), Usage: "forge base URL used to derive --remote-url when it is unset"},
			&cli.StringFlag{Name: flagRepository, Sources: cienv.Repository(), Usage: `"owner/repo" used to derive --remote-url when it is unset`},
			&cli.StringFlag{Name: flagRef, Sources: cienv.Ref(), Usage: "ref to resolve (tag, branch, or full refs/X/Y)"},
			&cli.StringFlag{Name: flagOutputKey, Value: "sha", Sources: cli.EnvVars("OUTPUT_KEY"), Usage: "key written to the platform output sink"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			remoteURL, err := resolveRemoteURL(cmd.String("remote-url"), cmd.String(flagServerURL), cmd.String(flagRepository))
			if err != nil {
				return err
			}

			return deps.FromCmd(ctx, cmd, func(d *deps.Deps) error {
				_, err := appplatform.ResolveRef(ctx, git.New(), d.OutputSink, os.Stderr, appplatform.ResolveRefInput{
					RemoteURL: remoteURL,
					Ref:       cmd.String(flagRef),
					OutputKey: cmd.String(flagOutputKey),
				})

				return err
			})
		},
	}
}

// resolveRemoteURL returns the explicit --remote-url, or derives it from the
// run context (server-url + repository) so `resolve-ref` targets the current
// repository on any forge without a baked-in org default.
func resolveRemoteURL(remoteURL, serverURL, repository string) (string, error) {
	if u := strings.TrimSpace(remoteURL); u != "" {
		return u, nil
	}

	server := strings.TrimRight(strings.TrimSpace(serverURL), "/")
	repo := strings.Trim(strings.TrimSpace(repository), "/")

	if server != "" && repo != "" {
		return server + "/" + repo, nil
	}

	return "", fmt.Errorf("platform resolve-ref: --remote-url is required (or provide --server-url and --repository): %w", errs.ErrUsage)
}

func debugWorkspaceCmd() *cli.Command {
	return &cli.Command{
		Name:  "debug-workspace",
		Usage: "print workspace listing + .github-shared listing + GitHub action context",
		Description: `EXAMPLE:
   # Print the workspace + action context for debugging (takes no required flags)
   forge-ci platform debug-workspace`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "action-repository", Sources: cli.EnvVars("ACTION_REPOSITORY"), Usage: "the calling action's repository (printed in the debug block)"},
			&cli.StringFlag{Name: "action-ref", Sources: cli.EnvVars("ACTION_REF"), Usage: "the calling action's ref (printed in the debug block)"},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			return appplatform.DebugWorkspace(os.Stderr, appplatform.DebugWorkspaceInput{
				ActionRepository: cmd.String("action-repository"),
				ActionRef:        cmd.String("action-ref"),
			})
		},
	}
}
