// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish

import (
	"context"
	"fmt"
	"io"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// MavenRepositoryUploader implements the Maven repository protocol; registry
// location/authentication still comes from the existing forge resolver roles.
type MavenRepositoryUploader interface {
	Upload(ctx context.Context, registry provider.ForgeMavenRegistry, name string, body io.Reader, size int64) error
}

// UploadMavenPublication validates the full set before uploading any file. A
// partial failure reports its exact progress and never deletes registry data.
func UploadMavenPublication(ctx context.Context, uploader MavenRepositoryUploader, resolver provider.ForgeMavenRegistryResolver, out io.Writer, directory, version string) error {
	set, err := InspectMavenPublication(directory, version, false)
	if err != nil {
		return err
	}

	registry, err := resolver.ResolveForgeMavenRegistry()
	if err != nil {
		return err
	}

	root, err := pathsafe.OpenRoot(directory)
	if err != nil {
		return err
	}

	defer func() { _ = root.Close() }()

	for index, name := range set.Files {
		file, uploadErr := root.Open(name)
		if uploadErr != nil {
			return uploadErr
		}

		info, uploadErr := file.Stat()
		if uploadErr == nil {
			uploadErr = uploader.Upload(ctx, registry, name, file, info.Size())
		}

		_ = file.Close()

		if uploadErr != nil {
			return fmt.Errorf("publication stopped after %d/%d files; inspect the registry before retrying: %w", index, len(set.Files), uploadErr)
		}
	}

	_, err = fmt.Fprintf(out, "Published %d Maven publication(s), %d files\n", len(set.Publications), len(set.Files))

	return err
}
