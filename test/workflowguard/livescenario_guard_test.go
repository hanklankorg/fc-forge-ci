// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// livetestTree holds the live kit; liveScenarioTrees includes its scenario owners.
const livetestTree = "test/live/internal/livetest"

var liveScenarioTrees = []string{livetestTree, "test/live/conformance", "test/live/githubconsumer"}

// repositoryCreators are the only files that make a creating request for a
// repository: the visibility-aware scratch helpers, and the demos, which are
// public by design. A scenario creates through NewScratchRepo,
// NewBareScratchRepo or a NewPublic* helper, so the run's visibility holds.
var repositoryCreators = map[string]bool{
	livetestTree + "/scratch.go": true,
	livetestTree + "/demo.go":    true,
}

// privilegedDirect are the live files that may ask for the privileged runner
// directly, and why. Every other scenario resolves the run's builder through
// ChooseImageBuilder, or buildahRunner, which refuses kaniko with a reason,
// and a scenario that runs a container itself takes the runner from
// ContainerRunnerLabel, whichever the builder.
var privilegedDirect = map[string]string{
	livetestTree + "/imagebuilder.go":                         "ChooseImageBuilder, the shared builder selection",
	"test/live/conformance/gitlabrelease_test.go":             "the Go CLI fixture's checks are Buildah's own integration tests, its subject",
	"test/live/conformance/gitlabconsumerdevelopment_test.go": "the Buildah branch of its own GitLabKaniko selection",
	"test/live/conformance/layerconsumertrial_test.go":        "Maven Testcontainers need user namespaces to run containers, and the Buildah branch of the trial's CI_IMAGE_BUILDER",
}

// buildahNamed are the live files whose fixtures may name Buildah, and why.
var buildahNamed = map[string]string{
	livetestTree + "/imagebuilder.go":                "Kaniko development admission rejects an explicit setup-buildah action",
	livetestTree + "/imagebuilder_internal_test.go":  "negative admission regression for an explicit setup-buildah action",
	livetestTree + "/nativesnapshotartifact_test.go": "the Buildah capability probe, run only when the selection is Buildah",
	"test/live/conformance/gitlabrelease_test.go":    "the build-image probe checks the build image's own Buildah",
}

// buildahLiteral names Buildah as a build step in a fixture.
var buildahLiterals = []string{"setup-buildah", "buildah build", "manifest push --local-manifest"}

type liveFinding struct {
	path string
	line int
	text string
}

// Every repository a live scenario creates is made by the scratch helpers,
// which hold it to the run's visibility (requireRunVisibility) and a public
// one to a reason (requirePublicReason), and every scenario that builds images
// resolves the run's builder selection. A direct creating request, or a direct
// Buildah requirement outside the reasoned allowlists, fails.
func TestLiveScenariosHonourRunVisibilityAndBuilder(t *testing.T) {
	t.Parallel()

	root := reporoot.Path(t)
	findings, seen := scanLiveScenarios(t, root)

	for _, finding := range findings {
		t.Errorf("%s:%d %s", finding.path, finding.line, finding.text)
	}

	for kind, allowed := range map[string]map[string]string{"privileged:": privilegedDirect, "buildah:": buildahNamed} {
		for path, reason := range allowed {
			if !seen[kind+path] {
				t.Errorf("%s may require Buildah directly (%s) but no longer does; drop it from the allowlist", path, reason)
			}
		}
	}
}

func scanLiveScenarios(t *testing.T, root string) ([]liveFinding, map[string]bool) {
	t.Helper()

	var findings []liveFinding

	seen := map[string]bool{}

	for _, tree := range liveScenarioTrees {
		require.NoError(t, filepath.WalkDir(filepath.Join(root, tree), func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || filepath.Ext(path) != ".go" {
				return err
			}

			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)

			body, readErr := os.ReadFile(path)
			require.NoError(t, readErr)

			found, marks := scanLiveFile(t, rel, string(body))
			findings = append(findings, found...)

			for mark := range marks {
				seen[mark] = true
			}

			return nil
		}))
	}

	return findings, seen
}

func scanLiveFile(t *testing.T, rel, source string) ([]liveFinding, map[string]bool) {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, rel, source, parser.SkipObjectResolution)
	require.NoError(t, err, rel)

	var findings []liveFinding

	seen := map[string]bool{}
	report := func(node ast.Node, text string) {
		findings = append(findings, liveFinding{path: rel, line: fset.Position(node.Pos()).Line, text: text})
	}

	_, privilegedAllowed := privilegedDirect[rel]
	_, buildahAllowed := buildahNamed[rel]

	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.CallExpr:
			checkLiveCall(n, rel, privilegedAllowed, seen, report)
		case *ast.BasicLit:
			checkLiveLiteral(n, rel, buildahAllowed, seen, report)
		}

		return true
	})

	return findings, seen
}

func checkLiveCall(call *ast.CallExpr, rel string, privilegedAllowed bool, seen map[string]bool, report func(ast.Node, string)) {
	switch name := calleeName(call); name {
	case "PrivilegedRunnerLabel":
		seen["privileged:"+rel] = true

		if !privilegedAllowed {
			report(call, "asks for the privileged runner directly; resolve the run's builder with ChooseImageBuilder (or buildahRunner with a reason), so a kaniko run builds with kaniko")
		}
	case "Recreate", "CreateRepo", "CreateProject", "createBareRepo", "createInitialisedRepo", "createPrivateScratchRepo":
		if !repositoryCreators[rel] {
			report(call, name+" creates a repository outside the scratch helpers, past the run's visibility; use NewScratchRepo, NewBareScratchRepo or a NewPublic* helper")
		}
	}
}

func checkLiveLiteral(lit *ast.BasicLit, rel string, buildahAllowed bool, seen map[string]bool, report func(ast.Node, string)) {
	if lit.Kind != token.STRING {
		return
	}

	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return
	}

	for _, literal := range buildahLiterals {
		if strings.Contains(value, literal) {
			seen["buildah:"+rel] = true

			if !buildahAllowed {
				report(lit, "a fixture names Buildah ("+literal+"); build with the selected builder (container build, multiarch-image build-arch) in the builder's job, as the export and image jobs do")
			}
		}
	}

	if !repositoryCreators[rel] && (strings.HasSuffix(value, "/user/repos") || strings.HasSuffix(value, "/api/v4/projects")) {
		report(lit, "a repository-creating endpoint outside the scratch helpers, past the run's visibility")
	}
}

func calleeName(call *ast.CallExpr) string {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name
	case *ast.SelectorExpr:
		return fun.Sel.Name
	}

	return ""
}
