// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// LayoutFilesystemReader supplies an already verified, flattened local image.
type LayoutFilesystemReader interface {
	ExportLayoutFilesystem(ctx context.Context, directory string) (io.ReadCloser, error)
}

// ExtractLayoutFiles copies explicitly selected regular files from the actual
// retained image. It never executes an image or extracts unselected tar paths.
// Missing inputs fail the job; callers must not publish a failed partial output.
func ExtractLayoutFiles(ctx context.Context, reader LayoutFilesystemReader, out io.Writer, layoutDirectory, outputDirectory string, paths []string) error {
	names, err := domaincontainer.ImageFileNames(paths)
	if err != nil {
		return err
	}

	if err = ctx.Err(); err != nil {
		return err
	}

	wanted := map[string]string{}
	for index, source := range paths {
		wanted[strings.TrimPrefix(source, "/")] = names[index]
	}

	stream, err := reader.ExportLayoutFilesystem(ctx, layoutDirectory)
	if err != nil {
		return err
	}

	defer func() { _ = stream.Close() }()

	if err = os.MkdirAll(outputDirectory, 0o700); err != nil {
		return err
	}

	root, err := pathsafe.OpenRoot(outputDirectory)
	if err != nil {
		return err
	}

	defer func() { _ = root.Close() }()

	return extractSelectedImageFiles(ctx, root, stream, out, wanted, paths)
}

func extractSelectedImageFiles(ctx context.Context, root *os.Root, stream io.Reader, out io.Writer, wanted map[string]string, paths []string) error { //nolint:cyclop // one bounded archive traversal; selection, byte budget and missing-file accounting share state.
	limited := &io.LimitedReader{R: stream, N: 10 << 30}
	archive := tar.NewReader(limited)

	var total int64

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return err
		}

		source := strings.TrimPrefix(strings.TrimPrefix(header.Name, "./"), "/")

		name, selected := wanted[source]
		if !selected {
			continue
		}

		total += header.Size
		// archive/tar normalizes legacy TypeRegA entries to TypeReg.
		if header.Typeflag != tar.TypeReg || header.Size <= 0 || header.Size > 512<<20 || total > 2<<30 {
			return fmt.Errorf("selected image file %s must be a bounded nonempty regular file: %w", source, errs.ErrValidation)
		}

		if err := copyImageFile(root, name, archive, header.Size); err != nil {
			return err
		}

		delete(wanted, source)
		_, _ = fmt.Fprintf(out, "Extracted %s as %s from the retained image filesystem\n", source, name)

		if len(wanted) == 0 {
			return nil
		}
	}

	if limited.N == 0 {
		return fmt.Errorf("image filesystem extraction exceeded 10 GiB: %w", errs.ErrValidation)
	}

	if len(wanted) != 0 {
		var missing []string

		for _, source := range paths {
			if _, absent := wanted[strings.TrimPrefix(source, "/")]; absent {
				missing = append(missing, path.Clean(source))
			}
		}

		return fmt.Errorf("selected image files missing: %s: %w", strings.Join(missing, ", "), errs.ErrMissingInput)
	}

	return nil
}

func copyImageFile(root *os.Root, name string, source io.Reader, size int64) error {
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}

	_, copyErr := io.CopyN(file, source, size)

	return errors.Join(copyErr, file.Close())
}
