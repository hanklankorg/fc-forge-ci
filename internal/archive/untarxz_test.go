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

	"github.com/ulikunitz/xz"

	"github.com/diggsweden/forge-ci/internal/archive"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// xzTar writes an xz-compressed tar of regular files and returns its bytes.
func xzTar(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var out bytes.Buffer

	zw, err := xz.NewWriter(&out)
	if err != nil {
		t.Fatal(err)
	}

	tw := tar.NewWriter(zw)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}

		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return out.Bytes()
}

// An upstream release such as cargo-cyclonedx's ships only .tar.xz. The
// engine unpacks it with UntarStripOne's defenses, so a runner needs no xz.
func TestUntarXzStripOne(t *testing.T) {
	t.Parallel()

	good := xzTar(t, map[string]string{"tool-x86_64/tool": "binary"})

	for name, tc := range map[string]struct {
		body  []byte
		class error
	}{
		"extracts under the top directory":  {body: good},
		"refuses a parent-traversing entry": {body: xzTar(t, map[string]string{"top/../../escape": "x"}), class: errs.ErrValidation},
		"refuses a truncated stream":        {body: good[:len(good)-8], class: errs.ErrMalformedInput},
		"refuses gzip":                      {body: []byte{0x1f, 0x8b, 8, 0, 0, 0, 0, 0, 0, 3}, class: errs.ErrMalformedInput},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			file := filepath.Join(dir, "tool.tar.xz")

			if err := os.WriteFile(file, tc.body, 0o600); err != nil {
				t.Fatal(err)
			}

			dest := filepath.Join(dir, "out")
			err := archive.UntarXzStripOne(file, dest)

			if tc.class != nil {
				if !errors.Is(err, tc.class) {
					t.Fatalf("err = %v, want %v", err, tc.class)
				}

				if _, statErr := os.Stat(filepath.Join(dest, "tool")); statErr == nil {
					t.Fatal("a refused archive reached the destination")
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			got, err := os.ReadFile(filepath.Join(dest, "tool"))
			if err != nil || string(got) != "binary" {
				t.Fatalf("tool = %q, %v", got, err)
			}
		})
	}
}
