// Copyright 2016-2025, Pulumi Corporation.
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

package pkg

import (
	"fmt"
	"sort"

	shim "github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfshim"
)

// schemaSensitiveLeaks returns the attribute paths the provider schema marks
// Sensitive but that still hold a real value after redaction, sorted.
//
// Redaction is driven by the state's own AttrSensitivePaths, and so is the
// stack-config discovery that recovers the values later. A single source means
// a single point of failure: when those marks are missing the redaction still
// runs, still reports success, and writes the secret to disk in the clear.
// That is not hypothetical — it is what the "tofu show -json" path did, where
// nothing populated the marks at all.
//
// The provider schema is an independent second opinion, available wherever a
// bridged schema is loaded. A nil schemaMap returns nothing, which means
// "could not check", not "clean" — callers report that separately.
//
// Paths are returned, never values: a report about a leaked secret must not
// repeat it.
func schemaSensitiveLeaks(attrs map[string]interface{}, schemaMap shim.SchemaMap) []string {
	if schemaMap == nil || attrs == nil {
		return nil
	}
	var leaks []string
	collectSensitiveLeaks(attrs, schemaMap, "", &leaks)
	sort.Strings(leaks)
	return leaks
}

func collectSensitiveLeaks(attrs map[string]interface{}, schemaMap shim.SchemaMap, prefix string, leaks *[]string) {
	schemaMap.Range(func(name string, sch shim.Schema) bool {
		value, present := attrs[name]
		if !present || value == nil {
			return true
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}

		if sch.Sensitive() {
			if s, ok := value.(string); !ok || s != redactedPlaceholder {
				*leaks = append(*leaks, path)
			}
			// A sensitive block is redacted as a whole, so there is nothing
			// below it left to check.
			return true
		}

		nested, ok := sch.Elem().(shim.Resource)
		if !ok || nested == nil {
			return true
		}
		switch v := value.(type) {
		case []interface{}:
			for i, elem := range v {
				if m, ok := elem.(map[string]interface{}); ok {
					collectSensitiveLeaks(m, nested.Schema(), fmt.Sprintf("%s[%d]", path, i), leaks)
				}
			}
		case map[string]interface{}:
			// MaxItems=1 blocks arrive as a bare map rather than a list of one.
			collectSensitiveLeaks(v, nested.Schema(), path, leaks)
		}
		return true
	})
}
