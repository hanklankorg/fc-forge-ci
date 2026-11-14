// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package docs ships the user-facing documentation pages inside the
// forge-ci binary, so `forge-ci docs <page>` can print them on a machine
// with no network, or with no access to github.com.
//
// Only pages for people who adopt and run Forge CI are embedded. Maintainer
// pages (development, live runs, design policy, ADRs) stay in the
// repository. TestEmbeddedPagesMatchList in this package keeps the list and
// the go:embed directive in step.
package docs

import (
	"embed"
	"io/fs"
	"strings"
)

// Pages lists the embedded pages by command name, in the order
// `forge-ci docs` lists them: the first-steps pages first, then reference.
var Pages = []string{
	"README",
	"tutorial-first-pull-request",
	"github",
	"forgejo",
	"gitlab",
	"quickstart-setup-secrets",
	"setup-requirements",
	"artifacts-reference",
	"verification",
	"reference",
	"publishing",
	"ecosystems",
	"sbom",
	"runtime-images",
	"multiarch-images",
	"private-dependencies",
	"runners",
	"providers",
	"workflows",
	"components",
	"flows",
	"admin-policy",
	"failed-release-recovery",
	"threat-model",
	"cli-reference",
}

//go:embed README.md tutorial-first-pull-request.md github.md forgejo.md gitlab.md
//go:embed quickstart-setup-secrets.md setup-requirements.md artifacts-reference.md
//go:embed verification.md reference.md publishing.md ecosystems.md
//go:embed sbom.md runtime-images.md multiarch-images.md private-dependencies.md runners.md
//go:embed providers.md workflows.md components.md flows.md admin-policy.md
//go:embed failed-release-recovery.md threat-model.md cli-reference.md
var files embed.FS

// Page returns one embedded page by name, with or without the .md suffix.
func Page(name string) (string, bool) {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".md")
	if name == "" || strings.ContainsAny(name, "/\\") {
		return "", false
	}

	body, err := fs.ReadFile(files, name+".md")
	if err != nil {
		return "", false
	}

	return string(body), true
}

// Title returns a page's first "# " heading, or its name when it has none.
func Title(name string) string {
	body, ok := Page(name)
	if !ok {
		return name
	}

	for line := range strings.SplitSeq(body, "\n") {
		if title, found := strings.CutPrefix(line, "# "); found {
			return strings.ReplaceAll(strings.TrimSpace(title), "`", "")
		}
	}

	return name
}
