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
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type ImportStep struct {
	TFType     string
	Address    string // the step's full ResourceName, e.g. "aws_cloudwatch_event_target.test"
	File       string
	Line       int
	IDFuncExpr ast.Expr
	// IDFuncArgAddrs holds, for each positional argument at the
	// ImportStateIdFunc call site (when it is a call to a closure-returning
	// helper), the address that argument resolves to, or "" when it does not
	// resolve to a known address. Empty when IDFuncExpr is not a call.
	IDFuncArgAddrs []string
	StaticID       string
	Pkg            *ast.Package
	Fset           *token.FileSet
}

// collectImportSteps parses every *_test.go under internal/service and
// returns each resource.TestStep with ImportState: true.
func collectImportSteps(providerRoot string) ([]ImportStep, error) {
	serviceRoot := filepath.Join(providerRoot, "internal", "service")
	var dirs []string
	err := filepath.WalkDir(serviceRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking %s: %w", serviceRoot, err)
	}

	var steps []ImportStep
	for _, dir := range dirs {
		fset := token.NewFileSet()
		pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
			return strings.HasSuffix(fi.Name(), "_test.go")
		}, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", dir, err)
		}
		// Both maps must be walked in sorted order. A type often has several
		// equally-classified import steps in different files (portal_test.go
		// and portal_tags_gen_test.go, say); buildFormats keeps the first, so
		// map order would otherwise pick a different evidence file:line on
		// every run and make the generated table — and import-id-formats-check
		// — nondeterministic.
		for _, pkgName := range sortedKeys(pkgs) {
			pkg := pkgs[pkgName]
			for _, path := range sortedKeys(pkg.Files) {
				rel, _ := filepath.Rel(providerRoot, path)
				steps = append(steps, stepsInFile(fset, pkg, pkg.Files[path], filepath.ToSlash(rel))...)
			}
		}
	}
	return steps, nil
}

func stepsInFile(fset *token.FileSet, pkg *ast.Package, file *ast.File, rel string) []ImportStep {
	var steps []ImportStep
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		// resourceName := "aws_x.name" bindings in this function.
		names := map[string]string{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
				return true
			}
			id, ok := as.Lhs[0].(*ast.Ident)
			lit, ok2 := as.Rhs[0].(*ast.BasicLit)
			if ok && ok2 && lit.Kind == token.STRING {
				names[id.Name], _ = strconv.Unquote(lit.Value)
			}
			return true
		})
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok || !isTestStep(cl.Type) {
				return true
			}
			step, ok := stepFromLiteral(cl, names)
			if ok {
				step.File, step.Line, step.Pkg, step.Fset = rel, fset.Position(cl.Pos()).Line, pkg, fset
				steps = append(steps, step)
			}
			return true
		})
		return true
	})
	return steps
}

// isTestStep matches resource.TestStep, or nil (an element of a
// []resource.TestStep literal, whose elements omit the type).
func isTestStep(t ast.Expr) bool {
	if t == nil {
		return true
	}
	sel, ok := t.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "TestStep"
}

func stepFromLiteral(cl *ast.CompositeLit, names map[string]string) (ImportStep, bool) {
	var step ImportStep
	importState := false
	for _, el := range cl.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, _ := kv.Key.(*ast.Ident)
		if key == nil {
			continue
		}
		switch key.Name {
		case "ImportState":
			importState = isTrue(kv.Value)
		case "ResourceName":
			step.Address = addrOf(kv.Value, names)
			step.TFType = tfTypeOf(step.Address)
		case "ImportStateIdFunc":
			step.IDFuncExpr = kv.Value
			if call, ok := kv.Value.(*ast.CallExpr); ok {
				for _, arg := range call.Args {
					step.IDFuncArgAddrs = append(step.IDFuncArgAddrs, addrOf(arg, names))
				}
			}
		case "ImportStateId":
			if lit, ok := kv.Value.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				step.StaticID, _ = strconv.Unquote(lit.Value)
			}
		}
	}
	if !importState || step.TFType == "" {
		return ImportStep{}, false
	}
	return step, true
}

func isTrue(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "true"
}

// addrOf resolves e — a string literal, or a local variable bound to one via
// `names` — to its address string ("aws_x.name"), or "" when it does not
// resolve.
func addrOf(e ast.Expr, names map[string]string) string {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind == token.STRING {
			s, _ := strconv.Unquote(v.Value)
			return s
		}
	case *ast.Ident:
		return names[v.Name]
	}
	return ""
}

// tfTypeOf reduces an address ("aws_x.name") to its resource type ("aws_x").
// Element literals such as []resource.TestStep{{...}} name no type, so
// callers also tolerate an empty address.
func tfTypeOf(addr string) string {
	if i := strings.Index(addr, "."); i > 0 {
		return addr[:i]
	}
	return ""
}
