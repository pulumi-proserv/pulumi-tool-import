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

// internal/tools/importidscrape/steps.go
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type ImportStep struct {
	TFType     string
	File       string
	Line       int
	IDFuncExpr ast.Expr
	StaticID   string
	Pkg        *ast.Package
	Fset       *token.FileSet
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
		for _, pkg := range pkgs {
			for path, file := range pkg.Files {
				rel, _ := filepath.Rel(providerRoot, path)
				steps = append(steps, stepsInFile(fset, pkg, file, filepath.ToSlash(rel))...)
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
			step.TFType = tfTypeOf(kv.Value, names)
		case "ImportStateIdFunc":
			step.IDFuncExpr = kv.Value
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

// tfTypeOf reduces "aws_x.name" (literal or a local variable bound to one)
// to "aws_x". Element literals such as []resource.TestStep{{...}} name no
// type, so callers also accept a nil type.
func tfTypeOf(e ast.Expr, names map[string]string) string {
	var addr string
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind == token.STRING {
			addr, _ = strconv.Unquote(v.Value)
		}
	case *ast.Ident:
		addr = names[v.Name]
	}
	if i := strings.Index(addr, "."); i > 0 {
		return addr[:i]
	}
	return ""
}
