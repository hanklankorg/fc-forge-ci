// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package archguard

import (
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// TestGuardEligibility_EveryProductFileIsJudged keeps the guards from quietly
// covering less of the tree than they appear to.
//
// The source-walking guards parse every product file whatever its build
// constraint. The environment guard and the depguard layer rules load packages
// for linux and darwin only, so a file neither release build compiles is
// invisible to both. Such a variant may therefore import nothing at all: with
// no import it can neither read the environment nor cross a layer.
//
// Generated product files are refused too: a guard finding in one is a report
// against its generator, which no contributor can fix in place.
func TestGuardEligibility_EveryProductFileIsJudged(t *testing.T) {
	t.Parallel()

	root := reporoot.Path(t)
	scanned, unbuilt := 0, 0

	for _, tree := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, tree), func(path string, entry fs.DirEntry, err error) error {
			if err != nil || !isProductGoFile(entry, path) {
				return err
			}

			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly|parser.ParseComments)
			if err != nil {
				return err
			}

			scanned++
			rel, _ := filepath.Rel(root, path)

			if ast.IsGenerated(file) {
				t.Errorf("%s is generated; point the guards at its template, or exclude it and say so in docs/testing.md", rel)
			}

			if expr := buildConstraint(file); expr != nil && !builtByARelease(expr) {
				unbuilt++

				for _, spec := range file.Imports {
					t.Errorf("%s imports %s, but //go:build %s keeps it out of every release build, where the "+
						"environment guard and depguard look. Lint and guard it under a GOOS that builds it.",
						rel, spec.Path.Value, expr)
				}
			}

			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	if scanned < 200 || unbuilt == 0 {
		t.Fatalf("scanned %d product files, %d outside the release builds; the walk, not the tree, is what was measured", scanned, unbuilt)
	}
}

// buildConstraint returns the //go:build line above the package clause, as the
// toolchain reads it, or nil.
func buildConstraint(file *ast.File) constraint.Expr {
	for _, group := range file.Comments {
		if group.Pos() > file.Package {
			break
		}

		for _, comment := range group.List {
			if constraint.IsGoBuild(comment.Text) {
				expr, err := constraint.Parse(comment.Text)
				if err == nil {
					return expr
				}
			}
		}
	}

	return nil
}

// builtByARelease reports whether linux or darwin, on this architecture,
// compiles a file with this constraint.
func builtByARelease(expr constraint.Expr) bool {
	for _, goos := range []string{"linux", "darwin"} {
		if expr.Eval(func(tag string) bool { return tag == goos || tag == "unix" || tag == runtime.GOARCH }) {
			return true
		}
	}

	return false
}

func TestBuiltByARelease(t *testing.T) {
	t.Parallel()

	for expr, want := range map[string]bool{
		"linux": true, "!linux": true, "darwin": true, "unix": true, runtime.GOARCH: true,
		"!unix": false, "linux && plan9": false, "windows": false, "some_tag_nobody_sets": false,
	} {
		parsed, err := constraint.Parse("//go:build " + expr)
		if err != nil {
			t.Fatal(err)
		}

		if got := builtByARelease(parsed); got != want {
			t.Errorf("%q: built by a release = %v, want %v", expr, got, want)
		}
	}

	file, err := parser.ParseFile(token.NewFileSet(), "fixture.go",
		"// SPDX-License-Identifier: CC0-1.0\n\n//go:build !unix\n\npackage p\n\nvar s = `//go:build linux`\n", parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}

	if expr := buildConstraint(file); expr == nil || expr.String() != "!unix" {
		t.Errorf("buildConstraint = %v, want !unix", expr)
	}

	for path, want := range map[string]bool{"internal/app/x.go": true, "internal/app/x_test.go": false, "internal/app/x.md": false, "internal/app/cargo": false} {
		if got := isProductGoFile(fixtureDirEntry{}, path); got != want {
			t.Errorf("isProductGoFile(%q) = %v, want %v", path, got, want)
		}
	}

	if isProductGoFile(fixtureDirEntry{dir: true}, "internal/app/x.go") {
		t.Error("isProductGoFile admitted a directory")
	}
}

type fixtureDirEntry struct{ dir bool }

func (e fixtureDirEntry) Name() string               { return "" }
func (e fixtureDirEntry) IsDir() bool                { return e.dir }
func (e fixtureDirEntry) Type() fs.FileMode          { return 0 }
func (e fixtureDirEntry) Info() (fs.FileInfo, error) { return nil, nil } //nolint:nilnil // fixture; Info is never called.
