// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"fmt"
	"io/fs"
	"path"
	"strings"
	"unicode"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// CanonicalDistRoot is the logical dist root, independent of staging location.
const CanonicalDistRoot = "dist"

const canonicalPremadeRoot = "premade-sboms"

// AdmitCanonicalPayload admits the existing signer workspace layout, not an
// archive format. The digest recording is a sibling, never part of this tree.
// Publication ledgers belong to the signer and cannot be supplied by a producer.
func AdmitCanonicalPayload(fsys fs.FS) error {
	if err := WalkSafe(fsys); err != nil {
		return err
	}

	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return err
	}

	dist := false

	for _, entry := range entries {
		if !entry.IsDir() || (entry.Name() != CanonicalDistRoot && entry.Name() != canonicalPremadeRoot) {
			return fmt.Errorf("unexpected canonical payload root %q: %w", entry.Name(), errs.ErrValidation)
		}

		dist = dist || entry.Name() == CanonicalDistRoot
	}

	if !dist {
		return fmt.Errorf("canonical payload requires dist/: %w", errs.ErrValidation)
	}

	return fs.WalkDir(fsys, ".", func(name string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if name != "." && !CanonicalPayloadPath(name) {
			return fmt.Errorf("noncanonical payload path %q: %w", name, errs.ErrValidation)
		}

		switch path.Base(name) {
		case "base-images-publication.json", "release-images-publication.json":
			return fmt.Errorf("producer supplied reserved publication ledger %q: %w", name, errs.ErrValidation)
		}

		return nil
	})
}

// CanonicalPayloadPath reports whether a literal path belongs to the existing
// canonical evidence roots, without normalization, aliases or traversal.
func CanonicalPayloadPath(name string) bool {
	return fs.ValidPath(name) && !strings.Contains(name, "\\") && !strings.ContainsFunc(name, unicode.IsControl) &&
		(name == CanonicalDistRoot || name == canonicalPremadeRoot || strings.HasPrefix(name, CanonicalDistRoot+"/") || strings.HasPrefix(name, canonicalPremadeRoot+"/"))
}
