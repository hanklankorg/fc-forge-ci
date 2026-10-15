// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	appbaseimages "github.com/diggsweden/forge-ci/internal/app/baseimages"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/cli/regflags"
	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
)

func baseImagesImportCmd() *cli.Command {
	return &cli.Command{
		Name:  subCmdImport,
		Usage: "copy unsigned base digests from staging to a separate source-owned publication repository",
		Description: "Admits a nonempty base-only ledger and its content-addressed tags before login. " +
			"Copies only immutable image digests to staging candidates, without signatures or final tags. " +
			"Preserves base identities, SBOM pins and lineage; conflicting candidates refuse. " +
			"The expected publication repository must be on the login registry under the source owner.",
		Flags: append(baseImagesLocationFlags(),
			baseImagesRegistryFlag(),
			&cli.StringFlag{Name: flagLedger, Required: true, Usage: "unsigned source base-image ledger"},
			&cli.StringFlag{Name: "source-repository", Required: true, Usage: "exact staging image repository allowed in the source ledger"},
			&cli.StringFlag{Name: "output-ledger", Required: true, Usage: "new publication ledger under --dist-dir; never replaces the source ledger"},
			&cli.StringFlag{Name: "dist-dir", Value: "dist", Usage: "artifact root containing both ledgers"},
			regflags.Username(), regflags.PasswordFile(),
		),
		Action: importBaseImages,
	}
}

func importBaseImages(ctx context.Context, cmd *cli.Command) error {
	common, err := baseImagesLocationFromCmd(cmd, true)
	if err != nil {
		return err
	}

	if err = common.requireSourceOwnedRepository(); err != nil {
		return err
	}

	entries, err := readBaseImportLedger(cmd, common)
	if err != nil {
		return err
	}

	return withBaseImagesDockerConfig(func(authFile string) error {
		if err := common.login(authFile); err != nil {
			return err
		}

		imported, err := imageledger.ImportBaseImages(ctx, ociregistry.WithAuthFile(authFile), entries, cmd.String("source-repository"), common.ExpectedRepository)
		if err != nil {
			return err
		}

		encoded, err := imageledger.Marshal(imported)
		if err != nil {
			return err
		}

		return cliio.WriteFile(cmd.String("output-ledger"), encoded, 0o644)
	})
}

func readBaseImportLedger(cmd *cli.Command, common baseImagesCommon) ([]imageledger.Entry, error) {
	input, output, root := cmd.String(flagLedger), cmd.String("output-ledger"), cmd.String("dist-dir")
	for _, path := range []string{input, output} {
		if err := appcontainer.ValidateReleaseImagesPath(path, root); err != nil {
			return nil, err
		}
	}

	if filepath.Clean(input) == filepath.Clean(output) {
		return nil, fmt.Errorf("base images: output ledger must preserve the source ledger: %w", errs.ErrUsage)
	}

	if _, err := os.Lstat(output); !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("base images: output ledger must not exist: %w", errs.ErrValidation)
	}

	body, err := cliio.ReadFile(input)
	if err != nil {
		return nil, err
	}

	source := cmd.String("source-repository")

	entries, err := appbaseimages.AdmitBaseImageLedger(body, source)
	if err != nil {
		return nil, err
	}

	if _, err := imageledger.PlanBaseImport(entries, source, common.ExpectedRepository); err != nil {
		return nil, err
	}

	return entries, nil
}
