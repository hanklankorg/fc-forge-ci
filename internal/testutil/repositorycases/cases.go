// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package repositorycases supplies shared addressing cases for forge adapters
// and bootstrap scripts. Expected outcomes are literals, not parser-derived.
package repositorycases

// Case is a raw repository path and its expected two-segment admission result.
type Case struct {
	Path  string
	Valid bool
}

// OwnerName covers case, punctuation and transport ambiguities. It describes
// safe addressing, not whether a server permits creating a new repository.
func OwnerName() []Case {
	return []Case{
		{Path: "Org/lowercase", Valid: true},
		{Path: "Org/UPPERCASE", Valid: true},
		{Path: "MixedOwner/My.Repo", Valid: true},
		{Path: "Org/snake_case", Valid: true},
		{Path: "Org/kebab-case", Valid: true},
		{Path: "Org/App__Tools", Valid: true},
		{Path: "Org/App___Tools", Valid: true},
		{Path: "Org/.github", Valid: true},
		{Path: "Org/_tools", Valid: true},
		{Path: "Org/-tools", Valid: true},
		{Path: "Org/tools.", Valid: true},
		{Path: "Org/tools-", Valid: true},
		{Path: "Org/tools..v2", Valid: true},
		{Path: "Org/"},
		{Path: "/repo"},
		{Path: "Org//repo"},
		{Path: "Org/."},
		{Path: "Org/.."},
		{Path: "../repo"},
		{Path: "Org/%2e%2e"},
		{Path: "Org/repo%2fother"},
		{Path: "Org/repo?query"},
		{Path: "Org/repo#fragment"},
		{Path: "Org/repo\\app"},
		{Path: "Org/repo\noutput=evil"},
		{Path: "Org/repo name"},
		{Path: "Org/repo:tag"},
		{Path: "Org/repo@digest"},
	}
}
