// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/cosign"
	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/cli/cienv"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/cli/dryrun"
	"github.com/diggsweden/forge-ci/internal/cli/regflags"
	"github.com/diggsweden/forge-ci/internal/cli/signflags"
	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
	"github.com/diggsweden/forge-ci/internal/runcontext"
)

const (
	releaseImagesDefaultJournal = "image-promotions.jsonl"
	releaseImagesDefaultEnvVar  = "REGISTRY_TOKEN"
	flagProviderTokenFile       = "provider-token-file"
)

type releaseImagesCommon struct {
	DistDir                 string
	Ledger                  string
	LedgerExplicit          bool // named by --ledger or its env source, not defaulted
	ReleaseTag              string
	ServerURL               string
	Registry                string
	Repository              string
	RegistryUsername        string
	RegistryPasswordFile    string
	ExpectedImageRepository string
	// External is the caller's declaration that the images are on a registry
	// other than the forge's own (--external-registry).
	External bool
}

func releaseImagesGroup() *cli.Command {
	return &cli.Command{
		Name:  "release-images",
		Usage: "high-level release-image import, signing, promotion, rollback, and cleanup boundary",
		Description: `Workflow-facing release-image boundary. These commands combine the
digest-first image ledger with short-lived registry auth, signer-safe cosign
environment isolation, provider package-API tag deletion, and a release-only
promotion policy. Lower-level ` + "`container ledger`" + ` verbs remain
available for custom workflows; reusable workflows should use this group.`,
		Commands: []*cli.Command{
			releaseImagesImportCmd(),
			releaseImagesSignCmd(),
			releaseImagesVerifyCmd(),
			releaseImagesPromoteCmd(),
			releaseImagesRollbackCmd(),
			releaseImagesCleanupCmd(),
		},
	}
}

func releaseImagesSignCmd() *cli.Command {
	return &cli.Command{
		Name:  subCmdSign,
		Usage: "sign and attest release images from the confined release-image ledger",
		Flags: append(
			append(
				releaseImagesCommonFlags(),
				releaseImagesAllowMissingLedgerFlag(),
				&cli.StringFlag{Name: flagProvenanceEnvelope, Sources: cli.EnvVars("SLSA_PROVENANCE_ENVELOPE"), Usage: "in-toto statement JSON; defaults to <dist-dir>/" + apprelease.ProvenanceStatementName},
				&cli.BoolFlag{Name: flagRecursive, Sources: cli.EnvVars("LEDGER_SIGN_RECURSIVE"), Usage: "pass --recursive to cosign sign for manifest-list children"},
				&cli.StringFlag{Name: flagExpectedBaseRepository, Sources: cli.EnvVars("LEDGER_SIGN_EXPECTED_BASE_REPOSITORY"), Usage: "exact base image repository allowed for ledger base_ref values (default: <expected-image-repository>-base)"},
				&cli.StringFlag{Name: flagSBOMPathPattern, Sources: cli.EnvVars("LEDGER_SIGN_SBOM_PATH_PATTERN"), Usage: "regular expression every ledger SBOM path must match (default: <dist-dir>/image-sbom*.cyclonedx.json)"},
			),
			signflags.Cosign(signflags.CosignOpts{MethodNote: cosignMethodNoteGPG})...,
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			common, err := releaseImagesCommonFromCmd(cmd)
			if err != nil {
				return err
			}

			if stageErr := requireReleaseStage(cmd.String("stage")); stageErr != nil {
				return stageErr
			}

			entries, present, err := common.admitLedger(cmd, true)
			if err != nil || !present {
				return err
			}

			method, err := domainrelease.ParseSignMethod(cmd.String("method"))
			if err != nil {
				return err
			}

			provenanceEnvelope := cmd.String(flagProvenanceEnvelope)
			if provenanceEnvelope == "" {
				provenanceEnvelope = common.DistDir + "/" + apprelease.ProvenanceStatementName
			}

			expectedBaseRepository := cmd.String(flagExpectedBaseRepository)
			if expectedBaseRepository == "" {
				expectedBaseRepository = common.ExpectedImageRepository + "-base"
			}

			sbomPathPattern := cmd.String(flagSBOMPathPattern)
			if sbomPathPattern == "" {
				sbomPathPattern = "^" + regexp.QuoteMeta(common.DistDir) + `/image-sbom[-A-Za-z0-9_.]*[.]cyclonedx[.]json$`
			}

			return withReleaseImagesDockerConfig(func(authFile string) error {
				if err := common.login(authFile); err != nil {
					return err
				}

				return runLedgerSign(ctx,
					cosign.NewIsolated("COSIGN_KEY", "COSIGN_PASSWORD", "DOCKER_CONFIG"),
					ociregistry.WithAuthFile(authFile),
					appcontainer.SignLedgerImagesInput{
						Entries:                 entries,
						ReleaseTag:              common.ReleaseTag,
						PredicateEnvelopePath:   provenanceEnvelope,
						Method:                  method,
						Recursive:               cmd.Bool(flagRecursive),
						KeyRef:                  cmd.String("key"),
						OIDCIssuer:              cmd.String("oidc-issuer"),
						FulcioURL:               signflags.ReadEndpoints(cmd).FulcioURL,
						RekorURL:                signflags.ReadEndpoints(cmd).RekorURL,
						TrustedRootPath:         signflags.ReadEndpoints(cmd).TrustedRootPath,
						ExpectedImageRepository: common.ExpectedImageRepository,
						ExpectedBaseRepository:  expectedBaseRepository,
						SBOMPathPattern:         sbomPathPattern,
					})
			})
		},
	}
}

func releaseImagesPromoteCmd() *cli.Command {
	return &cli.Command{
		Name:  subCmdPromote,
		Usage: "promote signed release image candidates to their final/moving release tags",
		Flags: append(
			releaseImagesCommonFlags(),
			releaseImagesStateFlags(
				releaseImagesAllowMissingLedgerFlag(),
				dryRunFlag(),
			)...,
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			common, err := releaseImagesCommonFromCmd(cmd)
			if err != nil {
				return err
			}

			if stageErr := requireReleaseStage(cmd.String("stage")); stageErr != nil {
				return stageErr
			}

			entries, present, err := common.admitLedger(cmd, true)
			if err != nil || !present {
				return err
			}

			journal, err := releaseImagesJournal(cmd)
			if err != nil {
				return err
			}

			return withReleaseImagesDockerConfig(func(authFile string) error {
				return releaseImagesPromote(ctx, cmd, common, entries, journal, authFile)
			})
		},
	}
}

// releaseImagesPromote runs the promote verb inside the short-lived Docker
// config: log in, journal the plan, then promote every ledger entry via the
// shared ledger promote body.
func releaseImagesPromote(ctx context.Context, cmd *cli.Command, common releaseImagesCommon, entries []imageledger.Entry, journal, authFile string) error {
	if err := common.login(authFile); err != nil {
		return err
	}

	stage := imageledger.Stage{
		Name:                   imageledger.ReleaseStageName,
		UseEntryReleaseTags:    true,
		AllowDigestRefFallback: true,
	}

	// Release promotion never rehomes registries, so no signature copier.
	reg, sigCopier := promotionRegistries(ociregistry.WithAuthFile(authFile), dryrun.Enabled(cmd), nil)

	return appcontainer.PromoteLedger(ctx, appcontainer.LedgerPromotion{
		Registry:        reg,
		SignatureCopier: sigCopier,
		Entries:         entries,
		ReleaseTag:      common.ReleaseTag,
		Stage:           stage,
		Journal:         journal,
		JournalDirPerm:  0o700,
		DryRun:          dryrun.Enabled(cmd),
		ErrPrefix:       "release images",
		Out:             os.Stderr,
	})
}

func releaseImagesRollbackCmd() *cli.Command {
	return &cli.Command{
		Name:  subCmdRollback,
		Usage: "roll back a failed release-image promotion from its promotion journal",
		Flags: append(
			releaseImagesCommonFlags(),
			releaseImagesStateFlags(
				releaseImagesProviderTokenFileFlag(),
				dryRunFlag(),
			)...,
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			common, err := releaseImagesCommonFromCmd(cmd)
			if err != nil {
				return err
			}

			if stageErr := requireReleaseStage(cmd.String("stage")); stageErr != nil {
				return stageErr
			}

			if stdinErr := common.singleStdin(cmd); stdinErr != nil {
				return stdinErr
			}

			journal, err := releaseImagesJournal(cmd)
			if err != nil {
				return err
			}

			// Both states mean "nothing to undo" and both are legitimate — a
			// workflow's `if: failure()` rollback step runs even when the
			// promotion failed before it journalled anything. But they are
			// reached by different routes, and only one of them is also what a
			// wrong --journal path looks like, so say which one it is: an
			// operator who mistyped the path, or whose state dir did not
			// survive between jobs, otherwise reads a bare "nothing to roll
			// back" as confirmation that the release was undone.
			if !fileExistsAt(journal) {
				_, _ = fmt.Fprintf(os.Stderr, "No promotion journal at %s — nothing to roll back.\n", journal)

				return nil
			}

			if !fileHasContent(journal) {
				_, _ = fmt.Fprintf(os.Stderr, "Promotion journal %s is empty — nothing to roll back.\n", journal)

				return nil
			}

			return releaseImagesRollback(ctx, cmd, common, journal)
		},
	}
}

// releaseImagesRollback undoes the journaled promotion inside the short-lived
// Docker config: admit the journal, log in, then roll back via the shared
// journal rollback body.
func releaseImagesRollback(ctx context.Context, cmd *cli.Command, common releaseImagesCommon, journal string) error {
	records, err := loadPromotionJournal(journal, "release images", common.ExpectedImageRepository)
	if err != nil {
		return err
	}

	return withReleaseImagesDockerConfig(func(authFile string) error {
		if err := common.login(authFile); err != nil {
			return err
		}

		if dryrun.Enabled(cmd) {
			reg := newDryRunRegistry(ociregistry.WithAuthFile(authFile), os.Stderr)

			return runPromotionJournalRollback(ctx, reg, records, common.ReleaseTag, "release images")
		}

		deleter, err := releaseImagesTagDeleter(cmd, common, authFile)
		if err != nil {
			return err
		}

		reg := auditPromotionRollbackRegistry{
			PromotionRollbackRegistry: promotionRollbackRegistry{Registry: ociregistry.WithAuthFile(authFile), deleter: deleter},
			out:                       os.Stderr,
		}

		return runPromotionJournalRollback(ctx, reg, records, common.ReleaseTag, "release images")
	})
}

func releaseImagesCleanupCmd() *cli.Command {
	return &cli.Command{
		Name:  subCmdCleanup,
		Usage: "delete release candidate tags after verified final/moving promotion",
		Flags: append(
			releaseImagesCommonFlags(),
			releaseImagesAllowMissingLedgerFlag(),
			releaseImagesProviderTokenFileFlag(),
			dryRunFlag(),
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			common, err := releaseImagesCommonFromCmd(cmd)
			if err != nil {
				return err
			}

			if stageErr := requireReleaseStage(cmd.String("stage")); stageErr != nil {
				return stageErr
			}

			if stdinErr := common.singleStdin(cmd); stdinErr != nil {
				return stdinErr
			}

			entries, present, err := common.admitLedger(cmd, false)
			if err != nil || !present {
				return err
			}

			return withReleaseImagesDockerConfig(func(authFile string) error {
				if err := common.login(authFile); err != nil {
					return err
				}

				if dryrun.Enabled(cmd) {
					reg := newDryRunRegistry(ociregistry.WithAuthFile(authFile), os.Stderr)

					return runLedgerCleanup(ctx, reg, entries, common.ReleaseTag, "release images", true)
				}

				deleter, err := releaseImagesTagDeleter(cmd, common, authFile)
				if err != nil {
					return err
				}

				reg := auditDeleter{
					CleanupRegistry: cleanupRegistry{resolver: ociregistry.WithAuthFile(authFile), deleter: deleter},
					out:             os.Stderr,
				}

				return runLedgerCleanup(ctx, reg, entries, common.ReleaseTag, "release images", false)
			})
		},
	}
}

func releaseImagesCommonFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "dist-dir", Value: "dist", Sources: cli.EnvVars("DIST_DIR"), Usage: "release dist directory; the default ledger and SLSA envelope are resolved under this directory"},
		&cli.StringFlag{Name: flagLedger, Sources: cli.EnvVars("RELEASE_IMAGES_PATH", "RELEASE_IMAGES_LEDGER"), Usage: "release-image ledger path; defaults to <dist-dir>/release-images.json and must remain under <dist-dir>/"},
		releaseTagFlag(),
		stageFlag(),
		&cli.StringFlag{Name: flagServerURL, Sources: cienv.ServerURL(), Usage: "forge server URL used to derive the registry host and provider API base"},
		&cli.StringFlag{Name: flagRepository, Sources: cienv.Repository(), Usage: "owner/repo used to derive the expected image repository"},
		&cli.StringFlag{Name: flagRegistry, Sources: cli.EnvVars("CONTAINER_REGISTRY"), Usage: "registry host for the short-lived Docker auth config (default: the image repository's host with --external-registry, else the host from --server-url)"},
		regflags.Username(),
		regflags.PasswordFile(),
		expectedImageRepositoryFlag(),
		externalRegistryFlag(),
	}
}

func releaseImagesStateFlags(extra ...cli.Flag) []cli.Flag {
	flags := make([]cli.Flag, 0, 2+len(extra)) //nolint:mnd // the two fixed state flags appended below.
	flags = append(flags,
		&cli.StringFlag{Name: "state-dir", Sources: cli.EnvVars("SIGN_AND_PUBLISH_STATE_DIR"), Usage: "release-image state directory; default promotion journal is <state-dir>/image-promotions.jsonl"},
		promotionJournalFlag(),
	)

	return append(flags, extra...)
}

func releaseImagesAllowMissingLedgerFlag() cli.Flag {
	return &cli.BoolFlag{Name: flagAllowMissingLedger, Usage: "accept a release without images: when the default ledger <dist-dir>/release-images.json does not exist, say so and exit 0 before any credential read or registry login. The dist directory must exist, a ledger that exists is held to every rule, and a ledger named with --ledger must exist"}
}

func releaseImagesProviderTokenFileFlag() cli.Flag {
	return &cli.StringFlag{Name: flagProviderTokenFile, Usage: "file containing the provider/package-API token (\"-\" reads stdin); defaults to $FORGE_CI_PACKAGE_API_TOKEN, then the detected forge's own token variable ($FORGEJO_TOKEN or $GITEA_TOKEN on Forgejo, $GITLAB_TOKEN on GitLab); another forge's token is never used. The token never appears in argv."}
}

func releaseImagesCommonFromCmd(cmd *cli.Command) (releaseImagesCommon, error) {
	distDir := strings.TrimRight(cmd.String("dist-dir"), "/")
	if distDir == "" {
		return releaseImagesCommon{}, fmt.Errorf("release images: --dist-dir is required: %w", errs.ErrUsage)
	}

	ledger := cmd.String(flagLedger)

	ledgerExplicit := ledger != ""
	if !ledgerExplicit {
		ledger = distDir + "/" + imageledger.DefaultBasename
	}

	if err := appcontainer.ValidateReleaseImagesPath(ledger, distDir); err != nil {
		return releaseImagesCommon{}, err
	}

	serverURL := appcontainer.NormalizedServerURL(cmd.String(flagServerURL))

	serverHost, err := appcontainer.OptionalRegistryHost(serverURL)
	if err != nil {
		return releaseImagesCommon{}, err
	}

	repository := strings.TrimSpace(cmd.String(flagRepository))

	expectedImageRepository, err := releaseImagesExpectedRepository(cmd, serverHost, repository)
	if err != nil {
		return releaseImagesCommon{}, err
	}

	registry, err := resolveRegistryHostFromFlags(cmd, serverHost, expectedImageRepository)
	if err != nil {
		return releaseImagesCommon{}, err
	}

	if registry == "" {
		return releaseImagesCommon{}, fmt.Errorf("release images: --registry or --server-url is required for registry login: %w", errs.ErrUsage)
	}

	return releaseImagesCommon{
		DistDir:                 distDir,
		Ledger:                  ledger,
		LedgerExplicit:          ledgerExplicit,
		ReleaseTag:              cmd.String(flagTag),
		ServerURL:               serverURL,
		Registry:                registry,
		Repository:              repository,
		RegistryUsername:        strings.TrimSpace(cmd.String(flagRegistryUsername)),
		RegistryPasswordFile:    cmd.String(flagRegistryPasswordFile),
		ExpectedImageRepository: expectedImageRepository,
		External:                cmd.Bool(flagExternalRegistry),
	}, nil
}

// resolveRegistryHostFromFlags picks the registry host for short-lived
// auth: --registry when set (normalized to a bare host); else, for a
// repository declared external (--external-registry), that repository's host;
// else the host derived from --server-url. Empty when none is available.
func resolveRegistryHostFromFlags(cmd *cli.Command, serverHost, imageRepository string) (string, error) {
	registry := cmd.String(flagRegistry)
	if registry != "" {
		return appcontainer.RegistryHost(registry)
	}

	if cmd.Bool(flagExternalRegistry) {
		return appcontainer.OptionalRegistryHost(imageRepository)
	}

	return serverHost, nil
}

// releaseImagesExpectedRepository resolves the exact release-image
// repository: the explicit --expected-image-repository flag, else
// host/lower(owner/repo).
func releaseImagesExpectedRepository(cmd *cli.Command, serverHost, repository string) (string, error) {
	expectedImageRepository := strings.TrimSpace(cmd.String(flagExpectedImageRepository))
	if expectedImageRepository != "" {
		return expectedImageRepository, nil
	}

	if serverHost == "" || repository == "" {
		return "", fmt.Errorf("release images: --expected-image-repository or both --server-url and --repository are required: %w", errs.ErrUsage)
	}

	return appcontainer.DefaultReleaseImageRepository(serverHost, repository)
}

// admitLedger admits this command's ledger before any credential is read.
// The second result is false when --allow-missing-ledger admitted a release without
// images, which is said on stderr: the caller has nothing to do. The flag
// reaches the default ledger path only, so a named ledger has to exist.
func (c releaseImagesCommon) admitLedger(cmd *cli.Command, nonEmpty bool) ([]imageledger.Entry, bool, error) {
	entries, present, err := appcontainer.LoadReleaseImagesLedger(appcontainer.ReleaseImagesLedgerInput{
		DistDir:                 c.DistDir,
		Ledger:                  c.Ledger,
		ReleaseTag:              c.ReleaseTag,
		ExpectedImageRepository: c.ExpectedImageRepository,
		NonEmpty:                nonEmpty,
		MayBeAbsent:             cmd.Bool(flagAllowMissingLedger) && !c.LedgerExplicit,
	})
	if err != nil {
		return nil, false, err
	}

	if !present {
		_, _ = fmt.Fprintf(os.Stderr, "release images: no release-image ledger at %s; nothing to %s.\n", c.Ledger, cmd.Name)
	}

	return entries, present, nil
}

// singleStdin refuses rollback and cleanup when the provider token and the
// registry password are both read from stdin, before either is read.
func (c releaseImagesCommon) singleStdin(cmd *cli.Command) error {
	return cliio.SingleStdin(
		cliio.StdinInput{Flag: flagProviderTokenFile, Path: cmd.String(flagProviderTokenFile)},
		cliio.StdinInput{Flag: flagRegistryPasswordFile, Path: c.RegistryPasswordFile},
	)
}

func (c releaseImagesCommon) login(authFile string) error {
	password, err := regflags.LoginPassword(c.RegistryUsername, c.RegistryPasswordFile)
	if err != nil {
		return err
	}

	return appcontainer.RegistryLogin(os.Stderr, appcontainer.RegistryLoginInput{
		Registry: c.Registry,
		Username: c.RegistryUsername,
		Password: password,
		AuthFile: authFile,
	})
}

func withReleaseImagesDockerConfig(fn func(authFile string) error) error {
	root := runcontext.TempRoot(os.Getenv)

	dir, err := os.MkdirTemp(root, "forge-ci-release-images-docker-config-*")
	if err != nil {
		return fmt.Errorf("release images: create Docker config dir: %w", err)
	}

	defer func() { _ = os.RemoveAll(dir) }() //nolint:gosec // dir is a fresh MkdirTemp path under the runner temp root, not caller input.

	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // 0o700: a directory needs the owner execute bit; it stays owner-only.
		return fmt.Errorf("release images: chmod Docker config dir: %w", err)
	}

	restoreDockerConfig, setErr := cienv.SetTemporarily("DOCKER_CONFIG", dir)
	if setErr != nil {
		return fmt.Errorf("release images: %w", setErr)
	}

	defer restoreDockerConfig()

	return fn(filepath.Join(dir, "config.json"))
}

func releaseImagesJournal(cmd *cli.Command) (string, error) {
	if journal := cmd.String("journal"); journal != "" {
		return journal, nil
	}

	stateDir := cmd.String("state-dir")
	if stateDir == "" {
		return "", fmt.Errorf("release images: --journal or --state-dir is required: %w", errs.ErrUsage)
	}

	return filepath.Join(stateDir, releaseImagesDefaultJournal), nil
}

func fileHasContent(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.Size() > 0
}

// fileExistsAt distinguishes "no journal was ever written here" from "the
// journal is present but empty" — see the rollback action.
func fileExistsAt(path string) bool {
	_, err := os.Stat(path)

	return err == nil
}

// releaseImagesProviderToken resolves the tag-deletion token: the forge-neutral
// sources first (--provider-token-file, then $FORGE_CI_PACKAGE_API_TOKEN), then
// the detected forge's own token variables. Another forge's token is not read:
// it would be sent to a server it was not issued for.
func releaseImagesProviderToken(cmd *cli.Command) (string, error) {
	const neutralEnv = "FORGE_CI_PACKAGE_API_TOKEN"

	token, err := regflags.FileOrEnv(cmd.String(flagProviderTokenFile), append([]string{neutralEnv}, deps.ProviderTokenEnv()...)...)
	if err != nil {
		return "", err
	}

	if token == "" {
		return "", errs.CredentialRequired(errs.Credential{What: "provider tag-deletion token", Flag: flagProviderTokenFile, Env: neutralEnv})
	}

	return token, nil
}

// releaseImagesTagDeleter returns the deleter for the registry the images are
// on: the detected forge's, configured with the provider token and the
// --server-url and --repository of this invocation, for the forge's own
// registry, and plain OCI with the login's auth file for an external one. The values
// go to the adapter, not to the process environment.
func releaseImagesTagDeleter(cmd *cli.Command, common releaseImagesCommon, authFile string) (imageledger.TagDeleter, error) {
	// Images on an external registry, such as GHCR, are deleted through that
	// registry, which may not delete tags at all; the forge's package API only
	// knows the forge's own packages.
	if common.External {
		return externalTagDeleter(common.Registry, common.RegistryUsername, common.RegistryPasswordFile, ociregistry.WithAuthFile(authFile))
	}

	token, err := releaseImagesProviderToken(cmd)
	if err != nil {
		return nil, err
	}

	forge, err := deps.ProviderWith(deps.ProviderConfig{
		Token:      token,
		ServerURL:  common.ServerURL,
		Repository: common.Repository,
	})
	if err != nil {
		return nil, err
	}

	return deps.RoleFrom[provider.TagDeleter](forge, "container tag deletion")
}

func requireReleaseStage(stage string) error {
	if stage != imageledger.ReleaseStageName {
		return fmt.Errorf("release images: only --stage release is supported: %w", errs.ErrUsage)
	}

	return nil
}
