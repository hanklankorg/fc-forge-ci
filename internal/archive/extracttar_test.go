// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package archive_test

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/diggsweden/forge-ci/internal/archive"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func tarStream(t *testing.T, entries []tarEntry) *bytes.Buffer {
	t.Helper()

	var data bytes.Buffer

	tw := tar.NewWriter(&data)

	for _, entry := range entries {
		hdr := entry.hdr
		if hdr.Typeflag == tar.TypeReg {
			hdr.Size = int64(len(entry.body))
		}

		if err := tw.WriteHeader(&hdr); err != nil {
			t.Fatal(err)
		}

		if _, err := tw.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	return &data
}

// An image filesystem keeps its paths: nothing is stripped, the root entry
// is skipped, and a confined relative symlink survives.
func TestExtractTar_KeepsPaths(t *testing.T) {
	t.Parallel()

	dest := filepath.Join(t.TempDir(), "dist")
	stream := tarStream(t, []tarEntry{
		{hdr: tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755}},
		{hdr: tar.Header{Name: "./hello", Typeflag: tar.TypeReg, Mode: 0o755}, body: "binary"},
		{hdr: tar.Header{Name: "share/doc/README", Typeflag: tar.TypeReg, Mode: 0o644}, body: "doc"},
		{hdr: tar.Header{Name: "current", Typeflag: tar.TypeSymlink, Linkname: "hello"}},
	})

	if err := archive.ExtractTar(stream, dest); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filepath.Join(dest, "hello"))
	if err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("executable not kept: %v %v", info, err)
	}

	if body, err := os.ReadFile(filepath.Join(dest, "share", "doc", "README")); err != nil || string(body) != "doc" {
		t.Fatalf("nested file: %q %v", body, err)
	}

	if target, err := os.Readlink(filepath.Join(dest, "current")); err != nil || target != "hello" {
		t.Fatalf("relative symlink: %q %v", target, err)
	}
}

// Every hostile member is refused and nothing reaches the destination, not
// even the benign entry that preceded it.
func TestExtractTar_RefusesHostileMembersBeforeInstalling(t *testing.T) {
	t.Parallel()

	for name, hostile := range map[string]tarEntry{
		"parent traversal": {hdr: tar.Header{Name: "../escape", Typeflag: tar.TypeReg, Mode: 0o644}, body: "x"},
		"nested traversal": {hdr: tar.Header{Name: "bin/../../escape", Typeflag: tar.TypeReg, Mode: 0o644}, body: "x"},
		"absolute path":    {hdr: tar.Header{Name: "/etc/passwd", Typeflag: tar.TypeReg, Mode: 0o644}, body: "x"},
		"symlink escape":   {hdr: tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../../outside"}},
		"absolute symlink": {hdr: tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc"}},
		"hardlink":         {hdr: tar.Header{Name: "link", Typeflag: tar.TypeLink, Linkname: "hello"}},
		"character device": {hdr: tar.Header{Name: "dev/null", Typeflag: tar.TypeChar, Devmajor: 1, Devminor: 3}},
		"fifo":             {hdr: tar.Header{Name: "pipe", Typeflag: tar.TypeFifo}},
		"block device":     {hdr: tar.Header{Name: "disk", Typeflag: tar.TypeBlock, Devmajor: 8}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dest := filepath.Join(t.TempDir(), "dist")
			entries := []tarEntry{{hdr: tar.Header{Name: "hello", Typeflag: tar.TypeReg, Mode: 0o755}, body: "binary"}, hostile}

			err := archive.ExtractTar(tarStream(t, entries), dest)
			if !errors.Is(err, errs.ErrValidation) {
				t.Fatalf("err=%v, want a validation refusal", err)
			}

			if _, statErr := os.Lstat(filepath.Join(dest, "hello")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("a refused stream installed output: %v", statErr)
			}

			if _, statErr := os.Lstat(filepath.Join(filepath.Dir(dest), "escape")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("a member escaped the destination: %v", statErr)
			}
		})
	}
}

func TestExtractTar_RefusesAStreamThatIsNotTar(t *testing.T) {
	t.Parallel()

	err := archive.ExtractTar(bytes.NewBufferString("not a tar stream at all, but long enough to be read as a header block"), t.TempDir())
	if !errors.Is(err, errs.ErrMalformedInput) {
		t.Fatalf("err=%v, want malformed input", err)
	}
}
