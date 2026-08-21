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

// Nested sensitive attributes are redacted to a placeholder that carries the
// Terraform path itself — "(sensitive:user[0].password)" — so the sidecar's
// key map, stack-config discovery, and injection-time resolution all correlate
// by that path with no Pulumi→Terraform name inversion anywhere (#28). The
// inversion would otherwise have to replay the bridge's per-level renames and
// MaxItems=1 flattening backwards, which is exactly the fragile machinery this
// avoids. Top-level redaction keeps the bare "(sensitive)" placeholder, so
// existing digests and their consumers are unchanged.

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

// renderAttrPath renders a cty attribute path in the same syntax the tagged
// placeholder carries: attribute steps join with ".", index steps render as
// [0] or ["key"].
func renderAttrPath(path cty.Path) string {
	var b strings.Builder
	for _, step := range path {
		switch s := step.(type) {
		case cty.GetAttrStep:
			if b.Len() > 0 {
				b.WriteByte('.')
			}
			b.WriteString(s.Name)
		case cty.IndexStep:
			switch {
			case s.Key.Type() == cty.String:
				fmt.Fprintf(&b, "[%q]", s.Key.AsString())
			case s.Key.Type() == cty.Number:
				f, _ := s.Key.AsBigFloat().Float64()
				fmt.Fprintf(&b, "[%d]", int(f))
			}
		}
	}
	return b.String()
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

// concreteSensitivePaths expands one marked path against the actual attribute
// value into the concrete path strings redaction would have tagged: an
// unresolvable index fans out to every element, exactly mirroring redactAtPath.
func concreteSensitivePaths(container interface{}, path cty.Path, walked cty.Path) []string {
	if len(path) == 0 {
		return []string{renderAttrPath(walked)}
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
		return concreteSensitivePaths(value, path[1:], append(walked, step))
	case cty.IndexStep:
		switch c := container.(type) {
		case []interface{}:
			if idx, ok := indexStepOrdinal(step, len(c)); ok {
				if idx < 0 || idx >= len(c) {
					return nil
				}
				return concreteSensitivePaths(c[idx], path[1:],
					append(walked, cty.IndexStep{Key: cty.NumberIntVal(int64(idx))}))
			}
			var out []string
			for i, elem := range c {
				out = append(out, concreteSensitivePaths(elem, path[1:],
					append(walked, cty.IndexStep{Key: cty.NumberIntVal(int64(i))}))...)
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
			return concreteSensitivePaths(value, path[1:],
				append(walked, cty.IndexStep{Key: cty.StringVal(key)}))
		}
	}
	return nil
}

// lookupAttrPath resolves a rendered path string ("user[0].password") against
// a decoded attribute map.
func lookupAttrPath(attrs map[string]interface{}, path string) (interface{}, bool) {
	var cur interface{} = attrs
	for _, seg := range splitAttrPath(path) {
		switch c := cur.(type) {
		case map[string]interface{}:
			v, ok := c[seg]
			if !ok {
				return nil, false
			}
			cur = v
		case []interface{}:
			var idx int
			if _, err := fmt.Sscanf(seg, "%d", &idx); err != nil || idx < 0 || idx >= len(c) {
				return nil, false
			}
			cur = c[idx]
		default:
			return nil, false
		}
	}
	return cur, true
}

// splitAttrPath splits "user[0].password" into ["user", "0", "password"] and
// `tags["a.b"]` into ["tags", "a.b"] — quoted keys keep their dots.
func splitAttrPath(path string) []string {
	var segs []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			segs = append(segs, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(path); i++ {
		switch path[i] {
		case '.':
			flush()
		case '[':
			flush()
			end := strings.IndexByte(path[i:], ']')
			if end < 0 {
				cur.WriteString(path[i:])
				i = len(path)
				break
			}
			seg := path[i+1 : i+end]
			segs = append(segs, strings.Trim(seg, "\""))
			i += end
		default:
			cur.WriteByte(path[i])
		}
	}
	flush()
	return segs
}

// isScalarSecretValue reports whether a sensitive value can round-trip through
// a string stack-config entry. Composite values cannot — stringifying a map
// and later injecting the string would write a wrong value, so they are
// skipped with a warning rather than corrupted.
func isScalarSecretValue(v interface{}) bool {
	switch v.(type) {
	case string, bool, float64, json.Number:
		return true
	default:
		return false
	}
}
