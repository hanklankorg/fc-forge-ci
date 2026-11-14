// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package docs_test

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/docs"
	cmddocs "github.com/diggsweden/forge-ci/internal/cli/commands/docs"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestResolveExactPrefixAndAmbiguous(t *testing.T) {
	t.Parallel()

	for query, want := range map[string]string{
		"artifacts-reference":    "artifacts-reference",
		"artifacts":              "artifacts-reference",
		"Verification.md":        "verification",
		"verification#swap":      "verification",
		"tutorial":               "tutorial-first-pull-request",
		"setup-requirements.md ": "setup-requirements", //nolint:gocritic // the trailing space is the case under test.
	} {
		got, err := cmddocs.Resolve(query)
		if err != nil || got != want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", query, got, err, want)
		}
	}

	for _, query := range []string{"r", "nope", "../go.mod"} {
		if _, err := cmddocs.Resolve(query); !errors.Is(err, errs.ErrUsage) {
			t.Errorf("Resolve(%q) err = %v, want usage", query, err)
		}
	}
}

// Offline, a page's relative links name what the reader can run or open: the
// docs command for a page in the binary, the web address for any other file.
func TestOfflineLinks(t *testing.T) {
	t.Parallel()

	body := "[a](verification.md#swap) [b](../examples/README.md) [c](adr/0001-x.md) " +
		"[d](#here) [e](https://example.org/x.md) [f](../../outside.md)"

	got := cmddocs.OfflineLinks(body)

	for _, want := range []string{
		"[a](forge-ci docs verification#swap)",
		"[b](https://github.com/diggsweden/forge-ci/blob/main/examples/README.md)",
		"[c](https://github.com/diggsweden/forge-ci/blob/main/docs/adr/0001-x.md)",
		"[d](#here)", "[e](https://example.org/x.md)", "[f](../../outside.md)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("OfflineLinks lacks %q:\n%s", want, got)
		}
	}
}

// Every page#section link between embedded pages prints that section, so a
// link the offline reader follows never ends in "no section".
func TestEmbeddedSectionLinksResolve(t *testing.T) {
	t.Parallel()

	link := regexp.MustCompile(`\]\(forge-ci docs ([a-z0-9-]+|README)#([^)]+)\)`)

	for _, name := range docs.Pages {
		body, _ := docs.Page(name)

		for _, m := range link.FindAllStringSubmatch(cmddocs.OfflineLinks(body), -1) {
			target, _ := docs.Page(m[1])
			if _, ok := cmddocs.Section(target, m[2]); !ok {
				t.Errorf("%s links %s#%s, which has no such section", name, m[1], m[2])
			}
		}
	}
}

func TestSectionStopsAtTheNextPeerHeadingAndSkipsFences(t *testing.T) {
	t.Parallel()

	body := "# Page\n\n## Quick start\ntext\n```sh\n# not a heading\n```\n### Detail\nmore\n## Next\nother\n"

	got, ok := cmddocs.Section(body, "quick-start")
	if !ok || !strings.Contains(got, "### Detail") || strings.Contains(got, "## Next") {
		t.Errorf("Section = %q, %t", got, ok)
	}

	if _, ok := cmddocs.Section(body, "not-a-heading"); ok {
		t.Error("a comment inside a code fence was taken as a heading")
	}
}
