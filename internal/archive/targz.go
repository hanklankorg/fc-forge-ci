// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package archive

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"time"
)

// TarGzipWriter writes regular files into a gzip-compressed tar. The output
// is reproducible: the same files added in the same order give the same
// bytes, because no entry carries the source file's mode, times or owner and
// the gzip header carries neither a name nor a time.
type TarGzipWriter struct {
	gz *gzip.Writer
	tw *tar.Writer
}

// NewTarGzipWriter starts an archive on w. The caller must Close it.
func NewTarGzipWriter(w io.Writer) *TarGzipWriter {
	// A new gzip.Writer has an empty name and a zero ModTime in its header.
	gz := gzip.NewWriter(w)

	return &TarGzipWriter{gz: gz, tw: tar.NewWriter(gz)}
}

// AddFile appends file as a regular entry called name. Entries keep the order
// they are added in.
func (w *TarGzipWriter) AddFile(name string, file fs.File) error {
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat %q: %w", name, err)
	}

	// Format is left unset: the tar writer then picks USTAR and falls back to
	// PAX only for a name or size USTAR cannot hold.
	hdr := &tar.Header{
		Typeflag: tar.TypeReg,
		Name:     filepath.ToSlash(name),
		Size:     info.Size(),
		Mode:     0o644,
		ModTime:  time.Unix(0, 0),
	}

	if err := w.tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("tar header %q: %w", name, err)
	}

	if _, err := io.Copy(w.tw, file); err != nil {
		return fmt.Errorf("tar copy %q: %w", name, err)
	}

	return nil
}

// Close finishes the tar and the gzip stream. It does not close the
// underlying writer.
func (w *TarGzipWriter) Close() error {
	if err := w.tw.Close(); err != nil {
		_ = w.gz.Close()

		return fmt.Errorf("close tar: %w", err)
	}

	if err := w.gz.Close(); err != nil {
		return fmt.Errorf("close gzip: %w", err)
	}

	return nil
}
