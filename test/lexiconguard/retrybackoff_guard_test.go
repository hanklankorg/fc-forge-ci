// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package lexiconguard

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/stretchr/testify/require"
)

// retryOwner holds the one retry loop; everything else calls retry.Do/Run.
const retryOwner = "internal/retry/retry.go"

// TestRetryBackoffIsSingleSourced is a lint rule: in a loop body, once an
// `if x == nil { return }` (or `break`) has run, no later statement or else
// branch may wait. A wait is time.Sleep, time.After or time.NewTimer under any
// import name, a local bound to time.Sleep, or a receive from the `.C` of a
// timer assigned in the loop.
// That is a hand-written retry, however its delay is computed. Waits inside
// closures, go and defer statements and nested loops are not judged here.
func TestRetryBackoffIsSingleSourced(t *testing.T) {
	t.Parallel()

	root := reporoot.Path(t)

	var findings []string

	visited, err := reporoot.WalkGoSources(root, func(path, rel string) error {
		fset := token.NewFileSet()

		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}

		waits := retryWaits(file)

		// The owner is exempt because it holds the canonical wait; an
		// exemption for a file that no longer waits guards nothing.
		if rel == retryOwner {
			require.NotEmpty(t, waits, "%s holds no retry wait, so its exemption is stale", retryOwner)

			return nil
		}

		for _, pos := range waits {
			findings = append(findings, fmt.Sprintf("%s:%d", rel, fset.Position(pos).Line))
		}

		return nil
	})
	require.NoError(t, err)
	require.NotZero(t, visited)
	require.Empty(t, findings, "local retry wait outside internal/retry; use retry.Do/Run")
}

func TestRetryWaits(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		imports string
		body    string
		want    int
	}{
		{"sleep", `import "time"`, `for n := 1; n < 4; n++ { if err := op(); err == nil { return }; time.Sleep(time.Duration(n) * delay) }`, 1},
		{"after", `import "time"`, `for { if err := op(); nil == err { break }; <-time.After(delay) }`, 1},
		{"timer", `import clock "time"`, `for { err := op(); if err == nil { return }; timer := clock.NewTimer(delay); select { case <-timer.C: case <-ctx.Done(): return } }`, 2},
		{"dot-import", `import . "time"`, `for { if err := op(); err == nil { break }; Sleep(delay) }`, 1},
		{"alias", `import "time"`, `sleep := time.Sleep; for { if err := op(); err == nil { return }; sleep(delay) }`, 1},
		{"else-branch", `import "time"`, `for { if err := op(); err == nil { return } else { time.Sleep(delay) } }`, 1},
		{"predeclared-result", `import "time"`, `var err error; for range 4 { err = op(); if err == nil { return }; time.Sleep(delay) }`, 1},
		{"timer-made-before-exit", `import "time"`, `for { timer := time.NewTimer(delay); if err := op(); err == nil { return }; <-timer.C }`, 1},
		{"ticker-outside-loop", `import "time"`, `ticker := time.NewTicker(delay); for { if err := op(); err == nil { return }; <-ticker.C }`, 0},
		{"canonical", `import "github.com/diggsweden/forge-ci/internal/retry"`, `retry.Run(ctx, nil, 4, delay, op)`, 0},
		{"pacing", `import "time"`, `for n := range 4 { time.Sleep(time.Duration(n) * delay) }`, 0},
		{"wait-before-exit", `import "time"`, `for { time.Sleep(delay); if err := op(); err == nil { return } }`, 0},
		{"no-loop", `import "time"`, `if err := op(); err == nil { return }; time.Sleep(delay)`, 0},
		{"non-nil-guard", `import "time"`, `for n := range 4 { if n > 2 { return } else { time.Sleep(delay) } }`, 0},
		{"inert", `import "time"`, "_ = time.Second; for { if err := op(); err == nil { return }; _ = `time.Sleep(delay)` }", 0},
		{"shadowed-import", `import "time"`, `time := fakeClock(); for { if err := op(); err == nil { return }; time.Sleep(delay) }`, 0},
		{"not-stdlib", `import "example.invalid/time"`, `for { if err := op(); err == nil { return }; time.Sleep(delay) }`, 0},
		{"deferred", `import "time"`, `for { if err := op(); err == nil { return }; go time.Sleep(delay); defer time.Sleep(delay); _ = func() { time.Sleep(delay) } }`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			source := "package fixture\n" + tc.imports + "\nfunc f() {\n" + tc.body + "\n}"
			file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", source, 0)
			require.NoError(t, err)
			require.Len(t, retryWaits(file), tc.want)
		})
	}
}

func retryWaits(file *ast.File) []token.Pos {
	var found []token.Pos

	ast.Inspect(file, func(node ast.Node) bool {
		var body *ast.BlockStmt

		switch loop := node.(type) {
		case *ast.ForStmt:
			body = loop.Body
		case *ast.RangeStmt:
			body = loop.Body
		default:
			return true
		}

		retrying := false

		for _, stmt := range body.List {
			if guard, ok := stmt.(*ast.IfStmt); ok && nilExit(guard) {
				retrying = true

				if guard.Else != nil {
					found = append(found, waitsIn(file, guard.Else, body)...)
				}

				continue
			}

			if retrying {
				found = append(found, waitsIn(file, stmt, body)...)
			}
		}

		return true
	})

	return found
}

// nilExit reports `if x == nil { … return/break … }`, either operand order.
func nilExit(guard *ast.IfStmt) bool {
	cond, ok := ast.Unparen(guard.Cond).(*ast.BinaryExpr)
	if !ok || cond.Op != token.EQL {
		return false
	}

	left, leftOK := ast.Unparen(cond.X).(*ast.Ident)
	right, rightOK := ast.Unparen(cond.Y).(*ast.Ident)

	if !leftOK || !rightOK || (left.Name == "nil") == (right.Name == "nil") {
		return false
	}

	for _, stmt := range guard.Body.List {
		switch exit := stmt.(type) {
		case *ast.ReturnStmt:
			return true
		case *ast.BranchStmt:
			if exit.Tok == token.BREAK && exit.Label == nil {
				return true
			}
		default:
		}
	}

	return false
}

func waitsIn(file *ast.File, stmt ast.Stmt, loop *ast.BlockStmt) []token.Pos {
	var found []token.Pos

	ast.Inspect(stmt, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.FuncLit, *ast.GoStmt, *ast.DeferStmt, *ast.ForStmt, *ast.RangeStmt:
			return false
		case *ast.CallExpr:
			if timeFunction(file, node.Fun, "Sleep", "After", "NewTimer") || sleepAlias(file, node.Fun) {
				found = append(found, node.Pos())
			}
		case *ast.UnaryExpr:
			if node.Op == token.ARROW && loopTimer(node.X, loop) {
				found = append(found, node.Pos())
			}
		default:
		}

		return true
	})

	return found
}

// loopTimer reports a receive from `t.C` where t is assigned inside the loop,
// which is how a timer made for this turn's wait is read.
func loopTimer(expr ast.Expr, loop *ast.BlockStmt) bool {
	sel, ok := ast.Unparen(expr).(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "C" {
		return false
	}

	ident, ok := sel.X.(*ast.Ident)
	if !ok || ident.Obj == nil {
		return false
	}

	assignment, ok := ident.Obj.Decl.(*ast.AssignStmt)

	return ok && assignment.Pos() > loop.Pos() && assignment.End() < loop.End()
}

// sleepAlias reports a call through a local bound directly to time.Sleep.
func sleepAlias(file *ast.File, fun ast.Expr) bool {
	ident, ok := ast.Unparen(fun).(*ast.Ident)
	if !ok || ident.Obj == nil {
		return false
	}

	assignment, ok := ident.Obj.Decl.(*ast.AssignStmt)

	return ok && len(assignment.Rhs) == 1 && timeFunction(file, assignment.Rhs[0], "Sleep")
}

// timeFunction reports a reference to one of the named functions of the
// standard time package, under the file's import name for it or through a dot
// import, and not through a local that shadows that name. Parse with object
// resolution.
func timeFunction(file *ast.File, expr ast.Expr, names ...string) bool {
	var qualifier, name string

	switch fun := ast.Unparen(expr).(type) {
	case *ast.SelectorExpr:
		id, ok := fun.X.(*ast.Ident)
		if !ok || id.Obj != nil {
			return false
		}

		qualifier, name = id.Name, fun.Sel.Name
	case *ast.Ident:
		if fun.Obj != nil {
			return false
		}

		qualifier, name = ".", fun.Name
	default:
		return false
	}

	for _, spec := range file.Imports {
		if path, err := strconv.Unquote(spec.Path.Value); err != nil || path != "time" {
			continue
		}

		local := "time"
		if spec.Name != nil {
			local = spec.Name.Name
		}

		if local == qualifier && slices.Contains(names, name) {
			return true
		}
	}

	return false
}
