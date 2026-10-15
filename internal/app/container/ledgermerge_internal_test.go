// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"errors"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestMergeLedgerDocsFailsClosedWithoutEntries(t *testing.T) {
	t.Parallel()

	emptyDir := t.TempDir()
	if _, _, err := mergeLedgerDocs([]string{emptyDir}); !errors.Is(err, errs.ErrMissingInput) {
		t.Fatalf("empty ledger directory error = %v, want ErrMissingInput", err)
	}

	// A path that is not there at all: the cause must survive so the operator
	// sees "no such file" rather than a bare "merge failed".
	missing := filepath.Join(t.TempDir(), "missing")
	if _, _, err := mergeLedgerDocs([]string{missing}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing ledger input err = %v, want fs.ErrNotExist", err)
	}
}
