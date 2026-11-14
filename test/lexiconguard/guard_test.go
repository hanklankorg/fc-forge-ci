// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package lexiconguard keeps a spelling, a regex, or a pattern literal
// declared exactly once in the tree.
//
// Each guard names one canonical definition, then walks the repository for
// anything that re-states it. They clear the bar ADR 0003 §3 sets: each pins a
// fact with one correct value rather than a judgement, and a second copy is
// expensive to reverse because the two drift apart silently. A digest regex
// that accepts one more character in one place than another is a validation
// hole, not a style problem.
//
// One of the repo-wide guard packages; docs/testing.md says which is which and
// where a new guard belongs.
package lexiconguard

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/stretchr/testify/require"
)

// singleSource is one pattern literal that may appear, as text anywhere in a
// Go file, only in the files that own it: the package declaring the canonical
// value and this file. A comment restating the pattern counts too; it is cheap
// to reword, and text matching also catches a validator built from a constant
// declared elsewhere.
//
// This does not prove that every runtime check uses the canonical API, or
// detect an equivalent regex spelled differently.
type singleSource struct {
	pattern string
	owner   string // repo-relative file declaring the canonical value
	remedy  string
}

func singleSources() []singleSource {
	return []singleSource{
		{
			// Full-length git commit hash. Before consolidation it had drifted
			// into strict (`40|64`) and loose (`40,64`) spellings.
			pattern: "^([0-9a-f]{40}|[0-9a-f]{64})$",
			owner:   "internal/domain/git/sha.go",
			remedy:  "commit-SHA regex re-declared outside domain/git; call git.ValidCommitSHA instead",
		},
		{
			// Bare sha256 hex. The caret sits directly before the hex class,
			// which the digest-pinned-ref patterns never do.
			pattern: "^[0-9a-f]{64}$",
			owner:   "internal/domain/container/ref.go",
			remedy:  "bare sha256-hex regex re-declared outside domain/container; call container.ValidSHA256Hex instead",
		},
		{
			// Prefixed OCI content digest, anchored before the prefix.
			pattern: "^sha256:[0-9a-f]{64}$",
			owner:   "internal/domain/container/ref.go",
			remedy:  "canonical sha256-digest regex re-declared outside domain/container; call container.ValidDigest instead",
		},
		{
			// The unanchored OCI tag-component charset, designed to be spliced
			// into larger patterns as container.OCITagComponent.
			pattern: "[A-Za-z0-9_][A-Za-z0-9._-]{0,127}",
			owner:   "internal/domain/container/ref.go",
			remedy: "OCI tag-component charset re-declared outside domain/container; " +
				"embed container.OCITagComponent (or call ValidOCITagComponent) instead",
		},
	}
}

const guardFile = "test/lexiconguard/guard_test.go"

func TestPatternLiteralsAreSingleSourced(t *testing.T) {
	t.Parallel()

	root := reporoot.Path(t)

	for _, rule := range singleSources() {
		// An owner is exempt only because it declares the canonical value.
		// Once that declaration moves, the exemption hides a file the rule no
		// longer has a reason to skip.
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rule.owner)))
		require.NoError(t, err)
		require.Containsf(t, string(content), rule.pattern, "%s no longer declares %q; move the owner", rule.owner, rule.pattern)

		if offenders := literalOffenders(t, root, rule.pattern, rule.owner, guardFile); len(offenders) > 0 {
			t.Errorf("%s: %v", rule.remedy, offenders)
		}
	}
}

func TestLiteralOffenders_FindsThePatternAnywhereInAFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "internal", "app", "poison.go")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))

	for body, want := range map[string][]string{
		"package clean\n":                          nil,
		"package poison\n// ^[0-9a-f]{64}$\n":      {"internal/app/poison.go"},
		"package poison\nvar _ = `^[0-9a-f]{64}$`": {"internal/app/poison.go"},
	} {
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
		require.Equal(t, want, literalOffenders(t, root, "^[0-9a-f]{64}$"))
		require.Empty(t, literalOffenders(t, root, "^[0-9a-f]{64}$", "internal/app/poison.go"))
	}
}

// literalOffenders returns every Go file under root, other than owners, whose
// text contains pattern.
func literalOffenders(t *testing.T, root, pattern string, owners ...string) []string {
	t.Helper()

	var offenders []string

	visited, err := reporoot.WalkGoSources(root, func(path, rel string) error {
		if slices.Contains(owners, rel) {
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		if strings.Contains(string(content), pattern) {
			offenders = append(offenders, rel)
		}

		return nil
	})
	require.NoError(t, err)
	require.NotZero(t, visited, "lexicon guard inspected no Go files")

	return offenders
}
