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
	ExportLayoutFilesystem(context.Context, string) (io.ReadCloser, error)
}

// ExtractLayoutFiles copies explicitly selected regular files from the actual
// retained image. It never executes an image or extracts unselected tar paths.
// Missing inputs fail the job; callers must not publish a failed partial output.
func ExtractLayoutFiles(ctx context.Context, reader LayoutFilesystemReader, out io.Writer, layoutDirectory, outputDirectory string, paths []string) error {
	names, err := domaincontainer.ImageFileNames(paths)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
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
	if err := os.MkdirAll(outputDirectory, 0o700); err != nil {
		return err
	}
	root, err := pathsafe.OpenRoot(outputDirectory)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
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
		if (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) || header.Size <= 0 || header.Size > 512<<20 || total > 2<<30 {
			return fmt.Errorf("selected image file %s must be a bounded nonempty regular file: %w", source, errs.ErrValidation)
		}
		file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
		if err != nil {
			return err
		}
		_, copyErr := io.CopyN(file, archive, header.Size)
		closeErr := file.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
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
