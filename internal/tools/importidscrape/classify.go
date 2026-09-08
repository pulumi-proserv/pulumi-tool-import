// Copyright 2016-2026, Pulumi Corporation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Classification struct {
	Template string
	Manual   bool
	Snippet  string
	Symbol   string
	// File is the provider-relative path Line refers to. A helper usually
	// lives in a sibling file of the TestStep that names it, so pairing the
	// step's file with the helper's line cites an unrelated line.
	File string
	Line int
}

// loadNameConsts reads the provider's "names" package and returns its
// untyped string constants by identifier, e.g. AttrName -> "name".
//
// The provider spells most attribute lookups rs.Primary.Attributes[names.AttrName]
// rather than with a literal. Those constants are generated string literals, so
// resolving one is substitution, not inference: it does not admit any expression
// shape templateOf would otherwise reject, it only lets the existing
// Attributes[<string literal>] shape see the literal it was already written as.
// Without this the classifier proves a template only for the shrinking minority
// of tests still using bare literals.
func loadNameConsts(providerRoot string) (map[string]string, error) {
	dir := filepath.Join(providerRoot, "names")
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return strings.HasSuffix(fi.Name(), ".go") && !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", dir, err)
	}
	out := map[string]string{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, d := range file.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok || gd.Tok != token.CONST {
					continue
				}
				for _, spec := range gd.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
						continue
					}
					lit, ok := vs.Values[0].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					if s, err := strconv.Unquote(lit.Value); err == nil && s != "" {
						out[vs.Names[0].Name] = s
					}
				}
			}
		}
	}
	return out, nil
}

// classify proves a template for the step's ImportStateIdFunc or marks it
// manual. A passthrough step (no func, no static id) yields the zero value.
// consts is the provider's "names" package, from loadNameConsts.
func classify(step ImportStep, consts map[string]string) Classification {
	if step.StaticID != "" {
		return Classification{Manual: true, Snippet: `ImportStateId: ` + strconv.Quote(step.StaticID), File: step.File, Line: step.Line}
	}
	if step.IDFuncExpr == nil {
		return Classification{}
	}
	if call, ok := step.IDFuncExpr.(*ast.CallExpr); ok {
		if tmpl, symbol, ok := acctestTemplate(call, step, consts); ok {
			return Classification{Template: tmpl, Symbol: symbol, File: step.File, Line: step.Line}
		}
	}
	fn, symbol := resolveIDFunc(step)
	if fn == nil {
		return Classification{Manual: true, Snippet: exprString(step.Fset, step.IDFuncExpr), Symbol: symbol, File: step.File, Line: step.Line}
	}
	pos := step.Fset.Position(fn.Pos())
	c := Classification{Symbol: symbol, File: relToProvider(step, pos.Filename), Line: pos.Line}
	body := closureBody(fn)
	ret := returnedIDExpr(body)
	if ret == nil {
		c.Manual, c.Snippet = true, exprString(step.Fset, body)
		return c
	}
	tmpl, ok := templateOf(ret, receiverNames(body, fn, step), consts)
	if !ok {
		c.Manual, c.Snippet = true, exprString(step.Fset, body)
		return c
	}
	c.Template = tmpl
	return c
}

// relToProvider turns an absolute path from the FileSet into a
// provider-relative, slash-separated one. The provider root is recovered from
// the step, whose absolute and relative paths are both known; a path outside
// that root (which cannot happen — the resolved func is in the step's own
// package) falls back to the step's file.
func relToProvider(step ImportStep, abs string) string {
	stepAbs := filepath.ToSlash(step.Fset.Position(step.Syntax.Pos()).Filename)
	root := strings.TrimSuffix(stepAbs, step.File)
	rel := filepath.ToSlash(abs)
	if root == "" || !strings.HasPrefix(rel, root) {
		return step.File
	}
	return strings.TrimPrefix(rel, root)
}

// acctestTemplate proves a template for a call to one of the provider's own
// import-ID helpers in internal/acctest. Their bodies live outside
// internal/service, so the classifier never sees them; their semantics are
// fixed at the scraped tag and mirrored in
// testdata/provider/internal/acctest/state_id.go.
//
// The helpers that append "@<region>" (CrossRegion*) yield the same template
// as their plain counterparts: the suffix is Terraform's region-override
// syntax for the test, not part of the resource's import-ID format.
func acctestTemplate(call *ast.CallExpr, step ImportStep, consts map[string]string) (string, string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", "", false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "acctest" || !importsProviderAcctest(step.Syntax, pkg.Name) {
		return "", "", false
	}
	// A spread (attrs...) hides the attribute names.
	if call.Ellipsis.IsValid() {
		return "", "", false
	}
	// Every helper's first argument names the resource whose state it reads.
	// If that is not this step's own resource, the template it composes is
	// some other resource's ID.
	if step.Address == "" || len(step.IDFuncArgAddrs) == 0 || step.IDFuncArgAddrs[0] != step.Address {
		return "", "", false
	}
	symbol := "acctest." + sel.Sel.Name
	args := call.Args
	switch sel.Sel.Name {
	case "AttrImportStateIdFunc", "CrossRegionAttrImportStateIdFunc":
		if len(args) != 2 {
			return "", "", false
		}
		k, ok := attrKeyOf(args[1], consts)
		if !ok {
			return "", "", false
		}
		return "{" + k + "}", symbol, true
	case "AttrsImportStateIdFunc":
		if len(args) < 3 {
			return "", "", false
		}
		sep, ok := stringLit(args[1])
		if !ok {
			return "", "", false
		}
		parts := make([]string, 0, len(args)-2)
		for _, a := range args[2:] {
			k, ok := attrKeyOf(a, consts)
			if !ok {
				return "", "", false
			}
			parts = append(parts, "{"+k+"}")
		}
		return strings.Join(parts, sep), symbol, true
	case "CrossRegionImportStateIdFunc":
		if len(args) != 1 {
			return "", "", false
		}
		return "{id}", symbol, true
	}
	return "", "", false
}

// importsProviderAcctest reports whether name is bound, in this file, to the
// provider's own internal/acctest package. Without the check a test that
// imports some other package under the same name would be read with the
// wrong semantics.
func importsProviderAcctest(file *ast.File, name string) bool {
	if file == nil {
		return false
	}
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		local := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			local = imp.Name.Name
		}
		if local == name {
			return strings.HasSuffix(path, "/internal/acctest")
		}
	}
	return false
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}

// resolveIDFunc finds the FuncDecl behind the ImportStateIdFunc value: an
// identifier, or a call whose callee returns a closure.
func resolveIDFunc(step ImportStep) (*ast.FuncDecl, string) {
	var name string
	switch v := step.IDFuncExpr.(type) {
	case *ast.Ident:
		name = v.Name
	case *ast.CallExpr:
		if id, ok := v.Fun.(*ast.Ident); ok {
			name = id.Name
		}
	}
	if name == "" {
		return nil, ""
	}
	for _, f := range step.Pkg.Files {
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == name && fd.Recv == nil {
				return fd, name
			}
		}
	}
	return nil, name
}

// closureBody returns the body of the closure a helper returns, or the
// function's own body when it is the ImportStateIdFunc itself.
func closureBody(fn *ast.FuncDecl) *ast.BlockStmt {
	for _, st := range fn.Body.List {
		if rs, ok := st.(*ast.ReturnStmt); ok && len(rs.Results) == 1 {
			if lit, ok := rs.Results[0].(*ast.FuncLit); ok {
				return lit.Body
			}
		}
	}
	return fn.Body
}

// returnedIDExpr is the first result of the last `return x, nil` in body,
// or nil when the body has a return whose second value is not nil or any
// control flow other than the standard "not found" guard.
func returnedIDExpr(body *ast.BlockStmt) ast.Expr {
	var last ast.Expr
	for _, st := range body.List {
		switch s := st.(type) {
		case *ast.ReturnStmt:
			if len(s.Results) != 2 || !isNil(s.Results[1]) {
				return nil
			}
			last = s.Results[0]
		case *ast.IfStmt:
			// Only the "rs not found" guard is allowed: `if !ok { return "", fmt.Errorf(...) }`.
			if !isNotFoundGuard(s) {
				return nil
			}
		case *ast.AssignStmt, *ast.DeclStmt:
			// rs := ...; rs, ok := ... — inspected by receiverNames.
		default:
			return nil
		}
	}
	return last
}

func isNil(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "nil"
}

func isNotFoundGuard(s *ast.IfStmt) bool {
	if s.Else != nil || len(s.Body.List) != 1 {
		return false
	}
	rs, ok := s.Body.List[0].(*ast.ReturnStmt)
	return ok && len(rs.Results) == 2 && !isNil(rs.Results[1])
}

// receiverNames collects the local name bound to *this step's own resource*
// via the standard test prologue `s.RootModule().Resources[...]`. A binding
// only qualifies when its index provably names step.Address: a string
// literal equal to it, or an identifier that is a parameter of fn (the
// helper enclosing the closure) whose value at the ImportStateIdFunc call
// site resolved to step.Address (see IDFuncArgAddrs). A binding for any
// other resource — including a second, unrelated one such as
// `other := s.RootModule().Resources["aws_vpc.test"]`, or a parent lookup
// `s.RootModule().Resources[parentName]` — is never a receiver: accepting it
// would let a manual, wrong-resource body prove a template.
func receiverNames(body *ast.BlockStmt, fn *ast.FuncDecl, step ImportStep) map[string]bool {
	names := map[string]bool{}
	for _, st := range body.List {
		as, ok := st.(*ast.AssignStmt)
		if !ok || len(as.Rhs) != 1 {
			continue
		}
		ix, ok := as.Rhs[0].(*ast.IndexExpr)
		if !ok || !isRootModuleResources(ix.X) {
			continue
		}
		if !indexIsStepAddress(ix.Index, fn, step) {
			continue
		}
		if id, ok := as.Lhs[0].(*ast.Ident); ok {
			names[id.Name] = true
		}
		break
	}
	return names
}

// indexIsStepAddress reports whether idx — the key of a
// s.RootModule().Resources[idx] lookup — provably names step.Address.
func indexIsStepAddress(idx ast.Expr, fn *ast.FuncDecl, step ImportStep) bool {
	if step.Address == "" {
		return false
	}
	if lit, ok := idx.(*ast.BasicLit); ok && lit.Kind == token.STRING {
		s, err := strconv.Unquote(lit.Value)
		return err == nil && s == step.Address
	}
	id, ok := idx.(*ast.Ident)
	if !ok || fn == nil || fn.Type.Params == nil {
		return false
	}
	pos := paramPosition(fn, id.Name)
	if pos < 0 || pos >= len(step.IDFuncArgAddrs) {
		return false
	}
	return step.IDFuncArgAddrs[pos] == step.Address
}

// paramPosition returns the 0-based position of name among fn's parameters,
// or -1 if fn has no parameter by that name.
func paramPosition(fn *ast.FuncDecl, name string) int {
	i := 0
	for _, field := range fn.Type.Params.List {
		for _, n := range field.Names {
			if n.Name == name {
				return i
			}
			i++
		}
	}
	return -1
}

func isRootModuleResources(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Resources" {
		return false
	}
	call, ok := sel.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	inner, ok := call.Fun.(*ast.SelectorExpr)
	return ok && inner.Sel.Name == "RootModule"
}

// attrKeyOf resolves the key of an Attributes[...] lookup: a string literal,
// or a names.AttrFoo constant from the provider's names package.
func attrKeyOf(e ast.Expr, consts map[string]string) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		return s, err == nil && s != ""
	case *ast.SelectorExpr:
		pkg, ok := v.X.(*ast.Ident)
		if !ok || pkg.Name != "names" {
			return "", false
		}
		s, ok := consts[v.Sel.Name]
		return s, ok && s != ""
	}
	return "", false
}

// templateOf converts a whitelisted expression into a template. ok=false
// for any form outside the whitelist.
func templateOf(e ast.Expr, receivers map[string]bool, consts map[string]string) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		return s, err == nil
	case *ast.ParenExpr:
		return templateOf(v.X, receivers, consts)
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", false
		}
		l, ok1 := templateOf(v.X, receivers, consts)
		r, ok2 := templateOf(v.Y, receivers, consts)
		return l + r, ok1 && ok2
	case *ast.SelectorExpr: // rs.Primary.ID
		if v.Sel.Name == "ID" && isPrimaryOf(v.X, receivers) {
			return "{id}", true
		}
		return "", false
	case *ast.IndexExpr: // rs.Primary.Attributes["k"]
		sel, ok := v.X.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Attributes" || !isPrimaryOf(sel.X, receivers) {
			return "", false
		}
		k, ok := attrKeyOf(v.Index, consts)
		if !ok {
			return "", false
		}
		return "{" + k + "}", true
	case *ast.CallExpr: // fmt.Sprintf("%s/%s", a, b)
		sel, ok := v.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Sprintf" || len(v.Args) < 1 {
			return "", false
		}
		pkgIdent, ok := sel.X.(*ast.Ident)
		if !ok || pkgIdent.Name != "fmt" {
			return "", false
		}
		format, ok := v.Args[0].(*ast.BasicLit)
		if !ok || format.Kind != token.STRING {
			return "", false
		}
		f, err := strconv.Unquote(format.Value)
		if err != nil {
			return "", false
		}
		parts := strings.Split(f, "%s")
		if len(parts)-1 != len(v.Args)-1 || strings.Contains(strings.Join(parts, ""), "%") {
			return "", false
		}
		var b strings.Builder
		b.WriteString(parts[0])
		for i, arg := range v.Args[1:] {
			t, ok := templateOf(arg, receivers, consts)
			if !ok {
				return "", false
			}
			b.WriteString(t)
			b.WriteString(parts[i+1])
		}
		return b.String(), true
	}
	return "", false
}

func isPrimaryOf(e ast.Expr, receivers map[string]bool) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Primary" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && receivers[id.Name]
}

func exprString(fset *token.FileSet, n ast.Node) string {
	var buf bytes.Buffer
	_ = printer.Fprint(&buf, fset, n)
	return buf.String()
}
