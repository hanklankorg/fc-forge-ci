// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

type imageFilesystemFixture struct {
	archive []byte
	err     error
	calls   int
}

func (fixture *imageFilesystemFixture) ExportLayoutFilesystem(context.Context, string) (io.ReadCloser, error) {
	fixture.calls++
	return io.NopCloser(bytes.NewReader(fixture.archive)), fixture.err
}

func imageFileArchive(t *testing.T, kind byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, file := range []struct{ name, body string }{{"usr/local/bin/tool", "actual image bytes"}, {"etc/unselected", "never extracted"}} {
		header := &tar.Header{Name: file.name, Mode: 0o755, Typeflag: tar.TypeReg, Size: int64(len(file.body))}
		if file.name == "usr/local/bin/tool" && kind != tar.TypeReg {
			header.Typeflag, header.Size, header.Linkname = kind, 0, "../../outside"
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size != 0 {
			if _, err := io.WriteString(writer, file.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestExtractLayoutFilesCopiesOnlySelectedImageBytesAndNeverOverwrites(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "executables")
	reader := &imageFilesystemFixture{archive: imageFileArchive(t, tar.TypeReg)}
	if err := appcontainer.ExtractLayoutFiles(t.Context(), reader, io.Discard, "retained-layout", directory, []string{"/usr/local/bin/tool"}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(directory, "tool"))
	if err != nil || string(body) != "actual image bytes" {
		t.Fatalf("extracted bytes = %q, %v", body, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "tool" {
		t.Fatalf("unexpected output inventory: %v %v", entries, err)
	}
	if err := appcontainer.ExtractLayoutFiles(t.Context(), reader, io.Discard, "retained-layout", directory, []string{"/usr/local/bin/tool"}); err == nil {
		t.Fatal("overwrote an existing executable")
	}
}

func TestExtractLayoutFilesRejectsUnsafeMissingAndNonregularInputs(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		paths      []string
		kind       byte
		want       error
		beforeRead bool
	}{
		{"parent path", []string{"../outside"}, tar.TypeReg, errs.ErrValidation, true},
		{"duplicate basename", []string{"/bin/tool", "/usr/bin/tool"}, tar.TypeReg, errs.ErrValidation, true},
		{"empty selection", nil, tar.TypeReg, errs.ErrUsage, true},
		{"missing file", []string{"/bin/missing"}, tar.TypeReg, errs.ErrMissingInput, false},
		{"symlink", []string{"/usr/local/bin/tool"}, tar.TypeSymlink, errs.ErrValidation, false},
		{"hard link", []string{"/usr/local/bin/tool"}, tar.TypeLink, errs.ErrValidation, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			reader := &imageFilesystemFixture{archive: imageFileArchive(t, test.kind)}
			directory := filepath.Join(t.TempDir(), "output")
			err := appcontainer.ExtractLayoutFiles(t.Context(), reader, io.Discard, "retained-layout", directory, test.paths)
			if !errors.Is(err, test.want) {
				t.Fatalf("err = %v, want %v", err, test.want)
			}
			if test.beforeRead && reader.calls != 0 {
				t.Fatal("invalid paths reached image I/O")
			}
		})
	}
}
