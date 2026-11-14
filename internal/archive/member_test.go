// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package archive_test

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/diggsweden/forge-ci/internal/archive"
	"github.com/stretchr/testify/require"
)

func TestReadTarGzipMemberRequiresOneExactRegularMember(t *testing.T) {
	t.Parallel()

	member := tarEntry{hdr: tar.Header{Name: "package/package.json", Typeflag: tar.TypeReg, Mode: 0o600}, body: "{}"}
	for _, tc := range []struct {
		name    string
		entries []tarEntry
		limit   int64
		valid   bool
	}{
		{"exact", []tarEntry{member}, 2, true},
		{"duplicate", []tarEntry{member, member}, 2, false},
		{"alternate root overwrite", []tarEntry{member, {hdr: tar.Header{Name: "other/package.json", Typeflag: tar.TypeReg}, body: "{}"}}, 2, false},
		{"symlink parent alias", []tarEntry{member, {hdr: tar.Header{Name: "package/alias", Typeflag: tar.TypeSymlink, Linkname: "."}}}, 2, false},
		{"dot alias", []tarEntry{member, {hdr: tar.Header{Name: "package/./package.json", Typeflag: tar.TypeReg}, body: "{}"}}, 2, false},
		{"parent alias", []tarEntry{member, {hdr: tar.Header{Name: "package/sub/../package.json", Typeflag: tar.TypeReg}, body: "{}"}}, 2, false},
		{"absolute alias", []tarEntry{member, {hdr: tar.Header{Name: "/package/package.json", Typeflag: tar.TypeReg}, body: "{}"}}, 2, false},
		{"missing", nil, 2, false},
		{"oversize", []tarEntry{member}, 1, false},
		{"wrong root", []tarEntry{{hdr: tar.Header{Name: "other/package.json", Typeflag: tar.TypeReg}, body: "{}"}}, 2, false},
		{"symlink", []tarEntry{{hdr: tar.Header{Name: "package/package.json", Typeflag: tar.TypeSymlink, Linkname: "/outside"}}}, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			filename := filepath.Join(dir, "package.tgz")
			writeTarball(t, filename, tc.entries)

			body, err := archive.ReadTarGzipMember(filename, "package/package.json", tc.limit)
			if tc.valid {
				require.NoError(t, err)
				require.Equal(t, "{}", string(body))
			} else {
				require.Error(t, err)
			}

			files, err := os.ReadDir(dir)
			require.NoError(t, err)
			require.Len(t, files, 1, "inspection must extract nothing")
		})
	}
}

// A bundle that is rewritten must hold exactly what a reader of the original
// saw: regular files only, each name once, within the size the caller allows.
func TestReadTarGzipFilesAdmitsOnlyDistinctBoundedRegularFiles(t *testing.T) {
	t.Parallel()

	first := tarEntry{hdr: tar.Header{Name: "a.json", Typeflag: tar.TypeReg, Mode: 0o600}, body: "{}"}
	second := tarEntry{hdr: tar.Header{Name: "b.json", Typeflag: tar.TypeReg, Mode: 0o600}, body: "[]"}

	for _, tc := range []struct {
		name    string
		entries []tarEntry
		limit   int64
		valid   bool
	}{
		{"files in archive order", []tarEntry{second, first}, 4, true},
		{"empty archive", nil, 4, true},
		{"repeated name", []tarEntry{first, first}, 4, false},
		{"over the total limit", []tarEntry{first, second}, 3, false},
		{"symlink", []tarEntry{first, {hdr: tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "a.json"}}}, 4, false},
		{"directory", []tarEntry{{hdr: tar.Header{Name: "dir/", Typeflag: tar.TypeDir}}}, 4, false},
		{"parent path", []tarEntry{{hdr: tar.Header{Name: "../a.json", Typeflag: tar.TypeReg}, body: "{}"}}, 4, false},
		{"absolute path", []tarEntry{{hdr: tar.Header{Name: "/a.json", Typeflag: tar.TypeReg}, body: "{}"}}, 4, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			filename := filepath.Join(t.TempDir(), "bundle.tar.gz")
			writeTarball(t, filename, tc.entries)

			files, err := archive.ReadTarGzipFiles(filename, tc.limit)
			if !tc.valid {
				require.Error(t, err)
				require.Empty(t, files)

				return
			}

			require.NoError(t, err)
			require.Len(t, files, len(tc.entries))

			for index, entry := range tc.entries {
				require.Equal(t, archive.TarFile{Name: entry.hdr.Name, Body: []byte(entry.body)}, files[index])
			}
		})
	}
}

// AddBytes writes the entry AddFile writes, so a bundle rewritten from its
// members has the bytes of one written from the files.
func TestTarGzipWriterAddBytesMatchesAddFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	source := filepath.Join(dir, "a.json")
	require.NoError(t, os.WriteFile(source, []byte(`{"a":1}`), 0o600))

	var fromFile, fromBytes bytes.Buffer

	file, err := os.Open(source)
	require.NoError(t, err)

	defer func() { _ = file.Close() }()

	writer := archive.NewTarGzipWriter(&fromFile)
	require.NoError(t, writer.AddFile("a.json", file))
	require.NoError(t, writer.Close())

	writer = archive.NewTarGzipWriter(&fromBytes)
	require.NoError(t, writer.AddBytes("a.json", []byte(`{"a":1}`)))
	require.NoError(t, writer.Close())

	require.Equal(t, fromFile.Bytes(), fromBytes.Bytes())
}
