// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package platform

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/git"
	appplatform "github.com/diggsweden/forge-ci/internal/app/platform"
	"github.com/diggsweden/forge-ci/internal/cli/cienv"
	"github.com/diggsweden/forge-ci/internal/cli/clitoken"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
	"github.com/diggsweden/forge-ci/internal/runcontext"
)

func checkoutCmd() *cli.Command {
	return checkoutCommand(checkoutDependencies, func(workspace string) appplatform.CheckoutGit {
		return &git.Repo{Dir: workspace}
	})
}

// Keep the command action testable without constructing providers or running Git.
func checkoutCommand(withDeps func(context.Context, *cli.Command, appplatform.CheckoutInput, func(*deps.Deps) error) error, newGit func(string) appplatform.CheckoutGit) *cli.Command {
	return &cli.Command{
		Name:  commandCheckout,
		Usage: "exact, credential-free, sha256-aware checkout of a repository into the workspace",
		Description: "Git-based checkout that works in minimal containers and for sha256 repositories. " +
			"Resolves the object format from the forge, " +
			"materializes the workspace at the given ref, and writes the resolved commit SHA to the output sink. " +
			"The token (when set) is sent as transient HTTP Basic auth and never persisted to .git/config.\n\n" +
			"EXAMPLES:\n" +
			"   # Check out a tag of a repo into the current directory\n" +
			"   forge-ci platform checkout --repository org/app --ref v1.2.3\n\n" +
			"   # Shallow checkout into a subdirectory\n" +
			"   forge-ci platform checkout --repository org/app --ref main --depth 1 --path app",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: flagRepository, Sources: cienv.Repository(), Usage: "owner/name to check out"},
			&cli.StringFlag{Name: flagServerURL, Sources: cienv.ServerURL(), Usage: "forge base URL (e.g. https://forgejo.example.com)"},
			&cli.StringFlag{Name: flagRef, Sources: cienv.CheckoutRef(), Usage: "commit SHA, refs/tags/…, refs/heads/…, or a bare tag/branch name to check out. Set via $CHECKOUT_REF; defaults to the triggering commit."},
			&cli.StringFlag{Name: flagWorkspace, Sources: cienv.Workspace(), Usage: "target directory (default: current directory)"},
			clitoken.FileFlag("Token for a private checkout."),
			&cli.StringFlag{Name: "object-format", Sources: cli.EnvVars("CHECKOUT_OBJECT_FORMAT"), Usage: "override the forge-reported object format (sha1|sha256); skips the metadata lookup"},
			&cli.StringFlag{Name: flagFetchBase, Sources: cli.EnvVars("CHECKOUT_FETCH_BASE"), Usage: "extra branch to also fetch (diff/commit-range checks)"},
			&cli.BoolFlag{Name: "fetch-tags", Sources: cli.EnvVars("CHECKOUT_FETCH_TAGS"), Usage: "also fetch all tags (the JS actions/checkout fetch-tags:true; needed for git-cliff/changelog)"},
			&cli.BoolFlag{Name: "fetch-all-refs", Sources: cli.EnvVars("CHECKOUT_FETCH_ALL_REFS"), Usage: "fetch every branch into refs/remotes/origin/* plus all tags (the JS actions/checkout fetch-depth:0); needed by builds that read git topology, e.g. `git rev-list --count origin/main` or `git describe`"},                                                  //nolint:lll // single-line flag declaration for grep-ability, matching the package convention.
			&cli.IntFlag{Name: "depth", Sources: cli.EnvVars("CHECKOUT_FETCH_DEPTH"), Usage: "shallow history depth for the checked-out ref (git --depth); 0 = full history (the JS actions/checkout fetch-depth, where 0 means all). Use 1 to speed up build/scan jobs that only need the tree. Orthogonal to --fetch-all-refs, which always brings full topology."}, //nolint:lll // single-line flag declaration for grep-ability, matching the package convention.
			&cli.StringSliceFlag{Name: flagSparse, Sources: cli.EnvVars("CHECKOUT_SPARSE"), Usage: "cone-mode sparse-checkout dir(s), e.g. scripts/bootstrap; restricts the working tree to these subtrees (repeatable / comma-separated)"},
			&cli.StringFlag{Name: flagPath, Sources: cli.EnvVars("CHECKOUT_PATH"), Usage: "subdirectory to check out into, relative to $GITHUB_WORKSPACE (the JS actions/checkout path:); alternative to --workspace"},
			&cli.StringFlag{Name: flagOutputKey, Value: "checkout-sha", Sources: cli.EnvVars("OUTPUT_KEY"), Usage: "key written to the output sink for the resolved SHA"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if _, err := deps.ParseRepository(cmd.String(flagRepository)); err != nil {
				return err
			}

			workspace, err := resolveWorkspace(cmd.String(flagWorkspace), cmd.String(flagPath))
			if err != nil {
				return err
			}

			in := appplatform.CheckoutInput{
				Repository:   cmd.String(flagRepository),
				ServerURL:    cmd.String(flagServerURL),
				Ref:          cmd.String(flagRef),
				Workspace:    workspace,
				ObjectFormat: cmd.String("object-format"),
				FetchBase:    cmd.String(flagFetchBase),
				FetchTags:    cmd.Bool("fetch-tags"),
				FetchAllRefs: cmd.Bool("fetch-all-refs"),
				Depth:        cmd.Int("depth"),
				Sparse:       cmd.StringSlice(flagSparse),
				OutputKey:    cmd.String(flagOutputKey),
			}
			if err = appplatform.PreflightCheckout(git.CheckoutDestinations{}, in); err != nil {
				return err
			}

			in.Token, err = clitoken.Resolve(cmd)
			if err != nil {
				return err
			}

			return withDeps(ctx, cmd, in, func(dep *deps.Deps) error {
				format, err := resolveObjectFormat(ctx, dep, cmd)
				if err != nil {
					return err
				}

				in.ObjectFormat = format
				_, err = appplatform.Checkout(ctx, git.CheckoutDestinations{}, newGit, dep.OutputSink, os.Stderr, in)

				return withGitCredentialHint(err, in.Token, os.Getenv)
			})
		},
	}
}

// withGitCredentialHint names the one cause Git's own message cannot: an API
// token was configured, and Git still asked anonymously. $GITLAB_TOKEN reaches
// the GitLab API only, so a private clone that relies on it fails with Git's
// "could not read Username".
func withGitCredentialHint(err error, token runcontext.Credential, getenv func(string) string) error {
	if err == nil || token.Present() || getenv("GITLAB_TOKEN") == "" {
		return err
	}

	return fmt.Errorf("%w; Git ran without a credential, because $GITLAB_TOKEN authenticates the GitLab API only: pass --token-file for a private repository", err)
}

func checkoutDependencies(ctx context.Context, cmd *cli.Command, in appplatform.CheckoutInput, action func(*deps.Deps) error) error {
	return deps.FromCmd(ctx, cmd, func(dep *deps.Deps) error {
		if in.ObjectFormat == "" {
			bound, err := deps.CheckoutProvider(in.ServerURL, in.Token, cmd.IsSet("token-file"))
			if err != nil {
				return err
			}

			dep.Provider = bound
		}

		return action(dep)
	})
}

// resolveWorkspace picks the checkout target directory. --path (the JS
// actions/checkout path:) wins and is resolved relative to the runner
// workspace — Forgejo's native $FORGEJO_WORKSPACE preferred over the
// $GITHUB_WORKSPACE alias (or cwd when neither is set), kept absolute so the
// app's safe.directory guard stays correct. Otherwise an explicit
// --workspace, else the current directory.
func resolveWorkspace(workspace, path string) (string, error) {
	if path != "" {
		if !pathsafe.Relative(path) {
			return "", fmt.Errorf("checkout path must stay inside the runner workspace: %w", errs.ErrUsage)
		}

		base := runcontext.Workspace().Resolve(os.Getenv)
		if base == "" {
			cwd, err := os.Getwd()
			if err != nil {
				return "", err
			}

			base = cwd
		}

		return filepath.Join(base, path), nil
	}

	if workspace != "" {
		return workspace, nil
	}

	return os.Getwd()
}

// resolveObjectFormat applies the override-or-forge policy: an explicit
// --object-format wins (and skips the network entirely); otherwise the
// provider's repo metadata supplies it. An empty result is fine — the app
// defaults it to sha1.
func resolveObjectFormat(ctx context.Context, dep *deps.Deps, cmd *cli.Command) (string, error) {
	if override := cmd.String("object-format"); override != "" {
		return override, nil
	}

	repo := cmd.String(flagRepository)
	if repo == "" {
		return "", nil
	}

	md, err := dep.RepoMetadataFetcher().FetchRepoMetadata(ctx, repo)
	if err != nil {
		return "", err
	}

	return md.ObjectFormat, nil
}
