// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package docslink builds the web links to Forge CI's documentation that
// help text, check remediations and error messages quote. Every link targets
// the git ref of the running binary, so a v0.93.2 binary sends its reader to
// the v0.93.2 docs, which describe the behaviour they are looking at, and not
// to main, which may already describe a later release. Development builds,
// whose version is no release tag, fall back to main.
//
// The same pages ship inside the binary: Offline names the `forge-ci docs`
// command that prints one without a network.
package docslink

import (
	"strings"
	"sync/atomic"
)

// Repository is the published Forge CI repository the links point into.
const Repository = "https://github.com/diggsweden/forge-ci"

const fallbackRef = "main"

var ref atomic.Value

// SetVersion records the running binary's version. cli.New calls it once
// with the ldflags-injected version; a value that is not a release tag
// (for example "dev") keeps the fallback ref.
func SetVersion(version string) {
	ref.Store(RefFor(version))
}

// RefFor returns the git ref the docs of a binary version live at: the
// version itself when it is a release tag (vMAJOR...), otherwise main.
func RefFor(version string) string {
	if len(version) > 1 && version[0] == 'v' && version[1] >= '0' && version[1] <= '9' && !strings.ContainsAny(version, " /") {
		return version
	}

	return fallbackRef
}

// Ref is the git ref the links currently target.
func Ref() string {
	if v, ok := ref.Load().(string); ok && v != "" {
		return v
	}

	return fallbackRef
}

// URL links one page under docs/, with an optional #anchor, for example
// URL("verification.md#swap-policy").
func URL(page string) string {
	return File("docs/" + strings.TrimPrefix(page, "/"))
}

// File links a file by its path in the repository, for example
// File("examples/README.md").
func File(repoPath string) string {
	return Repository + "/blob/" + Ref() + "/" + strings.TrimPrefix(repoPath, "/")
}

// Source links the repository's file tree at the ref, where the reusable
// workflows and actions a help text names can be read.
func Source() string {
	return Repository + "/tree/" + Ref()
}

// Tree links the docs/ directory itself.
func Tree() string {
	return Repository + "/tree/" + Ref() + "/docs"
}

// Offline names the command that prints a page shipped in the binary, for
// example Offline("verification.md#swap-policy") is
// "forge-ci docs verification". The anchor is dropped: the command prints
// the whole page.
func Offline(page string) string {
	name, _, _ := strings.Cut(page, "#")

	return "forge-ci docs " + strings.TrimSuffix(name, ".md")
}

// See renders the usual "see <url> (offline: forge-ci docs <page>)" pointer.
func See(page string) string {
	return "see " + URL(page) + " (offline: " + Offline(page) + ")"
}
