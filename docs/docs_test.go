// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package docs

import (
	"io/fs"
	"slices"
	"strings"
	"testing"
)

// The list `forge-ci docs` prints and the files the binary embeds are the
// same set: a page added to one and not the other would either be listed
// and missing, or shipped and unfindable.
func TestEmbeddedPagesMatchList(t *testing.T) {
	t.Parallel()

	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		t.Fatal(err)
	}

	embedded := make([]string, 0, len(entries))
	for _, e := range entries {
		embedded = append(embedded, strings.TrimSuffix(e.Name(), ".md"))
	}

	listed := slices.Clone(Pages)
	slices.Sort(listed)
	slices.Sort(embedded)

	if !slices.Equal(listed, embedded) {
		t.Fatalf("Pages %v != embedded %v", listed, embedded)
	}
}

func TestPageRefusesPaths(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"", "../go.mod", "adr/0001", "DEVELOPMENT"} {
		if _, ok := Page(name); ok {
			t.Errorf("Page(%q) found a page", name)
		}
	}

	if _, ok := Page("artifacts-reference.md"); !ok {
		t.Error("Page(artifacts-reference.md) not found")
	}

	if got := Title("artifacts-reference"); got != "Artifacts Reference" {
		t.Errorf("Title = %q", got)
	}
}

// A heading's code spans are kept as text: "# `forge-ci` reference" lists as
// "forge-ci reference".
func TestTitleDropsCodeMarks(t *testing.T) {
	t.Parallel()

	if got := Title("cli-reference"); strings.Contains(got, "`") {
		t.Errorf("Title = %q, want no backticks", got)
	}
}
