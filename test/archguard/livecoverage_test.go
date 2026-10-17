// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package archguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// liveDisposition says how the live tier answers for one command group: either
// a conformance scenario that runs it on a real forge, or why none does yet.
type liveDisposition struct {
	scenario string
	gap      string
}

// TestEveryCommandGroupHasALiveDisposition fails when a command group has no
// entry, an entry names a group that is gone, or a named scenario no longer
// exists or no longer holds its group's name as a string literal. The literal
// is a source check, not proof the scenario runs the command.
func TestEveryCommandGroupHasALiveDisposition(t *testing.T) {
	t.Parallel()

	// liveCoverage maps every internal/cli/commands group to its live disposition.
	// A gap is coverage debt, not an exemption and not evidence of parity.
	liveCoverage := map[string]liveDisposition{
		"artifact":  {scenario: "TestArtifact_WithAStore_FailsOnTheRuntimeNotTheCapability"},
		"build":     {gap: "DEFERRED: domain/build -> domain/summary -> provider is package-level awareness; native build verbs still need a verb-level parity decision"},
		"config":    {gap: "DEFERRED: config validation reaches domain/build and provider-backed summary types; no config CLI conformance invocation"},
		"container": {scenario: "TestLedger_Cleanup_RemovesTheCandidateAndKeepsTheRelease"},
		"doctor":    {scenario: "TestJSON_DoctorReport_HasTheSameShapeOnEveryForge"},
		"initcmd":   {gap: "DEFERRED: init reads a mirror anonymously over git and writes local files; unit tests cover it against the repository's own examples, no forge API is involved"},
		"lint":      {gap: "DEFERRED: the Git adapter reaches runcontext; no lint CLI conformance invocation, and native linter parity is not established"},
		"plan":      {gap: "DEFERRED: pipeline planning reaches provider-backed domain types; no plan CLI conformance invocation"},
		"platform":  {scenario: "TestCheckout_ProducesAWorkingTreeAtTheRequestedRef"},
		"publish":   {gap: "OUTSIDE SCANNER: forgepackages_test.go drives in-runner shell probes, not CLI/CLIIn; do not credit shell text or infer coverage of other publish verbs"},
		"release":   {scenario: "TestRelease_PublishReconcile_ConvergesOnTheGivenAssets"},
		"report":    {gap: "MISSING: report/status imports provider, but adapter capability checks and helper labels are not a report CLI scenario"},
		"sbom":      {gap: "DEFERRED: cosign -> domain/container -> provider is package-level awareness; no sbom CLI conformance invocation"},
		"security":  {scenario: "TestDegradation_SARIFUpload_NoticesAndSucceeds"},
		"toolchain": {gap: "DEFERRED: cienv reaches runcontext; no toolchain CLI conformance invocation, and native installer parity is not established"},
		"validate":  {scenario: "TestToken_RejectedCredential_FailsTheCommandWithAReason"},
		"version":   {gap: "MISSING (D179): commit-changelog-release reads runcontext and performs signed Git mutations; JSON version fields are not coverage; a separately authorized live scenario remains unimplemented"},
	}

	root := reporoot.Path(t)

	entries, err := os.ReadDir(filepath.Join(root, "internal", "cli", "commands"))
	if err != nil {
		t.Fatal(err)
	}

	groups := map[string]bool{}

	for _, entry := range entries {
		if entry.IsDir() {
			groups[entry.Name()] = true
			if _, ok := liveCoverage[entry.Name()]; !ok {
				t.Errorf("command group %q has no live disposition: name a conformance scenario or the gap", entry.Name())
			}
		}
	}

	scenarios := conformanceFuncs(t, filepath.Join(root, "internal", "livetest", "conformance"))

	for group, disposition := range liveCoverage {
		if !groups[group] {
			t.Errorf("stale live disposition for %q: internal/cli/commands/%s is gone", group, group)
		}

		if (disposition.scenario == "") == (disposition.gap == "") {
			t.Errorf("%s: name exactly one of a scenario or a gap", group)

			continue
		}

		if disposition.scenario == "" {
			continue
		}

		body, ok := scenarios[disposition.scenario]
		if !ok {
			t.Errorf("%s: scenario %s is not in internal/livetest/conformance", group, disposition.scenario)

			continue
		}

		if !holdsStringLiteral(body, group) {
			t.Errorf("%s: scenario %s no longer holds the literal %q", group, disposition.scenario, group)
		}
	}
}

// conformanceFuncs parses every test file in dir, whatever its build tags,
// and returns each top-level function body by name.
func conformanceFuncs(t *testing.T, dir string) map[string]*ast.BlockStmt {
	t.Helper()

	paths, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no conformance sources in %s: %v", dir, err)
	}

	funcs := map[string]*ast.BlockStmt{}

	fset := token.NewFileSet()
	for _, path := range paths {
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}

		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Body != nil {
				funcs[fn.Name.Name] = fn.Body
			}
		}
	}

	return funcs
}

func holdsStringLiteral(body *ast.BlockStmt, want string) bool {
	found := false

	ast.Inspect(body, func(node ast.Node) bool {
		if lit, ok := node.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if value, err := strconv.Unquote(lit.Value); err == nil && value == want {
				found = true
			}
		}

		return !found
	})

	return found
}
