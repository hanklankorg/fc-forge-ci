// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package archguard

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

const execSafePackage = "github.com/diggsweden/forge-ci/internal/safeexec"

// TestExecAdaptersClassifyStartFailures protects each subprocess's non-exit
// failure path: a missing binary must not escape as an unclassified error (70).
func TestExecAdaptersClassifyStartFailures(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(reporoot.Path(t), "internal", "adapters")

	checked, offenders, err := execAdapterUsage(os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}

	if checked == 0 {
		t.Fatalf("no exec calls found under %s; the guard is looking in the wrong place", dir)
	}

	if len(offenders) > 0 {
		t.Fatalf("exec calls without returned start-failure classification:\n%s\n"+
			"Return safeexec.WrapError (or a checked helper) for this process's non-exit error.", strings.Join(offenders, "\n"))
	}

	t.Logf("checked %d process call sites recursively", checked)
}

func execAdapterUsage(tree fs.FS) (int, []string, error) {
	fset := token.NewFileSet()
	packages := map[string][]*ast.File{}

	err := fs.WalkDir(tree, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if !isProductGoFile(entry, name) {
			return nil
		}

		source, readErr := fs.ReadFile(tree, name)
		if readErr != nil {
			return readErr
		}

		file, parseErr := parser.ParseFile(fset, name, source, 0)
		if parseErr != nil {
			return parseErr
		}

		key := path.Dir(name) + "/" + file.Name.Name
		packages[key] = append(packages[key], file)

		return nil
	})
	if err != nil {
		return 0, nil, fmt.Errorf("scan exec adapters: %w", err)
	}

	checked := 0

	var offenders []string

	for _, files := range packages {
		functions := execFunctions(files)
		for _, fn := range functions {
			probe := execCheck{functions: functions, sites: map[token.Pos]string{}}
			probe.block(fn, fn.body.List, []execPath{{vars: map[*execBinding]execValue{}}}, 0)

			for pos, method := range probe.sites {
				checked++
				check := execCheck{functions: functions, target: pos, sites: map[token.Pos]string{}}
				paths := check.block(fn, fn.body.List, []execPath{{vars: map[*execBinding]execValue{}}}, 0)
				reason := ""

				if !check.exhausted && !slices.ContainsFunc(paths, func(p execPath) bool {
					reason = p.reason

					return p.active && (p.result.kind != "wrapped" || p.unsupported)
				}) {
					continue
				}

				offender := fmt.Sprintf("%s: %s: %s has an unclassified or unsupported non-exit failure path",
					fset.Position(pos), fn.name, method)
				if reason != "" {
					offender += ": " + reason
				}

				offenders = append(offenders, offender)
			}

			if probe.exhausted {
				offenders = append(offenders, fmt.Sprintf("%s: %s: exec discovery exceeded its analysis bound", fset.Position(fn.body.Pos()), fn.name))
			}
		}
	}

	slices.Sort(offenders)

	return checked, offenders, nil
}

type execFunction struct {
	file *ast.File
	decl *ast.FuncDecl
	body *ast.BlockStmt
	name string
}

func execFunctions(files []*ast.File) []*execFunction {
	var functions []*execFunction

	for _, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.FuncDecl:
				if node.Body != nil {
					name := node.Name.Name
					if node.Recv != nil {
						name = execReceiver(node) + "." + name
					}

					functions = append(functions, &execFunction{file, node, node.Body, name})
				}
			case *ast.FuncLit:
				functions = append(functions, &execFunction{file, nil, node.Body, "func literal"})
			default:
			}

			return true
		})
	}

	return functions
}

type execValue struct {
	kind   string
	helper *execFunction
}

// Parser bindings suffice for lexical scalar locals here, not composite keys
// or selector types. No type-dependent binding conclusions are made from them.
//
//lint:ignore SA1019 bounded lexical analysis over parser bindings; see above.
type execBinding = ast.Object //nolint:staticcheck // bounded lexical analysis; see above.

type execPath struct {
	vars        map[*execBinding]execValue
	active      bool
	done        bool
	unsupported bool
	reason      string
	result      execValue
}

type execCheck struct {
	functions []*execFunction
	target    token.Pos
	sites     map[token.Pos]string
	steps     int
	exhausted bool
}

// This is a bounded source-pattern check, not a Go CFG or a type checker.
// Replay each process independently with a non-nil, non-ExitError result;
// unknown conditions take both arms. Bindings are parser objects, not names.
// Only local scalar aliases, if/return paths, same-receiver command builders,
// and unambiguous package-local error helpers are followed. Unknown operands
// and control initializers are inspected for discovery, not semantic proof.
// Loops/switches cannot establish classification, closures cannot classify their
// enclosing function, and reaching a bound fails rather than granting coverage.
func (c *execCheck) block(fn *execFunction, stmts []ast.Stmt, paths []execPath, depth int) []execPath {
	for _, stmt := range stmts {
		var next []execPath

		for _, provider := range paths {
			c.steps++
			if c.steps > 20000 || depth > 8 || len(paths) > 256 {
				c.exhausted = true

				return paths
			}

			if provider.done {
				next = append(next, provider)

				continue
			}

			next = append(next, c.statement(fn, stmt, provider, depth)...)
		}

		paths = next
	}

	return paths
}

func (c *execCheck) statement(fn *execFunction, stmt ast.Stmt, flow execPath, depth int) []execPath {
	switch stmt := stmt.(type) {
	case *ast.AssignStmt:
		c.assign(fn, stmt.Lhs, stmt.Rhs, &flow, depth)
	case *ast.DeclStmt:
		if decl, ok := stmt.Decl.(*ast.GenDecl); ok {
			for _, spec := range decl.Specs {
				if spec, ok := spec.(*ast.ValueSpec); ok {
					lhs := make([]ast.Expr, len(spec.Names))
					for i, id := range spec.Names {
						lhs[i] = id
					}

					c.assign(fn, lhs, spec.Values, &flow, depth)
				}
			}
		}
	case *ast.ExprStmt:
		c.value(fn, stmt.X, &flow, depth)
	case *ast.ReturnStmt:
		for _, result := range stmt.Results {
			flow.result = c.value(fn, result, &flow, depth)
		}

		flow.done = true
	case *ast.BlockStmt:
		return c.block(fn, stmt.List, []execPath{flow}, depth)
	case *ast.IfStmt:
		return c.branches(fn, stmt, flow, depth)
	default:
		c.uncertain(fn, stmt, &flow, depth)
	}

	return []execPath{flow}
}

func (c *execCheck) uncertain(fn *execFunction, stmt ast.Stmt, flow *execPath, depth int) {
	// Bind only the initializer, without inferring iteration or case semantics.
	var init ast.Stmt

	switch stmt := stmt.(type) {
	case *ast.ForStmt:
		init = stmt.Init
	case *ast.SwitchStmt:
		init = stmt.Init
	case *ast.TypeSwitchStmt:
		init = stmt.Init
	default:
	}

	if init != nil {
		*flow = c.statement(fn, init, *flow, depth)[0]
	}
	// Inspect unsupported control for discovery, but never use it as proof.
	ast.Inspect(stmt, func(node ast.Node) bool {
		_, closure := node.(*ast.FuncLit)
		if node == init || closure {
			return false
		}

		var body []ast.Stmt

		switch node := node.(type) {
		case *ast.BlockStmt:
			body = node.List
		case *ast.CaseClause:
			body = node.Body
		case *ast.CommClause:
			body = node.Body
		default:
		}

		if body != nil {
			arm := *flow

			arm.vars = maps.Clone(flow.vars)
			for _, outcome := range c.block(fn, body, []execPath{arm}, depth+1) {
				flow.active = flow.active || outcome.active
			}

			return false
		}

		if call, ok := node.(*ast.CallExpr); ok {
			c.value(fn, call, flow, depth)

			return false
		}

		return true
	})
	// Unsupported control can overwrite an alias even before a process starts.
	ast.Inspect(stmt, func(node ast.Node) bool {
		if _, ok := node.(*ast.FuncLit); ok {
			return false
		}

		assignment, ok := node.(*ast.AssignStmt)
		if !ok {
			return true
		}

		for _, lhs := range assignment.Lhs {
			id, ok := lhs.(*ast.Ident)
			if !ok || flow.vars[id.Obj] == (execValue{}) {
				continue
			}

			flow.unsupported = true
			if flow.vars[id.Obj].kind != "command" {
				flow.vars[id.Obj] = execValue{}
			}
		}

		return true
	})

	flow.unsupported = flow.unsupported || flow.active
	if flow.unsupported {
		flow.reason = "unsupported control flow"
	}
}

func (c *execCheck) assign(fn *execFunction, lhs, rhs []ast.Expr, flow *execPath, depth int) {
	values := make([]execValue, len(rhs))
	for i, expr := range rhs {
		values[i] = c.value(fn, expr, flow, depth)
	}

	for index, expr := range lhs {
		id, ok := expr.(*ast.Ident)
		if !ok || id.Obj == nil {
			continue
		}

		value := execValue{}
		if len(values) == len(lhs) {
			value = values[index]
		} else if index == len(lhs)-1 && len(values) == 1 {
			value = values[0] // Run/output/helper errors are the final result.
		}

		flow.vars[id.Obj] = value
	}
}

func (c *execCheck) branches(fn *execFunction, stmt *ast.IfStmt, flow execPath, depth int) []execPath {
	// The replayed result is never an ExitError, so this arm is not taken.
	if c.exitAsType(fn, stmt, &flow, depth) {
		if stmt.Else != nil {
			return c.statement(fn, stmt.Else, flow, depth)
		}

		return []execPath{flow}
	}

	if stmt.Init != nil {
		flow = c.statement(fn, stmt.Init, flow, depth)[0]
	}

	truth := c.condition(fn, stmt.Cond, &flow, depth)

	var branches []execPath

	if truth != "false" {
		arm := flow
		arm.vars = maps.Clone(flow.vars)
		branches = append(branches, c.block(fn, stmt.Body.List, []execPath{arm}, depth)...)
	}

	if truth != "true" {
		if stmt.Else != nil {
			branches = append(branches, c.statement(fn, stmt.Else, flow, depth)...)
		} else {
			branches = append(branches, flow)
		}
	}

	return branches
}

// exitAsType matches `target, ok := errors.AsType[*exec.ExitError](raw); ok`,
// the generic form of the errors.As exit test in condition.
func (c *execCheck) exitAsType(fn *execFunction, stmt *ast.IfStmt, flow *execPath, depth int) bool {
	assign, ok := stmt.Init.(*ast.AssignStmt)
	if !ok || assign.Tok != token.DEFINE || len(assign.Lhs) != 2 || len(assign.Rhs) != 1 {
		return false
	}

	okName, isIdent := assign.Lhs[1].(*ast.Ident)
	cond, isCond := ast.Unparen(stmt.Cond).(*ast.Ident)

	if !isIdent || !isCond || okName.Obj == nil || cond.Obj != okName.Obj {
		return false
	}

	call, ok := assign.Rhs[0].(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return false
	}

	generic, ok := call.Fun.(*ast.IndexExpr)
	if !ok || c.value(fn, generic.X, flow, depth).kind != "errors.AsType" {
		return false
	}

	target, ok := generic.Index.(*ast.StarExpr)

	return ok && execImported(fn.file, target.X) == "os/exec.ExitError" &&
		c.value(fn, call.Args[0], flow, depth).kind == "raw"
}

// nonCallValue resolves an expression that is not a call: a process method
// on a command, or an operand that may hold a helper.
func (c *execCheck) nonCallValue(fn *execFunction, expr ast.Expr, flow *execPath, depth int) execValue {
	if sel, ok := expr.(*ast.SelectorExpr); ok &&
		slices.Contains([]string{"Run", "Output", "CombinedOutput", "Start", "Wait"}, sel.Sel.Name) &&
		c.value(fn, sel.X, flow, depth).kind == "command" {
		return execValue{kind: "process." + sel.Sel.Name}
	}

	c.operands(fn, expr, flow, depth)

	return c.helper(fn, expr)
}

func (c *execCheck) value(fn *execFunction, expr ast.Expr, flow *execPath, depth int) execValue {
	expr = ast.Unparen(expr)
	if id, ok := expr.(*ast.Ident); ok && id.Obj != nil {
		if v, found := flow.vars[id.Obj]; found {
			return v
		}
	}

	if imported := execImported(fn.file, expr); imported != "" {
		return execValue{kind: imported}
	}

	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return c.nonCallValue(fn, expr, flow, depth)
	}

	callee := c.value(fn, call.Fun, flow, depth)

	args := make([]execValue, len(call.Args))
	for i, arg := range call.Args {
		args[i] = c.value(fn, arg, flow, depth)
	}

	if strings.HasPrefix(callee.kind, "ambiguous ") {
		flow.unsupported = true
		flow.reason = "ambiguous helper declaration"

		return execValue{kind: strings.TrimPrefix(callee.kind, "ambiguous ")}
	}

	if callee.kind == execSafePackage+".Command" {
		return execValue{kind: "command"}
	}

	if method, ok := strings.CutPrefix(callee.kind, "process."); ok {
		c.sites[call.Pos()] = method
		if call.Pos() == c.target {
			flow.active = true

			return execValue{kind: "raw"}
		}
	}

	if (callee.kind == execSafePackage+".WrapError" || callee.kind == execSafePackage+".WrapErrorWithStderr") &&
		len(args) > 0 && (args[0].kind == "raw" || args[0].kind == "wrapped") {
		return execValue{kind: "wrapped"}
	}

	if callee.kind == "fmt.Errorf" && execWrappedFormat(call, args) {
		return execValue{kind: "wrapped"}
	}

	if callee.kind == "fmt.Errorf" && flow.active {
		flow.reason = "unsupported error format"
	}

	if callee.helper != nil && (execCommandBuilder(callee.helper) || slices.ContainsFunc(args, func(v execValue) bool {
		return v.kind == "raw" || v.kind == "wrapped"
	})) {
		result := c.helperResult(callee.helper, args, depth+1)
		if execCommandBuilder(callee.helper) && result.kind != "command" {
			flow.unsupported = true

			return execValue{kind: "command"} // Do not lose the caller's process site.
		}

		return result
	}

	return execValue{}
}

// Unknown expression semantics must not hide calls in operands or receivers.
// Discover them, but do not infer short-circuit truth or error preservation.
func (c *execCheck) operands(fn *execFunction, expr ast.Expr, flow *execPath, depth int) {
	ast.Inspect(expr, func(node ast.Node) bool {
		if _, ok := node.(*ast.FuncLit); ok {
			return false
		}

		if node == expr {
			return true
		}

		operand, ok := node.(ast.Expr)
		if !ok {
			return true
		}

		active := flow.active

		v := c.value(fn, operand, flow, depth)
		if (!active && flow.active) || v.kind == "raw" || v.kind == "wrapped" {
			flow.unsupported = true
			flow.reason = "unsupported expression"
		}

		return false
	})
}

func (c *execCheck) helperResult(fn *execFunction, args []execValue, depth int) execValue {
	flow := execPath{vars: map[*execBinding]execValue{}, active: slices.ContainsFunc(args, func(v execValue) bool {
		return v.kind == "raw" || v.kind == "wrapped"
	})}
	index := 0

	for _, field := range fn.decl.Type.Params.List {
		for _, id := range field.Names {
			if index < len(args) {
				flow.vars[id.Obj] = args[index]
			}

			index++
		}
	}

	paths := c.block(fn, fn.body.List, []execPath{flow}, depth)

	result := paths[0].result
	for _, p := range paths {
		if !p.done || p.unsupported || p.result != result {
			return execValue{}
		}
	}

	return result
}

func (c *execCheck) condition(fn *execFunction, expr ast.Expr, flow *execPath, depth int) string {
	expr = ast.Unparen(expr)
	if unary, ok := expr.(*ast.UnaryExpr); ok && unary.Op == token.NOT {
		switch c.condition(fn, unary.X, flow, depth) {
		case "true":
			return "false"
		case "false":
			return "true"
		default:
			return ""
		}
	}

	if binary, ok := expr.(*ast.BinaryExpr); ok && (binary.Op == token.EQL || binary.Op == token.NEQ) {
		left, right := binary.X, binary.Y
		if id, ok := left.(*ast.Ident); ok && id.Name == "nil" && id.Obj == nil {
			left, right = right, left
		}

		v := c.value(fn, left, flow, depth)
		if id, ok := right.(*ast.Ident); ok && id.Name == "nil" && id.Obj == nil && (v.kind == "raw" || v.kind == "wrapped") {
			return strconv.FormatBool(binary.Op == token.NEQ)
		}
	}

	if call, ok := expr.(*ast.CallExpr); ok && c.value(fn, call.Fun, flow, depth).kind == "errors.As" && len(call.Args) == 2 &&
		c.value(fn, call.Args[0], flow, depth).kind == "raw" {
		if execExitTarget(fn.file, call.Args[1]) {
			return "false"
		}
	}

	c.value(fn, expr, flow, depth)

	return ""
}

// Only simple sequential %w formats preserve classification here. Passing a
// wrapped error to an arbitrary call, or printing it with %v, is not enough.
// Escaped percent signs and indexed/dynamic formats are outside this contract.
func execWrappedFormat(call *ast.CallExpr, args []execValue) bool {
	if len(args) < 2 {
		return false
	}

	format, ok := call.Args[0].(*ast.BasicLit)
	if !ok {
		return false
	}

	text, _ := strconv.Unquote(format.Value)
	if strings.Count(text, "%") != len(args)-1 || strings.ContainsAny(text, "[*") || strings.Contains(text, "%%") {
		return false
	}

	for i, verb := range strings.Split(text, "%")[1:] {
		if strings.HasPrefix(verb, "w") && args[i+1].kind == "wrapped" {
			return true
		}
	}

	return false
}

func execExitTarget(file *ast.File, expr ast.Expr) bool {
	addr, ok := expr.(*ast.UnaryExpr)
	if !ok || addr.Op != token.AND {
		return false
	}

	id, ok := addr.X.(*ast.Ident)
	if !ok || id.Obj == nil {
		return false
	}

	spec, ok := id.Obj.Decl.(*ast.ValueSpec)
	if !ok {
		return false
	}

	star, ok := spec.Type.(*ast.StarExpr)

	return ok && execImported(file, star.X) == "os/exec.ExitError"
}

func execImported(file *ast.File, expr ast.Expr) string {
	expr = ast.Unparen(expr)

	var qualifier, member string

	switch expr := expr.(type) {
	case *ast.SelectorExpr:
		id, ok := expr.X.(*ast.Ident)
		if !ok || id.Obj != nil {
			return ""
		}

		qualifier, member = id.Name, expr.Sel.Name
	case *ast.Ident:
		if expr.Obj != nil {
			return ""
		}

		qualifier, member = ".", expr.Name
	default:
		return ""
	}

	for _, spec := range file.Imports {
		importPath, _ := strconv.Unquote(spec.Path.Value)

		name := path.Base(importPath)
		if spec.Name != nil {
			name = spec.Name.Name
		}

		if name == qualifier && name != "_" {
			return importPath + "." + member
		}
	}

	return ""
}

func (c *execCheck) helper(fn *execFunction, expr ast.Expr) execValue {
	var result execValue

	for _, candidate := range c.functions {
		decl := candidate.decl
		if decl == nil {
			continue
		}

		matches := false
		if id, ok := expr.(*ast.Ident); ok && decl.Recv == nil && id.Name == decl.Name.Name &&
			(id.Obj == nil || id.Obj.Kind == ast.Fun) {
			matches = true
		}

		if sel, ok := expr.(*ast.SelectorExpr); ok && decl.Recv != nil && fn.decl != nil && fn.decl.Recv != nil && sel.Sel.Name == decl.Name.Name {
			if id, ok := sel.X.(*ast.Ident); ok && len(fn.decl.Recv.List[0].Names) == 1 && id.Obj == fn.decl.Recv.List[0].Names[0].Obj &&
				execReceiver(fn.decl) != "" && execReceiver(decl) == execReceiver(fn.decl) {
				matches = true
			}
		}

		if !matches {
			continue
		}

		if result.helper == nil {
			result.helper = candidate

			continue
		}
		// All build variants are scanned. Never pick the first declaration's
		// body; retain only command shape so an ambiguous builder cannot hide Run.
		if result.kind != "ambiguous command" {
			result.kind = "ambiguous helper"
		}

		if execCommandBuilder(result.helper) || execCommandBuilder(candidate) {
			result.kind = "ambiguous command"
		}
	}

	return result
}

func execReceiver(decl *ast.FuncDecl) string {
	expr := decl.Recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	// Receiver type parameters do not change the package-local base identity.
	switch indexed := expr.(type) {
	case *ast.IndexExpr:
		expr = indexed.X
	case *ast.IndexListExpr:
		expr = indexed.X
	default:
	}

	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}

	return ""
}

func execCommandBuilder(fn *execFunction) bool {
	if fn.decl.Type.Results != nil && len(fn.decl.Type.Results.List) == 1 {
		if star, ok := fn.decl.Type.Results.List[0].Type.(*ast.StarExpr); ok {
			return execImported(fn.file, star.X) == "os/exec.Cmd"
		}
	}

	return false
}
