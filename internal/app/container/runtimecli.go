// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// RuntimeCLIReader fetches and inspects candidates, never executing them.
type RuntimeCLIReader interface {
	RegistryInputReader
	LayoutFilesystemReader
}

// VerifyRuntimeCLI compares downloaded or retained image bytes to the source pin.
func VerifyRuntimeCLI(ctx context.Context, registry RuntimeCLIReader, workspace, arch, file, imageRef string) error {
	if arch != "amd64" && arch != "arm64" {
		return fmt.Errorf("unsupported CLI architecture: %w", errs.ErrUsage)
	}

	if (file == "") == (imageRef == "") {
		return fmt.Errorf("CLI verification requires exactly one file or image ref: %w", errs.ErrUsage)
	}

	expected, err := runtimeCLIChecksum(workspace, arch)
	if err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	if imageRef != "" {
		work, err := os.MkdirTemp("", "forge-ci-runtime-cli-*")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(work) }()

		layout := filepath.Join(work, "layout")
		if err := FetchRegistryInput(ctx, registry, imageRef, "linux/"+arch, layout); err != nil {
			return err
		}

		extracted := filepath.Join(work, "extracted")
		if err := ExtractLayoutFiles(ctx, registry, io.Discard, layout, extracted, []string{"/usr/local/bin/forge-ci"}); err != nil {
			return err
		}

		file = filepath.Join(extracted, "forge-ci")
	}

	body, err := cliio.ReadRegularFile(file)
	if err != nil {
		return err
	}

	if fmt.Sprintf("%x", sha256.Sum256(body)) != expected {
		return fmt.Errorf("CLI bytes do not match source checksum: %w", errs.ErrValidation)
	}

	return nil
}
