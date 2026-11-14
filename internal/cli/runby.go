// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package cli

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/cli/runbyscan"
	"github.com/diggsweden/forge-ci/internal/docslink"
)

// runByShown is how many units the RUN BY section names before counting.
const runByShown = 4

// commandPath is a command's path below the root, "build go run".
func commandPath(cmd *cli.Command) string {
	lineage := cmd.Lineage()

	names := make([]string, 0, len(lineage))
	for i := len(lineage) - 2; i >= 0; i-- { // the last entry is the root
		names = append(names, lineage[i].Name)
	}

	return strings.Join(names, " ")
}

// topCategory is the root help category the command sits under.
func topCategory(cmd *cli.Command) string {
	lineage := cmd.Lineage()
	if len(lineage) < 2 {
		return ""
	}

	return lineage[len(lineage)-2].Category
}

// runByText renders the RUN BY section of a leaf workflow step's help, or ""
// for groups and for the commands a person runs at a terminal.
func runByText(data any) string {
	cmd, ok := data.(*cli.Command)
	// VisibleCommands, not Commands: urfave/cli gives every command a hidden
	// help child when it runs, so a leaf has one Command and no visible ones.
	if !ok || len(cmd.VisibleCommands()) > 0 || cmd.Name == "help" || topCategory(cmd) != categoryWorkflow {
		return ""
	}

	path := commandPath(cmd)

	units := runBy[path]
	if len(units) == 0 {
		return "   No reusable workflow of this release runs this command; run it yourself, at a\n" +
			"   terminal or in your own CI. Its flags and environment variables are listed below."
	}

	shown := units
	more := ""

	if len(units) > runByShown {
		shown = units[:runByShown]
		more = fmt.Sprintf(", and %d more", len(units)-runByShown)
	}

	lead := "   A workflow step: "
	if isPorcelain(path) {
		lead = "   Run it yourself; the reusable workflows also run it: "
	}

	return lead + strings.Join(shown, ", ") + more + ".\n" +
		"   There the job sets the flags below from its inputs and environment. To pass the\n" +
		"   same values, read the step that calls it at " + docslink.Source()
}

func isPorcelain(path string) bool {
	return slices.ContainsFunc(porcelainCommands, func(c porcelainCommand) bool { return c.path == path })
}

// runByHint is the extra line a usage error prints for a workflow step, so
// "Required flags not set" at a terminal says where the flags normally come
// from. It names no single workflow: the step usually has one per forge, and
// RUN BY in --help lists them all.
func runByHint(cmd *cli.Command) string {
	if cmd == nil || topCategory(cmd) != categoryWorkflow || len(runBy[commandPath(cmd)]) == 0 {
		return ""
	}

	return "Reusable workflows run this step and set these flags; RUN BY in --help names them."
}

// leafPaths lists every leaf command path of the tree.
func leafPaths(cmd *cli.Command, prefix []string, out *[]string) {
	for _, child := range cmd.Commands {
		path := append(slices.Clone(prefix), child.Name)
		if len(child.Commands) == 0 {
			*out = append(*out, strings.Join(path, " "))

			continue
		}

		leafPaths(child, path, out)
	}
}

// GenerateRunBy scans the repository at root and renders runby.json: one
// command path per line, sorted, so a workflow change shows as a small diff.
func GenerateRunBy(root string) (string, error) {
	var paths []string

	leafPaths(New(BuildInfo{Version: "dev"}), nil, &paths)

	found, err := runbyscan.Scan(root, paths)
	if err != nil {
		return "", err
	}

	keys := make([]string, 0, len(found))
	for k := range found {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	var builder strings.Builder

	builder.WriteString("{\n")

	for i, k := range keys {
		key, err := json.Marshal(k)
		if err != nil {
			return "", err
		}

		units, err := json.Marshal(found[k])
		if err != nil {
			return "", err
		}

		fmt.Fprintf(&builder, "  %s: %s", key, units)

		if i < len(keys)-1 {
			builder.WriteString(",")
		}

		builder.WriteString("\n")
	}

	builder.WriteString("}\n")

	return builder.String(), nil
}

// runByJSON maps a command path to the reusable workflows, actions and
// GitLab templates of this release that invoke it. It is data, written by
// `just gen-runby` (see internal/cli/runbyscan); generated Go is kept
// out of the product tree, which the guard-eligibility test enforces.
//
//go:embed runby.json
var runByJSON []byte

// runBy is runByJSON decoded once; a malformed file is a build defect that
// TestRunByTableInSync reports, so here it only empties the section.
var runBy = func() map[string][]string {
	table := map[string][]string{}
	_ = json.Unmarshal(runByJSON, &table)

	return table
}()
