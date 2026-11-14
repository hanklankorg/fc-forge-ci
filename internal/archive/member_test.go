// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package archive_test

import (
	"archive/tar"
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
