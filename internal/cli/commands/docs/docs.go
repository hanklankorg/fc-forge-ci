// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package docs wires `forge-ci docs`: the documentation pages the binary
// ships, printed without a network, plus the web link of the same page at
// the binary's own release.
package docs

import (
	"context"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/docs"
	"github.com/diggsweden/forge-ci/internal/docslink"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// New returns the `docs` top-level command.
func New() *cli.Command {
	return &cli.Command{
		Name:      "docs",
		Usage:     "print the documentation shipped in this binary (artifacts.yml reference, forge guides, …)",
		ArgsUsage: "[page]",
		Description: `Without a page, lists the pages this binary carries. With one, prints that
page as Markdown; a unique prefix is enough ("artifacts" finds
artifacts-reference). The pages are the docs of the release this binary was
built from, so they describe the behaviour of this binary.

EXAMPLES:
   # What is there:
   forge-ci docs

   # Every artifacts.yml field:
   forge-ci docs artifacts-reference | less

   # One section, as a page's links name it:
   forge-ci docs forgejo#quick-start

   # The same page on the web, at this binary's release:
   forge-ci docs artifacts-reference --web`,
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "web", Usage: "print the page's web address at this binary's release instead of the page"},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			if cmd.NArg() > 1 {
				return fmt.Errorf("docs takes at most one page: %w", errs.ErrUsage)
			}

			if cmd.NArg() == 0 {
				list(cmd.Writer)

				return nil
			}

			name, err := Resolve(cmd.Args().First())
			if err != nil {
				return err
			}

			_, anchor, _ := strings.Cut(cmd.Args().First(), "#")

			if cmd.Bool("web") {
				page := name + ".md"
				if anchor != "" {
					page += "#" + anchor
				}

				_, _ = fmt.Fprintln(cmd.Writer, docslink.URL(page))

				return nil
			}

			body, _ := docs.Page(name)
			if anchor != "" {
				part, ok := Section(body, anchor)
				if !ok {
					return fmt.Errorf("%s has no section #%s; run forge-ci docs %s for the whole page: %w", name, anchor, name, errs.ErrUsage)
				}

				body = part
			}

			_, _ = io.WriteString(cmd.Writer, OfflineLinks(body))

			return nil
		},
	}
}

// Resolve finds a page by exact name or unique prefix. A #section suffix, as
// the links OfflineLinks writes carry, is not part of the name; see Section.
func Resolve(query string) (string, error) {
	query, _, _ = strings.Cut(query, "#")

	query = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(query)), ".md")
	if _, ok := docs.Page(query); ok {
		return query, nil
	}

	var matches []string

	for _, name := range docs.Pages {
		if strings.HasPrefix(strings.ToLower(name), query) {
			matches = append(matches, name)
		}
	}

	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("no page %q; run forge-ci docs to list them: %w", query, errs.ErrUsage)
	default:
		return "", fmt.Errorf("%q matches %s; name one: %w", query, strings.Join(matches, ", "), errs.ErrUsage)
	}
}

// Section returns the part of a Markdown page under the heading whose anchor
// is anchor, up to the next heading of the same or a higher level. Anchors are
// GitHub's, the ones the pages link with; lines inside code fences are not
// headings.
func Section(body, anchor string) (string, bool) {
	lines := strings.SplitAfter(body, "\n")
	start, level := -1, 0
	fenced := false

	for index, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced

			continue
		}

		depth := len(line) - len(strings.TrimLeft(line, "#"))
		if fenced || depth == 0 || depth > 6 || !strings.HasPrefix(line[depth:], " ") {
			continue
		}

		switch {
		case start >= 0 && depth <= level:
			return strings.Join(lines[start:index], ""), true
		case start < 0 && headingAnchor(line[depth:]) == anchor:
			start, level = index, depth
		}
	}

	if start < 0 {
		return "", false
	}

	return strings.Join(lines[start:], ""), true
}

// headingAnchor is GitHub's anchor for a heading's text: lower case, spaces
// as hyphens, and every character but letters, digits, hyphens and
// underscores dropped.
func headingAnchor(text string) string {
	var b strings.Builder

	for _, r := range strings.ToLower(strings.TrimSpace(text)) {
		switch {
		case r == ' ':
			b.WriteRune('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}

	return b.String()
}

var markdownLink = regexp.MustCompile(`\]\(([^)\s]+)\)`)

// OfflineLinks rewrites a page's relative links for a terminal reader: a
// link to another page in the binary becomes the forge-ci docs command that
// prints it (keeping its #section), and a link to any other repository file
// becomes its web address at this binary's release. Web links and in-page
// #anchors are left as they are.
func OfflineLinks(body string) string {
	return markdownLink.ReplaceAllStringFunc(body, func(link string) string {
		target := link[2 : len(link)-1]
		if target == "" || strings.HasPrefix(target, "#") || strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
			return link
		}

		file, anchor, _ := strings.Cut(target, "#")
		if name, ok := strings.CutSuffix(file, ".md"); ok && slices.Contains(docs.Pages, name) {
			if anchor != "" {
				name += "#" + anchor
			}

			return "](forge-ci docs " + name + ")"
		}

		repoPath := path.Clean(path.Join("docs", file))
		if strings.HasPrefix(repoPath, "../") {
			return link
		}

		if anchor != "" {
			repoPath += "#" + anchor
		}

		return "](" + docslink.File(repoPath) + ")"
	})
}

func list(out io.Writer) {
	_, _ = fmt.Fprintln(out, "Documentation in this binary (forge-ci docs <page>):")
	_, _ = fmt.Fprintln(out)

	for _, name := range docs.Pages {
		_, _ = fmt.Fprintf(out, "  %-28s %s\n", name, docs.Title(name))
	}

	_, _ = fmt.Fprintf(out, "\nOn the web, at this binary's release: %s\n", docslink.Tree())
}
