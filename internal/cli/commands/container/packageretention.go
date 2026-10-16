// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/github"
	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/cli/regflags"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

const (
	flagRetentionRepository = "repository"
	flagRetentionPrefix     = "repository-prefix"
	flagKeepTags            = "keep-tags"
	flagRecentTags          = "recent-tags"
	flagRecentCount         = "recent-count"
	flagPackagesTokenFile   = "token-file"

	// defaultKeepTags keeps release and pre-release tags such as v1.2.3,
	// v1.2, v1 and v1.0.0-rc.1, and a rolling tag such as v0.90.0-pre; a
	// stamped tag such as v0.90.0-pre-20261015 is left to --recent-tags.
	defaultKeepTags   = `^v[0-9]+(\.[0-9]+){0,2}(-[0-9A-Za-z.]+)?$`
	defaultRecentTags = `^sha-[0-9a-f]+$`
)

func packageRetentionCmd() *cli.Command {
	return &cli.Command{
		Name:  "package-retention",
		Usage: "delete GHCR package versions that no kept tag needs",
		Description: `Retention for container packages on GHCR, which deletes nothing through
the registry API: versions are removed through the GitHub Packages API, and a
version is one manifest with all its tags.

A version is kept when it holds a --keep-tags tag, when it is one of the newest
--recent-count versions holding a --recent-tags tag, or when a kept version
lists it (an index's platform manifests) or it refers to a kept version (a
Cosign signature, attestation or SBOM tag, or an OCI referrers index). The rest
is deleted, untagged leftovers of interrupted pushes included. A manifest that
cannot be read stops the pass.

The token is $GITHUB_TOKEN with packages: write for packages linked to the
repository, or a classic token with read:packages and delete:packages.

--dry-run is the default: run it, read the list, then pass --dry-run=false.

EXAMPLE:
   forge-ci container package-retention \
     --repository-prefix ghcr.io/acme/forge-ci-runtime --recent-count 10`,
		Flags: []cli.Flag{
			&cli.StringSliceFlag{Name: flagRetentionRepository, Usage: "package to prune, as ghcr.io/<owner>/<name>; repeatable"},
			&cli.StringFlag{Name: flagRetentionPrefix, Sources: cli.EnvVars("PACKAGE_RETENTION_PREFIX"), Usage: "prune every container package of the owner whose name starts with this, given as ghcr.io/<owner>/<name-prefix>"},
			&cli.StringFlag{Name: flagKeepTags, Value: defaultKeepTags, Sources: cli.EnvVars("PACKAGE_RETENTION_KEEP_TAGS"), Usage: "regular expression of tags kept whatever their age"},
			&cli.StringFlag{Name: flagRecentTags, Value: defaultRecentTags, Sources: cli.EnvVars("PACKAGE_RETENTION_RECENT_TAGS"), Usage: "regular expression of tags of which the newest --recent-count versions are kept"},
			&cli.IntFlag{Name: flagRecentCount, Value: 10, Sources: cli.EnvVars("PACKAGE_RETENTION_RECENT_COUNT"), Usage: "how many of the newest --recent-tags versions to keep"},
			&cli.IntFlag{Name: flagMaxDelete, Value: 100, Sources: cli.EnvVars("PACKAGE_RETENTION_MAX_DELETE"), Usage: "refuse a package that would lose more versions than this (0 disables the bound)"},
			&cli.BoolFlag{Name: flagDryRun, Value: true, Sources: cli.EnvVars("PACKAGE_RETENTION_DRY_RUN"), Usage: "report what would be deleted without deleting it"},
			&cli.StringFlag{Name: flagPackagesTokenFile, Usage: "file holding the GitHub token (default: $GH_TOKEN, then $GITHUB_TOKEN)"},
			regflags.UsernameFor("the user the token logs in to ghcr.io as, to read manifests"),
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			rule, err := retentionRuleFromCmd(cmd)
			if err != nil {
				return err
			}

			username := strings.TrimSpace(cmd.String(regflags.FlagUsername))
			if username == "" {
				return fmt.Errorf("package retention: name the user the token logs in to ghcr.io as with --%s or $%s: %w",
					regflags.FlagUsername, regflags.EnvUser, errs.ErrUsage)
			}

			token, err := regflags.FileOrEnv(cmd.String(flagPackagesTokenFile), "GH_TOKEN", "GITHUB_TOKEN")
			if err != nil {
				return err
			}

			packages, err := github.NewPackages(nil, token)
			if err != nil {
				return err
			}

			repositories, err := retentionRepositories(ctx, cmd, packages)
			if err != nil {
				return err
			}

			return withBaseImagesDockerConfig(func(authFile string) error {
				if err := appcontainer.RegistryLogin(os.Stderr, appcontainer.RegistryLoginInput{
					Registry: github.GHCRHost, Username: cmd.String(regflags.FlagUsername), Password: token, AuthFile: authFile,
				}); err != nil {
					return err
				}

				manifests := ociregistry.WithAuthFile(authFile)

				for _, repository := range repositories {
					if _, err := appcontainer.ApplyPackageRetention(ctx, packages, manifests, os.Stdout, appcontainer.PackageRetentionInput{
						Repository: repository,
						Rule:       rule,
						MaxDelete:  cmd.Int(flagMaxDelete),
						DryRun:     cmd.Bool(flagDryRun),
					}); err != nil {
						return err
					}
				}

				return nil
			})
		},
	}
}

func retentionRuleFromCmd(cmd *cli.Command) (domaincontainer.RetentionRule, error) {
	rule := domaincontainer.RetentionRule{RecentCount: cmd.Int(flagRecentCount)}

	for flag, target := range map[string]**regexp.Regexp{flagKeepTags: &rule.Keep, flagRecentTags: &rule.Recent} {
		pattern := strings.TrimSpace(cmd.String(flag))
		if pattern == "" {
			continue
		}

		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return rule, fmt.Errorf("package retention: --%s: %w: %w", flag, err, errs.ErrUsage)
		}

		*target = compiled
	}

	return rule, rule.Validate()
}

// retentionRepositories resolves the packages to prune: the named ones and
// every package matching the prefix, all on GHCR.
func retentionRepositories(ctx context.Context, cmd *cli.Command, packages *github.Packages) ([]string, error) {
	repositories := cmd.StringSlice(flagRetentionRepository)

	if prefix := strings.TrimSpace(cmd.String(flagRetentionPrefix)); prefix != "" {
		owner, namePrefix, err := ghcrOwnerAndName(prefix)
		if err != nil {
			return nil, err
		}

		names, err := packages.ContainerPackages(ctx, owner, namePrefix)
		if err != nil {
			return nil, err
		}

		if len(names) == 0 {
			return nil, fmt.Errorf("package retention: %s matches no container package: %w", prefix, errs.ErrMissingInput)
		}

		for _, name := range names {
			repositories = append(repositories, github.GHCRHost+"/"+owner+"/"+name)
		}
	}

	if len(repositories) == 0 {
		return nil, fmt.Errorf("package retention: name --%s or --%s: %w", flagRetentionRepository, flagRetentionPrefix, errs.ErrUsage)
	}

	for _, repository := range repositories {
		if _, _, err := ghcrOwnerAndName(repository); err != nil {
			return nil, err
		}
	}

	return repositories, nil
}

func ghcrOwnerAndName(repository string) (string, string, error) {
	host, path, _ := strings.Cut(strings.TrimSpace(repository), "/")
	owner, name, _ := strings.Cut(path, "/")

	if host != github.GHCRHost || owner == "" || name == "" {
		return "", "", fmt.Errorf("package retention: %q is not %s/<owner>/<name>; only GHCR is managed through the GitHub Packages API: %w",
			repository, github.GHCRHost, errs.ErrUsage)
	}

	return owner, name, nil
}
