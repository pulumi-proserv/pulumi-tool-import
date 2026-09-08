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
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var placeholderRe = regexp.MustCompile(`\{([^{}]+)\}`)

// sensitiveAttrs finds the resource's schema by its @SDKResource /
// @FrameworkResource annotation and reports which template placeholders it
// marks Sensitive. Both SDKv2 (`"k": {... Sensitive: true}`) and Framework
// (`"k": schema.StringAttribute{... Sensitive: true}`) shapes are keyed
// string literals whose value literal contains a `Sensitive: true` field.
func sensitiveAttrs(providerRoot, tfType, template string) []string {
	file := findSchemaFile(providerRoot, tfType)
	if file == nil {
		return nil
	}
	sensitive := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		lit, ok := kv.Key.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		key, _ := strconv.Unquote(lit.Value)
		if cl, ok := kv.Value.(*ast.CompositeLit); ok && hasSensitiveTrue(cl) {
			sensitive[key] = true
		}
		return true
	})
	var out []string
	for _, m := range placeholderRe.FindAllStringSubmatch(template, -1) {
		if sensitive[m[1]] {
			out = append(out, m[1])
		}
	}
	return out
}

func hasSensitiveTrue(cl *ast.CompositeLit) bool {
	for _, el := range cl.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "Sensitive" && isTrue(kv.Value) {
			return true
		}
	}
	return false
}

var annotationRe = regexp.MustCompile(`@(?:SDK|Framework)Resource\("([a-z0-9_]+)"`)

// findSchemaFile scans non-test files under internal/service for the
// annotation naming tfType and parses that file.
func findSchemaFile(providerRoot, tfType string) *ast.File {
	var found *ast.File
	_ = filepath.WalkDir(filepath.Join(providerRoot, "internal", "service"), func(p string, d os.DirEntry, err error) error {
		if err != nil || found != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for _, m := range annotationRe.FindAllSubmatch(src, -1) {
			if string(m[1]) == tfType {
				f, err := parser.ParseFile(token.NewFileSet(), p, src, parser.ParseComments)
				if err == nil {
					found = f
				}
				return nil
			}
		}
		return nil
	})
	return found
}
