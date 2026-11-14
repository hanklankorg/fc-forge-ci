// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package archguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// Process creation stays in the reviewed environment-policy owners. A new
// execution path must extend both this inventory and subprocess behavior tests.
func TestSigningAndBuilderProcessesUseTheReviewedEnvironmentOwners(t *testing.T) {
	t.Parallel()
	root := filepath.Join(reporoot.Path(t), "internal", "adapters")
	owners := map[string]map[string]string{
		"cosign":  {"runCaptured": "verificationEnv"},
		"kaniko":  {"run": "ImageBuilderEnv"},
		"buildah": {"runWithCommandEnv": "", "outputBytesWithEnv": "ImageBuilderEnv"},
	}

	for adapter, allowed := range owners {
		t.Run(adapter, func(t *testing.T) {
			t.Parallel()

			seen := map[string]bool{}

			err := filepath.WalkDir(filepath.Join(root, adapter), func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}

				if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
					return nil
				}

				source, err := os.ReadFile(path)
				if err != nil {
					return err
				}

				file, err := parser.ParseFile(token.NewFileSet(), path, source, 0)
				if err != nil {
					return err
				}

				for _, decl := range file.Decls {
					fn, ok := decl.(*ast.FuncDecl)
					if !ok || fn.Body == nil {
						continue
					}

					creates, assignsEnv, policy := false, false, false

					ast.Inspect(fn.Body, func(node ast.Node) bool {
						switch node := node.(type) {
						case *ast.CallExpr:
							name := ""

							switch call := node.Fun.(type) {
							case *ast.SelectorExpr:
								name = call.Sel.Name
							case *ast.Ident:
								name = call.Name
							}

							creates = creates || name == "Command" || name == "CommandContext"
							policy = policy || name == allowed[fn.Name.Name]
						case *ast.AssignStmt:
							for _, lhs := range node.Lhs {
								if selector, ok := lhs.(*ast.SelectorExpr); ok && selector.Sel.Name == "Env" {
									assignsEnv = true
								}
							}
						}

						return true
					})

					if !creates {
						continue
					}

					want, admitted := allowed[fn.Name.Name]
					if !admitted || !assignsEnv || (want != "" && !policy) {
						t.Errorf("%s: %s creates a process outside its reviewed environment policy", path, fn.Name.Name)
					}

					seen[fn.Name.Name] = true
				}

				return nil
			})
			if err != nil {
				t.Fatal(err)
			}

			for owner := range allowed {
				if !seen[owner] {
					t.Errorf("reviewed process owner %s disappeared; update the policy inventory", owner)
				}
			}
		})
	}
}
