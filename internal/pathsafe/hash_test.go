// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pathsafe_test

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

func TestHashRegularFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "asset.tgz")

	if err := os.WriteFile(path, []byte("asset"), 0o600); err != nil {
		t.Fatal(err)
	}

	digest, size, err := pathsafe.HashRegularFile(path)
	if want := sha256.Sum256([]byte("asset")); err != nil || size != 5 || string(digest) != string(want[:]) {
		t.Fatalf("digest=%x size=%d err=%v", digest, size, err)
	}

	link := filepath.Join(dir, "link.tgz")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}

	if _, _, err := pathsafe.HashRegularFile(link); !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("symlinked file: err=%v, want ErrValidation", err)
	}

	if _, _, err := pathsafe.HashRegularFile(dir); !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("directory: err=%v, want ErrValidation", err)
	}
}
