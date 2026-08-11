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

package cfn

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// CloudControlReader fetches a resource's current attributes via the AWS Cloud
// Control API — used to resolve Fn::GetAtt references not present in the template.
type CloudControlReader interface {
	GetResource(ctx context.Context, typeName, id string) (map[string]interface{}, error)
}

// ResolveProperties evaluates CloudFormation intrinsics in a resource's
// properties into concrete values.
//   - resources:     logical ID -> physical ID (from ListStackResources)
//   - resourceTypes: logical ID -> CFN type (for Fn::GetAtt Cloud Control reads)
//   - exports:       stack export name -> value (for Fn::ImportValue)
func ResolveProperties(
	ctx context.Context,
	props map[string]interface{},
	resources, resourceTypes, exports map[string]string,
	cc CloudControlReader,
	region string,
) (map[string]interface{}, error) {
	out := make(map[string]interface{}, len(props))
	for k, v := range props {
		rv, err := resolveValue(ctx, v, resources, resourceTypes, exports, cc, region, true)
		if err != nil {
			return nil, fmt.Errorf("resolving %q: %w", k, err)
		}
		out[k] = rv
	}
	return out, nil
}

// resolveValue evaluates a single property value. topLevel is true only for a
// resource property's direct value; nested values (inside objects/arrays/joins)
// are resolved with topLevel=false.
//
// Ref / Fn::ImportValue / Fn::Join are cheap (map lookups) and resolved at any
// depth. Fn::GetAtt requires a live Cloud Control call, so it is resolved only
// at the top level — where a property may itself be a resource's import
// identifier. A GetAtt nested inside a policy document or environment map (where
// resolving it would be one AWS call per occurrence, and a reference is more
// useful to the migration than a baked value anyway) is surfaced as a marker
// instead of resolved.
func resolveValue(ctx context.Context, v interface{}, resources, resourceTypes, exports map[string]string, cc CloudControlReader, region string, topLevel bool) (interface{}, error) {
	switch val := v.(type) {
	case map[string]interface{}:
		// A CloudFormation intrinsic is a single-key map (Ref / Fn::*).
		if len(val) == 1 {
			if ref, ok := val["Ref"].(string); ok {
				if phys, ok := resources[ref]; ok {
					return phys, nil
				}
				return ref, nil // pseudo-param or parameter — left as-is
			}
			if imp, ok := val["Fn::ImportValue"].(string); ok {
				if v, ok := exports[imp]; ok {
					return v, nil
				}
				return "", fmt.Errorf("unresolved Fn::ImportValue %q", imp)
			}
			if ga, ok := val["Fn::GetAtt"].([]interface{}); ok && len(ga) == 2 {
				if !topLevel {
					// Nested GetAtt: don't spend a Cloud Control call per
					// occurrence — surface it as a marker.
					return "<unresolved-intrinsic:Fn::GetAtt>", nil
				}
				logical, _ := ga[0].(string)
				attr, _ := ga[1].(string)
				phys, ok := resources[logical]
				if !ok {
					return "", fmt.Errorf("Fn::GetAtt unknown resource %q", logical)
				}
				attrs, err := cc.GetResource(ctx, resourceTypes[logical], phys)
				if err != nil {
					return "", err
				}
				if av, ok := attrs[attr]; ok {
					return av, nil
				}
				return "", fmt.Errorf("Fn::GetAtt %q.%q not found", logical, attr)
			}
			if joinArgs, ok := val["Fn::Join"].([]interface{}); ok && len(joinArgs) == 2 {
				delim, _ := joinArgs[0].(string)
				list, _ := joinArgs[1].([]interface{})
				parts := make([]string, 0, len(list))
				for _, item := range list {
					rv, err := resolveValue(ctx, item, resources, resourceTypes, exports, cc, region, false)
					if err != nil {
						return "", err
					}
					parts = append(parts, fmt.Sprintf("%v", rv))
				}
				return strings.Join(parts, delim), nil
			}
			if sub, ok := val["Fn::Sub"]; ok {
				return resolveSub(ctx, sub, resources, resourceTypes, exports, cc, region)
			}
			if sel, ok := val["Fn::Select"].([]interface{}); ok && len(sel) == 2 {
				if idx, ok := toIndex(sel[0]); ok {
					lv, err := resolveValue(ctx, sel[1], resources, resourceTypes, exports, cc, region, false)
					if err != nil {
						return "", err
					}
					if list, ok := lv.([]interface{}); ok && idx >= 0 && idx < len(list) {
						return list[idx], nil
					}
				}
				return "<unresolved-intrinsic:Fn::Select>", nil
			}
			// A single-key map whose key is an intrinsic we don't resolve
			// (Fn::FindInMap, Fn::If, ...) — surface it rather than pass a raw
			// map through, so it never silently lands in a composed import ID.
			for k := range val {
				if strings.HasPrefix(k, "Fn::") || k == "Ref" {
					return fmt.Sprintf("<unresolved-intrinsic:%s>", k), nil
				}
			}
		}
		// A plain nested object: recurse into every value so cheap intrinsics at
		// any depth (e.g. inside IAM policy documents or environment maps) resolve.
		out := make(map[string]interface{}, len(val))
		for k, vv := range val {
			rv, err := resolveValue(ctx, vv, resources, resourceTypes, exports, cc, region, false)
			if err != nil {
				return nil, err
			}
			out[k] = rv
		}
		return out, nil
	case []interface{}:
		// Recurse into array elements (e.g. policy statement lists).
		out := make([]interface{}, len(val))
		for i, vv := range val {
			rv, err := resolveValue(ctx, vv, resources, resourceTypes, exports, cc, region, false)
			if err != nil {
				return nil, err
			}
			out[i] = rv
		}
		return out, nil
	default:
		return v, nil // literal
	}
}

// toIndex parses an Fn::Select index (a JSON number arrives as float64; CFN also
// allows a numeric string).
func toIndex(v interface{}) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case float64:
		return int(n), true
	case string:
		i, err := strconv.Atoi(n)
		return i, err == nil
	}
	return 0, false
}

var subVarRe = regexp.MustCompile(`\$\{([^}]+)\}`)

// resolveSub evaluates Fn::Sub — a template string, or [template, {vars}].
// Substitutes ${Var} from the vars map, the ${AWS::Region|Partition|URLSuffix}
// pseudo-parameters, and ${LogicalId} references. ${LogicalId.Attr} (a GetAtt)
// becomes a marker, and any other unknown parameter (e.g. ${AWS::AccountId}) is
// left literal — so nothing is silently wrong.
func resolveSub(ctx context.Context, raw interface{}, resources, resourceTypes, exports map[string]string, cc CloudControlReader, region string) (interface{}, error) {
	var tmpl string
	vars := map[string]interface{}{}
	switch s := raw.(type) {
	case string:
		tmpl = s
	case []interface{}:
		if len(s) >= 1 {
			tmpl, _ = s[0].(string)
		}
		if len(s) >= 2 {
			if m, ok := s[1].(map[string]interface{}); ok {
				vars = m
			}
		}
	default:
		return "<unresolved-intrinsic:Fn::Sub>", nil
	}

	var subErr error
	result := subVarRe.ReplaceAllStringFunc(tmpl, func(match string) string {
		name := match[2 : len(match)-1] // strip "${" and "}"
		if strings.HasPrefix(name, "!") {
			return "${" + name[1:] + "}" // ${!Literal} escape
		}
		if v, ok := vars[name]; ok {
			rv, err := resolveValue(ctx, v, resources, resourceTypes, exports, cc, region, false)
			if err != nil {
				subErr = err
				return match
			}
			return fmt.Sprintf("%v", rv)
		}
		switch name {
		case "AWS::Region":
			return region
		case "AWS::Partition":
			return "aws"
		case "AWS::URLSuffix":
			return "amazonaws.com"
		}
		if strings.Contains(name, ".") {
			return "<unresolved-intrinsic:Fn::GetAtt>" // ${LogicalId.Attr}
		}
		if phys, ok := resources[name]; ok {
			return phys
		}
		return match // unknown parameter (e.g. ${AWS::AccountId}) — leave literal
	})
	if subErr != nil {
		return "", subErr
	}
	return result, nil
}
