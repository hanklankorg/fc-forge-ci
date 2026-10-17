// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package cli_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	urfavecli "github.com/urfave/cli/v3"

	internalcli "github.com/diggsweden/forge-ci/internal/cli"
)

// publicGroups is the top-level command surface, written out independently of
// the command tree so that adding, removing or renaming a group is a visible
// change to this list rather than something the tests absorb.
func publicGroups() []string {
	return []string{
		"artifact", "build", "config", "container", "doctor", "lint", "plan", "platform",
		"publish", "release", "report", "sbom", "security", "toolchain", "validate", "version",
	}
}

// TestRender_AgainstLiveRoot requires the rendered reference to carry exactly
// the public groups as top-level headings, plus representative nested ones.
// The group check was a containment test over a list that had fallen three
// groups behind the tree, so neither a missing nor an extra group failed it.
func TestRender_AgainstLiveRoot(t *testing.T) {
	t.Parallel()

	body := internalcli.Render(internalcli.New(internalcli.BuildInfo{Version: "test"}))

	matches := regexp.MustCompile("(?m)^## `forge-ci ([^ `]+)`$").FindAllStringSubmatch(body, -1)

	groups := make([]string, 0, len(matches))
	for _, match := range matches {
		groups = append(groups, match[1])
	}

	if !slices.Equal(groups, publicGroups()) {
		t.Errorf("top-level headings = %v, want %v", groups, publicGroups())
	}

	for _, want := range []string{
		"### `forge-ci build maven`",
		"#### `forge-ci build maven run`",
		"#### `forge-ci lint swift format-lint`",
		"### `forge-ci sbom assemble`",
		"#### `forge-ci security report upload-sarif`",
		"#### `forge-ci report build android`",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing heading %q", want)
		}
	}
}

// TestRender_TableOfContentsHasOneEntryPerGroup parses the table of contents
// and compares it whole against the visible top-level commands of the live
// tree: one entry each, in heading order, each linking to the anchor its
// heading gets. Hidden commands have no heading, so they must have no entry.
func TestRender_TableOfContentsHasOneEntryPerGroup(t *testing.T) {
	t.Parallel()

	root := internalcli.New(internalcli.BuildInfo{})
	body := internalcli.Render(root)

	tocStart := strings.Index(body, "## Command groups (run by the workflows)\n")
	if tocStart < 0 {
		t.Fatal("no ToC")
	}

	firstHeading := strings.Index(body[tocStart:], "## `forge-ci ")
	if firstHeading < 0 {
		t.Fatal("no group heading after ToC")
	}

	var visible []string

	for _, cmd := range root.Commands {
		if !cmd.Hidden {
			visible = append(visible, cmd.Name)
		}
	}

	slices.Sort(visible)

	if !slices.Equal(visible, publicGroups()) {
		t.Errorf("visible top-level commands = %v, want %v", visible, publicGroups())
	}

	entries := regexp.MustCompile("(?m)^- \\[`([^`]+)`\\]\\(#([^)]*)\\) ").FindAllStringSubmatch(body[tocStart:tocStart+firstHeading], -1)

	names := make([]string, 0, len(entries))
	anchors := make([]string, 0, len(entries))

	for _, match := range entries {
		names = append(names, match[1])
		anchors = append(anchors, match[2])
	}

	if !slices.Equal(names, visible) {
		t.Errorf("ToC entries = %v, want %v", names, visible)
	}

	for i, name := range names {
		if want := "forge-ci-" + name; anchors[i] != want {
			t.Errorf("ToC entry %s links to #%s, want #%s", name, anchors[i], want)
		}
	}
}

// TestRender_DirectCommandsLeadAndLinkToTheirHeadings requires the reference
// to open with the commands a person runs by hand, the same list that leads
// `forge-ci --help`, before the groups the workflows run, and every entry to
// link to a heading the render actually emits.
func TestRender_DirectCommandsLeadAndLinkToTheirHeadings(t *testing.T) {
	t.Parallel()

	body := internalcli.Render(internalcli.New(internalcli.BuildInfo{}))

	direct := strings.Index(body, "## Commands to run directly\n")
	groups := strings.Index(body, "## Command groups (run by the workflows)\n")

	if direct < 0 || groups < 0 || direct > groups {
		t.Fatalf("direct-use section at %d, workflow groups at %d; want both, direct first", direct, groups)
	}

	entries := regexp.MustCompile("(?m)^- \\[`([^`]+)`\\]\\(#([^)]*)\\) ").FindAllStringSubmatch(body[direct:groups], -1)
	if len(entries) != 6 {
		t.Fatalf("direct-use entries = %d, want the six of forge-ci --help", len(entries))
	}

	for _, match := range entries {
		path, anchor := match[1], match[2]

		depth := strings.Count(path, " ") + 2
		if heading := strings.Repeat("#", depth) + " `forge-ci " + path + "`\n"; !strings.Contains(body, heading) {
			t.Errorf("direct-use entry %q has no heading %q", path, heading)
		}

		if want := "forge-ci-" + strings.ReplaceAll(path, " ", "-"); anchor != want {
			t.Errorf("direct-use entry %q links to #%s, want #%s", path, anchor, want)
		}
	}
}

func TestRender_EscapesPipeInUsage(t *testing.T) {
	t.Parallel()

	cmd := &urfavecli.Command{
		Name: "demo",
		Commands: []*urfavecli.Command{
			{
				Name:  "sub",
				Usage: "with a | pipe in it",
				Flags: []urfavecli.Flag{
					&urfavecli.StringFlag{Name: "x", Usage: "a | b"},
				},
			},
		},
	}

	body := internalcli.Render(cmd)
	if !strings.Contains(body, `a \| b`) {
		t.Errorf("pipe in flag usage not escaped:\n%s", body)
	}
}

func TestRender_EscapesMarkdownInUsageParagraphs(t *testing.T) {
	t.Parallel()

	cmd := &urfavecli.Command{
		Name: "demo",
		Commands: []*urfavecli.Command{
			{Name: "sub", Usage: "list *.tgz files under <build-dir>"},
		},
	}

	body := internalcli.Render(cmd)
	if !strings.Contains(body, `list \*.tgz files under &lt;build-dir&gt;`) {
		t.Errorf("markdown-sensitive usage text not escaped:\n%s", body)
	}
}

func TestRender_FillsMissingFlagDescription(t *testing.T) {
	t.Parallel()

	cmd := &urfavecli.Command{
		Name: "demo",
		Commands: []*urfavecli.Command{
			{
				Name: "sub",
				Flags: []urfavecli.Flag{
					&urfavecli.StringFlag{Name: "missing-usage"},
				},
			},
		},
	}

	body := internalcli.Render(cmd)
	if !strings.Contains(body, "| `--missing-usage` | Sets missing usage. | string | `\"\"` | n/a |") {
		t.Errorf("missing fallback flag usage row:\n%s", body)
	}
}

func TestRender_EndsWithSingleNewline(t *testing.T) {
	t.Parallel()

	body := internalcli.Render(&urfavecli.Command{Name: "demo"})
	if !strings.HasSuffix(body, "\n") || strings.HasSuffix(body, "\n\n") {
		t.Errorf("rendered body must end with exactly one newline")
	}
}
