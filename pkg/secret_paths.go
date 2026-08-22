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
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zclconf/go-cty/cty"
)

// Nested sensitive attributes redact to a placeholder carrying the Terraform
// path itself — "(sensitive:user[0].password)" — and the sidecar's key map,
// stack-config discovery, and injection-time resolution all correlate by that
// path. Correlating by path avoids inverting the bridge's per-level renames
// and MaxItems=1 flattening. Top-level redaction keeps the bare "(sensitive)",
// so existing digests are unchanged.

const taggedPlaceholderPrefix = "(sensitive:"

// taggedPlaceholder renders the path-carrying placeholder for a nested
// sensitive attribute.
func taggedPlaceholder(path string) string {
	return taggedPlaceholderPrefix + path + ")"
}

// placeholderPath extracts the Terraform path from a tagged placeholder.
// The bare legacy "(sensitive)" carries no path and returns false.
func placeholderPath(s string) (string, bool) {
	if !strings.HasPrefix(s, taggedPlaceholderPrefix) || !strings.HasSuffix(s, ")") {
		return "", false
	}
	return s[len(taggedPlaceholderPrefix) : len(s)-1], true
}

// isRedactedPlaceholder reports whether s is either redaction placeholder
// form, bare or tagged.
func isRedactedPlaceholder(s string) bool {
	if s == redactedPlaceholder {
		return true
	}
	_, tagged := placeholderPath(s)
	return tagged
}

// renderAttrPath renders a cty attribute path in the tag's syntax: attribute
// steps join with ".", index steps render as [0] or ["key"]. The rendered
// string is an opaque correlation key, matched by equality and never
// re-parsed, so escaping only has to be deterministic. ok is false for an
// index key type this cannot render; the caller must skip the whole path — a
// shortened one would correlate wrongly.
func renderAttrPath(path cty.Path) (string, bool) {
	var b strings.Builder
	for _, step := range path {
		switch s := step.(type) {
		case cty.GetAttrStep:
			if b.Len() > 0 {
				b.WriteByte('.')
			}
			b.WriteString(s.Name)
		case cty.IndexStep:
			switch s.Key.Type() {
			case cty.String:
				fmt.Fprintf(&b, "[%q]", s.Key.AsString())
			case cty.Number:
				f, _ := s.Key.AsBigFloat().Float64()
				fmt.Fprintf(&b, "[%d]", int(f))
			default:
				return "", false
			}
		default:
			return "", false
		}
	}
	return b.String(), true
}

// flattenAddressPath derives the stack config key for an attribute path. A
// single-segment path is the top-level case and produces exactly what
// flattenAddress always has, so existing digests stay compatible; a nested
// path embeds every segment, indexes included, so user[0].password and
// user[1].password get distinct keys.
func flattenAddressPath(address, path string) string {
	sanitized := strings.NewReplacer(
		"[", "_",
		"]", "",
		".", "_",
		"\"", "",
		" ", "_",
	).Replace(path)
	// Collapse any doubled underscores the replacement produced (e.g. "]." -> "_").
	for strings.Contains(sanitized, "__") {
		sanitized = strings.ReplaceAll(sanitized, "__", "_")
	}
	return flattenAddress(address, strings.Trim(sanitized, "_"))
}

// sensitiveLeaf is one concrete leaf a marked path resolves to: the rendered
// path string (the correlation key) and the value found there.
type sensitiveLeaf struct {
	path  string
	value interface{}
}

// concreteSensitiveLeaves expands one marked path against the actual value
// into the (path, value) leaves redaction would tag, fanning out over an
// unresolvable index exactly as redactAtPath does. walked is copied at every
// branch (full slice expression) so sibling recursions never share a backing
// array.
func concreteSensitiveLeaves(container interface{}, path cty.Path, walked cty.Path) []sensitiveLeaf {
	if len(path) == 0 {
		rendered, ok := renderAttrPath(walked)
		if !ok {
			return nil
		}
		return []sensitiveLeaf{{path: rendered, value: container}}
	}
	next := func(step cty.PathStep) cty.Path {
		return append(walked[:len(walked):len(walked)], step)
	}
	switch step := path[0].(type) {
	case cty.GetAttrStep:
		m, ok := container.(map[string]interface{})
		if !ok {
			return nil
		}
		value, exists := m[step.Name]
		if !exists {
			return nil
		}
		return concreteSensitiveLeaves(value, path[1:], next(step))
	case cty.IndexStep:
		switch c := container.(type) {
		case []interface{}:
			if idx, ok := indexStepOrdinal(step, len(c)); ok {
				if idx < 0 || idx >= len(c) {
					return nil
				}
				return concreteSensitiveLeaves(c[idx], path[1:],
					next(cty.IndexStep{Key: cty.NumberIntVal(int64(idx))}))
			}
			var out []sensitiveLeaf
			for i, elem := range c {
				out = append(out, concreteSensitiveLeaves(elem, path[1:],
					next(cty.IndexStep{Key: cty.NumberIntVal(int64(i))}))...)
			}
			return out
		case map[string]interface{}:
			key, ok := indexStepKey(step)
			if !ok {
				return nil
			}
			value, exists := c[key]
			if !exists {
				return nil
			}
			return concreteSensitiveLeaves(value, path[1:],
				next(cty.IndexStep{Key: cty.StringVal(key)}))
		}
	}
	return nil
}

// isScalarSecretValue reports whether a sensitive value can pass through a
// string stack-config entry. Composites are skipped with a warning rather
// than stringified into a wrong value.
func isScalarSecretValue(v interface{}) bool {
	switch v.(type) {
	case string, bool, float64, json.Number:
		return true
	default:
		return false
	}
}
