// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/cosign"
	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	appbaseimages "github.com/diggsweden/forge-ci/internal/app/baseimages"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/cli/cienv"
	"github.com/diggsweden/forge-ci/internal/cli/regflags"
	"github.com/diggsweden/forge-ci/internal/cli/signflags"
	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
)

func baseImagesSignCmd() *cli.Command {
	return &cli.Command{
		Name:  subCmdSign,
		Usage: "sign and attest the base images recorded in a base-image ledger",
		Description: `Signer side of the base-image boundary, in one process and in this order:

  1. admit the repository and the ledger: the base-image repository is on the
     registry this command logs in to (--registry, else the --server-url host,
     with its port) and below the owner of --repository, compared without
     case; the ledger parses, every entry is an image_kind=base entry, and
     every ref and tag is inside that repository (the one "base-images
     validate" derives from the same flags);
  2. build the run-level SLSA provenance from the ledger's own image digests
     (the generic "release provenance" profile), naming --server-url/--repository
     as the source, --source-sha as its commit and --workflow as the caller;
  3. log in to the registry with a short-lived auth config;
  4. sign each image digest and attest its CycloneDX SBOM and its per-image
     SLSA predicate, as "container ledger sign" does.

A refusal in step 1 or 2 happens before any credential is read and before
anything is signed or written to a registry. The source repository and commit
come from the flags, so a signer can attest a pinned commit other than the one
it runs on; the builder identity, invocation and ref remain the runner's own.
--expected-repository chooses the repository name below the owner and nothing
else: another owner or another registry is refused, with no flag to allow it,
so the signer cannot attest a path the source repository's owner does not hold.
--workflow, --source-sha and --started-on are read from the command line only:
what the statement records is not taken from a variable the job happens to set.

EXAMPLE:
   forge-ci container base-images sign \
     --ledger base-images.ledger.json --repository-suffix -base \
     --workflow build-base-images.yml --source-sha "$SOURCE_SHA" \
     --started-on "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
     --method kms --key env://COSIGN_KEY`,
		Flags: append(
			append(baseImagesLocationFlags(),
				baseImagesRegistryFlag(),
				&cli.StringFlag{Name: flagLedger, Required: true, Sources: cli.EnvVars("BASE_IMAGES_LEDGER"), Usage: "base-image ledger JSON file: a bare array of image_kind=base entries (\"-\" reads stdin)"},
				&cli.StringFlag{Name: flagWorkflow, Usage: "calling workflow filename/path, relative with no '..' step or control character; recorded as externalParameters.workflow, which base-image verification matches against --expected-workflow"},
				&cli.StringFlag{Name: flagSourceSHA, Usage: "pinned source commit the base images were built from, recorded as the provenance gitCommit"},
				&cli.StringFlag{Name: flagStartedOn, Usage: "RFC3339 build timestamp (default: $SOURCE_DATE_EPOCH)"},
				&cli.StringFlag{Name: flagSBOMPathPattern, Sources: cli.EnvVars("LEDGER_SIGN_SBOM_PATH_PATTERN"), Usage: "optional regular expression every ledger SBOM path must match"},
				regflags.Username(),
				regflags.PasswordFile(),
			),
			signflags.Cosign(signflags.CosignOpts{MethodNote: cosignMethodNoteGPG})...,
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			common, err := baseImagesLocationFromCmd(cmd, true)
			if err != nil {
				return err
			}

			source, err := baseImagesSignSource(common)
			if err != nil {
				return err
			}

			method, err := domainrelease.ParseSignMethod(cmd.String("method"))
			if err != nil {
				return err
			}

			if stdinErr := cliio.SingleStdin(
				cliio.StdinInput{Flag: flagLedger, Path: cmd.String(flagLedger)},
				cliio.StdinInput{Flag: flagRegistryPasswordFile, Path: common.RegistryPasswordFile},
			); stdinErr != nil {
				return stdinErr
			}

			ledger, err := cliio.ReadFile(cmd.String(flagLedger))
			if err != nil {
				return fmt.Errorf("base images: read ledger %s: %w", cmd.String(flagLedger), err)
			}

			entries, err := appbaseimages.AdmitBaseImageLedger(ledger, common.ExpectedRepository)
			if err != nil {
				return err
			}

			statement, err := baseImagesSignStatement(cmd, source, entries)
			if err != nil {
				return err
			}

			return withBaseImagesDockerConfig(func(authFile string) error {
				// The signer reads the statement from a file; the private
				// auth directory is this run's only scratch space and is
				// removed with it.
				envelope := filepath.Join(filepath.Dir(authFile), "base-provenance.intoto.json")
				if err := os.WriteFile(envelope, statement, 0o600); err != nil { //nolint:gosec // path is inside the fresh private directory created by withBaseImagesDockerConfig.
					return fmt.Errorf("base images: write provenance statement: %w", err)
				}

				if err := common.login(authFile); err != nil {
					return err
				}

				return runLedgerSign(ctx,
					cosign.ForKeyRef(cmd.String("key"), "DOCKER_CONFIG"),
					ociregistry.WithAuthFile(authFile),
					appcontainer.SignLedgerImagesInput{
						Entries:                 entries,
						PredicateEnvelopePath:   envelope,
						Method:                  method,
						KeyRef:                  cmd.String("key"),
						OIDCIssuer:              cmd.String("oidc-issuer"),
						FulcioURL:               signflags.ReadEndpoints(cmd).FulcioURL,
						RekorURL:                signflags.ReadEndpoints(cmd).RekorURL,
						TrustedRootPath:         signflags.ReadEndpoints(cmd).TrustedRootPath,
						ExpectedImageRepository: common.ExpectedRepository,
						SBOMPathPattern:         cmd.String(flagSBOMPathPattern),
					})
			})
		},
	}
}

// baseImagesSignSource returns the source repository URL the statement
// records, once the base-image repository is one that source may be signed
// in.
func baseImagesSignSource(common baseImagesCommon) (string, error) {
	source := baseImagesSource(common.ServerURL, common.Repository)
	if source == "" {
		return "", fmt.Errorf("base images: --%s and --%s are required: they name the source repository the provenance records: %w", flagServerURL, flagRepository, errs.ErrUsage)
	}

	if err := common.requireSourceOwnedRepository(); err != nil {
		return "", err
	}

	return source, nil
}

// baseImagesSignStatement builds the run-level provenance statement for the
// admitted ledger. The signer records source, the one base-image verification
// derives from the same --server-url and --repository, pinned to the
// --source-sha commit. The builder identity, invocation, ref and build
// timestamp are resolved as `release provenance` resolves them.
func baseImagesSignStatement(cmd *cli.Command, source string, entries []imageledger.Entry) ([]byte, error) {
	startedOn, err := cienv.StartedOn(cmd.String(flagStartedOn))
	if err != nil {
		return nil, fmt.Errorf("base images: %w", err)
	}

	_, ref, _ := cienv.ProvenanceSource()

	return appbaseimages.BaseImageProvenance(appbaseimages.BaseImageProvenanceInput{
		Entries:       entries,
		RepositoryURL: source,
		Ref:           ref,
		SourceSHA:     cmd.String(flagSourceSHA),
		Workflow:      cmd.String(flagWorkflow),
		BuilderID:     cienv.ProvenanceBuilderID(),
		InvocationID:  cienv.ProvenanceInvocationID(),
		StartedOn:     startedOn,
	})
}
