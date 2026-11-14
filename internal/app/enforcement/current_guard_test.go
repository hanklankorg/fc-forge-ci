// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package enforcement_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

const domainEnforcement = "github.com/diggsweden/forge-ci/internal/domain/enforcement"

// The embedded default is not the effective policy: the effective policy is
// the default with this invocation's --enforce-* flags applied, after the
// environment has been refused. Resolve is the one place that does both, so a
// gate that read enforcement.Current itself would ignore the flags and the
// refusal, silently. Only Resolve's file may call it.
func TestOnlyResolveReadsTheEmbeddedDefault(t *testing.T) {
	t.Parallel()

	root := reporoot.Path(t)

	offenders, checked, err := embeddedDefaultReaders(root)
	if err != nil {
		t.Fatal(err)
	}

	if checked < 5 {
		t.Fatalf("only %d files import the enforcement domain; this guard measured nothing", checked)
	}

	if len(offenders) > 0 {
		t.Fatalf("these files read the embedded default instead of appenforcement.Resolve(ctx), so they ignore the --enforce-* flags and the environment refusal: %v", offenders)
	}
}

func embeddedDefaultReaders(root string) ([]string, int, error) {
	const allowed = "internal/app/enforcement/admin.go"

	var offenders []string

	checked := 0

	_, err := reporoot.WalkGoSources(root, func(path, rel string) error {
		if strings.HasSuffix(path, "_test.go") || rel == allowed ||
			strings.HasPrefix(rel, "internal/domain/enforcement/") {
			return nil
		}

		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			return parseErr
		}

		alias := ""

		for _, spec := range file.Imports {
			if importPath, _ := strconv.Unquote(spec.Path.Value); importPath == domainEnforcement {
				alias = "enforcement"
				if spec.Name != nil {
					alias = spec.Name.Name
				}
			}
		}

		if alias == "" {
			return nil
		}

		checked++

		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "Current" {
				return true
			}

			if ident, ok := selector.X.(*ast.Ident); ok && ident.Name == alias {
				offenders = append(offenders, rel)
			}

			return true
		})

		return nil
	})

	return offenders, checked, err
}

func TestEmbeddedDefaultGuardChecksSourceInNestedOutputNamedDirectories(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for _, name := range []string{"plain", "node_modules", "vendor"} {
		dir := filepath.Join(root, "internal", "adapter", name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}

		body := "package fixture\nimport enforcement \"" + domainEnforcement + "\"\nvar load = enforcement.Current\n"
		if err := os.WriteFile(filepath.Join(dir, "reader.go"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	offenders, checked, err := embeddedDefaultReaders(root)
	if err != nil {
		t.Fatal(err)
	}

	if checked != 3 || len(offenders) != 3 {
		t.Fatalf("checked=%d offenders=%v, want all three source readers", checked, offenders)
	}
}
