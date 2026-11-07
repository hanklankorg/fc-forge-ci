// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package runbyscan finds which reusable workflows, actions and GitLab
// templates of this repository invoke each forge-ci command. The binary
// cannot read its repository at run time, so cmd/gen-runby writes the
// result into internal/cli/runby.json, and a test regenerates it to catch
// a stale table. Help text then says, under RUN BY, where a step normally
// runs, so a person reading `forge-ci release publish --help` knows it is a
// release-workflow step and where its flags come from.
package runbyscan

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Dirs are the repository directories whose files call the binary as a
// workflow step, relative to the repository root.
var Dirs = []string{
	".github/workflows",
	".github/actions",
	".forgejo/workflows",
	".forgejo/actions",
	".forgejo/bin",
	"templates",
}

// invocation finds the binary at the start of a command: forge-ci, the
// run_forge_ci wrapper the Forgejo actions use, or "${FORGE_CI_BIN}". A
// path to the binary (./cli-prebuilt/forge-ci) counts; a repository
// coordinate (diggsweden/forge-ci/.github/…) does not, since the character
// after it is a slash, not a space.
var invocation = regexp.MustCompile(`(?:^|[\s;&|(` + "`" + `"'=/])(?:run_forge_ci|forge-ci|\$\{?FORGE_CI_BIN\}?"?)[ \t]`)

var word = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Scan returns, for each command path in paths ("build go run"), the
// sorted unit names (Unit) of the files under root that invoke it.
func Scan(root string, paths []string) (map[string][]string, error) {
	known := make(map[string]bool, len(paths))
	for _, p := range paths {
		known[p] = true
	}

	found := map[string][]string{}

	for _, dir := range Dirs {
		base := filepath.Join(root, filepath.FromSlash(dir))
		if _, err := os.Stat(base); err != nil {
			continue
		}

		err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}

			switch filepath.Ext(path) {
			case ".yml", ".yaml", ".sh":
			default:
				return nil
			}

			body, err := os.ReadFile(path) //nolint:gosec // repository files under the fixed scan directories.
			if err != nil {
				return err
			}

			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}

			unit := Unit(filepath.ToSlash(rel))

			for _, cmd := range Invoked(string(body), known) {
				if !slices.Contains(found[cmd], unit) {
					found[cmd] = append(found[cmd], unit)
				}
			}

			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	for cmd := range found {
		slices.Sort(found[cmd])
	}

	return found, nil
}

// Invoked returns the known command paths body invokes: the longest run of
// command words after the binary, skipping --flags, line continuations and
// quotes, that names a known path.
func Invoked(body string, known map[string]bool) []string {
	var out []string

	for _, loc := range invocation.FindAllStringIndex(body, -1) {
		words := commandWords(body[loc[1]:])

		// A global flag before the command may take its value as the next
		// word (--log-level debug release publish), so a later start is tried
		// when the first word starts no known path.
	starts:
		for start := range words {
			for n := len(words); n > start; n-- {
				if path := strings.Join(words[start:n], " "); known[path] {
					out = append(out, path)

					break starts
				}
			}
		}
	}

	return out
}

func commandWords(rest string) []string {
	var words []string

	for _, field := range strings.Fields(rest) {
		field = strings.Trim(field, `"'`)

		switch {
		case field == `\`:
			continue
		case strings.HasPrefix(field, "-"):
			if len(words) > 0 {
				return words
			}

			continue // a global flag before the command
		case word.MatchString(field):
			words = append(words, field)
			if len(words) == 4 {
				return words
			}
		default:
			return words
		}
	}

	return words
}

// Unit names the reusable unit a file belongs to: an action by its
// directory, a workflow or template by its file.
func Unit(rel string) string {
	for _, actions := range []string{".github/actions/", ".forgejo/actions/"} {
		if name, ok := strings.CutPrefix(rel, actions); ok {
			first, _, _ := strings.Cut(name, "/")

			return actions + first
		}
	}

	return rel
}
