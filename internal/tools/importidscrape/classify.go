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

// internal/tools/importidscrape/classify.go
package main

import (
	"bytes"
	"go/ast"
	"go/printer"
	"go/token"
	"strconv"
	"strings"
)

type Classification struct {
	Template string
	Manual   bool
	Snippet  string
	Symbol   string
	Line     int
}

// classify proves a template for the step's ImportStateIdFunc or marks it
// manual. A passthrough step (no func, no static id) yields the zero value.
func classify(step ImportStep) Classification {
	if step.StaticID != "" {
		return Classification{Manual: true, Snippet: `ImportStateId: ` + strconv.Quote(step.StaticID), Line: step.Line}
	}
	if step.IDFuncExpr == nil {
		return Classification{}
	}
	fn, symbol := resolveIDFunc(step)
	if fn == nil {
		return Classification{Manual: true, Snippet: exprString(step.Fset, step.IDFuncExpr), Symbol: symbol, Line: step.Line}
	}
	c := Classification{Symbol: symbol, Line: step.Fset.Position(fn.Pos()).Line}
	body := closureBody(fn)
	ret := returnedIDExpr(body)
	if ret == nil {
		c.Manual, c.Snippet = true, exprString(step.Fset, body)
		return c
	}
	tmpl, ok := templateOf(ret, receiverNames(body, fn))
	if !ok {
		c.Manual, c.Snippet = true, exprString(step.Fset, body)
		return c
	}
	c.Template = tmpl
	return c
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

// receiverNames collects the local name bound to the *primary* resource's
// s.RootModule().Resources[...] lookup (the standard test prologue). Only
// the first such binding in the body qualifies: a manual body that reads a
// second, unrelated resource (e.g. `other := s.RootModule().Resources["aws_vpc.test"]`)
// must not be accepted as a whitelisted receiver, or a cross-resource read
// would be silently classified as a template.
func receiverNames(body *ast.BlockStmt, fn *ast.FuncDecl) map[string]bool {
	names := map[string]bool{}
	for _, st := range body.List {
		as, ok := st.(*ast.AssignStmt)
		if !ok || len(as.Rhs) != 1 {
			continue
		}
		if ix, ok := as.Rhs[0].(*ast.IndexExpr); ok && isRootModuleResources(ix.X) {
			if id, ok := as.Lhs[0].(*ast.Ident); ok {
				names[id.Name] = true
			}
			break
		}
	}
	return names
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

// templateOf converts a whitelisted expression into a template. ok=false
// for any form outside the whitelist.
func templateOf(e ast.Expr, receivers map[string]bool) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		return s, err == nil
	case *ast.ParenExpr:
		return templateOf(v.X, receivers)
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", false
		}
		l, ok1 := templateOf(v.X, receivers)
		r, ok2 := templateOf(v.Y, receivers)
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
		lit, ok := v.Index.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return "", false
		}
		k, err := strconv.Unquote(lit.Value)
		if err != nil || k == "" {
			return "", false
		}
		return "{" + k + "}", true
	case *ast.CallExpr: // fmt.Sprintf("%s/%s", a, b)
		sel, ok := v.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Sprintf" || len(v.Args) < 1 {
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
			t, ok := templateOf(arg, receivers)
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
