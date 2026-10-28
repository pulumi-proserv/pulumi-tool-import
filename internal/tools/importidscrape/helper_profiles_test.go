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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCrossRegionAttrsAndSeparator(t *testing.T) {
	const providerFlex = "github.com/hashicorp/terraform-provider-aws/internal/flex"
	hashes, err := helperHashes(fixtureRoot(t))
	require.NoError(t, err)
	separator, err := loadResourceIDSeparator(fixtureRoot(t))
	require.NoError(t, err)
	for _, tc := range []struct {
		name, helper, path, alias, setup, args, want string
		unverified                                   bool
	}{
		{"cross-region literal", "CrossRegionAttrsImportStateIdFunc", providerFlex, "flex", "", `resourceName, "/", "group", "name"`, "{group}/{name}", false},
		{"cross-region constant", "CrossRegionAttrsImportStateIdFunc", providerFlex, "flex", "", `resourceName, flex.ResourceIdSeparator, "group", "name"`, "{group},{name}", false},
		{"plain helper alias", "AttrsImportStateIdFunc", providerFlex, "intflex", "", `resourceName, intflex.ResourceIdSeparator, "group", "name"`, "{group},{name}", false},
		{"foreign package", "CrossRegionAttrsImportStateIdFunc", "example.com/internal/flex", "flex", "", `resourceName, flex.ResourceIdSeparator, "group", "name"`, "", false},
		{"shadowed package", "CrossRegionAttrsImportStateIdFunc", providerFlex, "flex", `flex := struct{ ResourceIdSeparator string }{":"}`, `resourceName, flex.ResourceIdSeparator, "group", "name"`, "", false},
		{"different resource", "CrossRegionAttrsImportStateIdFunc", providerFlex, "flex", "", `"aws_other.test", ",", "group", "name"`, "", false},
		{"dynamic separator", "CrossRegionAttrsImportStateIdFunc", providerFlex, "flex", `separator := ","`, `resourceName, separator, "group", "name"`, "", false},
		{"spread attrs", "CrossRegionAttrsImportStateIdFunc", providerFlex, "flex", "", `resourceName, ",", attrs...`, "", false},
		{"unverified helper", "CrossRegionAttrsImportStateIdFunc", providerFlex, "flex", "", `resourceName, ",", "group", "name"`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := fmt.Sprintf(`package example
import "github.com/hashicorp/terraform-provider-aws/internal/acctest"
import %s %q
func TestAccExample() {
 resourceName := "aws_example.test"
 %s
 resource.Test(resource.TestCase{Steps: []resource.TestStep{{ResourceName: resourceName, ImportState: true, ImportStateIdFunc: acctest.%s(%s)}}})
}`, tc.alias, tc.path, tc.setup, tc.helper, tc.args)
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "example_test.go", src, 0)
			require.NoError(t, err)
			pkg := &ast.Package{Name: "example", Files: map[string]*ast.File{"example_test.go": file}}
			steps := stepsInFile(fset, pkg, file, "example_test.go")
			require.Len(t, steps, 1)
			step := steps[0]
			step.Helpers, step.ResourceIDSeparator = hashes, &separator
			if tc.unverified {
				step.Helpers = map[string]string{"AttrImportStateIdFunc": hashes["AttrImportStateIdFunc"]}
			}
			c := classify(step, nil)
			assert.Equal(t, tc.want, c.Template)
			assert.Equal(t, tc.want == "", c.Manual)
		})
	}
}

func TestSeparatorMustBeLiteralConstant(t *testing.T) {
	for _, tc := range []struct{ declaration, want string }{
		{`const ResourceIdSeparator = "|"`, "|"},
		{`var ResourceIdSeparator = ","`, ""},
		{`const ResourceIdSeparator = other`, ""},
	} {
		root := t.TempDir()
		dir := filepath.Join(root, "internal", "flex")
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "flex.go"), []byte("package flex\n"+tc.declaration), 0o600))
		value, err := loadResourceIDSeparator(root)
		if tc.want == "" {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
			assert.Equal(t, tc.want, value)
		}
	}
}
