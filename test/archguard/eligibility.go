// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package archguard enforces where code may live: which layer may read the
// environment, which may mint a credential, which may branch on the platform.
// The import direction between layers (ADR 0004) is enforced by the depguard
// layer-* rules in .golangci.yml, which `just lint` runs.
//
// One of the repo-wide guard packages; docs/testing.md says which is which and
// where a new guard belongs.
package archguard

import (
	"io/fs"
	"strings"
)

// isProductGoFile is the one definition of what every guard in this package
// scans: a Go file that is not a test.
//
// It was written out seven times, once per guard, as the same three-clause
// expression against sometimes a path and sometimes a base name. Nothing
// depended on them being written separately, and everything depends on them
// agreeing — a guard whose filter drifts keeps passing while covering less,
// which is the failure this package exists to prevent elsewhere.
//
// What "eligible" means beyond this is not free-form either: see
// TestGuardEligibility_ConstrainedAndGeneratedProductFilesAreDeclared, which
// holds the build-constrained and generated cases the suffix alone cannot
// judge.
func isProductGoFile(entry fs.DirEntry, path string) bool {
	if entry.IsDir() {
		return false
	}

	return strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go")
}
