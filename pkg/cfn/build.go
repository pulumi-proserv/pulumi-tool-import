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
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type StackResource struct{ LogicalID, PhysicalID, CfnType string }

type StackReader interface {
	GetTemplate(ctx context.Context, stackName string) (string, error)
	ListStackResources(ctx context.Context, stackName string) ([]StackResource, error)
	GetExports(ctx context.Context) (map[string]string, error)
}

var skipTypes = map[string]bool{
	"AWS::CloudFormation::CustomResource":      true,
	"AWS::CDK::Metadata":                       true,
	"AWS::CloudFormation::WaitCondition":       true,
	"AWS::CloudFormation::WaitConditionHandle": true,
}

func shouldSkip(t string) bool { return skipTypes[t] || strings.HasPrefix(t, "Custom::") }

func BuildDigest(ctx context.Context, stackName, region string, sr StackReader, cc CloudControlReader, lk Lookups) (*StackDigest, error) {
	tmplStr, err := sr.GetTemplate(ctx, stackName)
	if err != nil {
		return nil, fmt.Errorf("get template: %w", err)
	}
	var tmpl struct {
		Resources map[string]struct {
			Type       string                 `json:"Type"`
			Properties map[string]interface{} `json:"Properties"`
		} `json:"Resources"`
		Parameters map[string]struct {
			NoEcho interface{} `json:"NoEcho"`
		} `json:"Parameters"`
	}
	if err := json.Unmarshal([]byte(tmplStr), &tmpl); err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}
	stackResources, err := sr.ListStackResources(ctx, stackName)
	if err != nil {
		return nil, fmt.Errorf("list stack resources: %w", err)
	}
	resources, resourceTypes := map[string]string{}, map[string]string{}
	for _, r := range stackResources {
		resources[r.LogicalID] = r.PhysicalID
		resourceTypes[r.LogicalID] = r.CfnType
	}
	exports, err := sr.GetExports(ctx)
	if err != nil {
		return nil, fmt.Errorf("get exports: %w", err)
	}

	digest := &StackDigest{StackName: stackName, Region: region}
	for _, r := range stackResources {
		res := CfnResource{LogicalID: r.LogicalID, CfnType: r.CfnType, PhysicalID: r.PhysicalID, Region: region}
		if shouldSkip(r.CfnType) {
			res.Skipped, res.SkipReason = true, "CFN-only/CDK resource"
			digest.Resources = append(digest.Resources, res)
			continue
		}
		res.PulumiType = PulumiType(r.CfnType)
		res.DerivedName, res.CdkHashedName, res.ServerAssigned = ClassifyName(r.LogicalID, r.PhysicalID, r.CfnType)

		attrs := map[string]interface{}{"Id": r.PhysicalID}
		if t, ok := tmpl.Resources[r.LogicalID]; ok && t.Properties != nil {
			resolved, err := ResolveProperties(ctx, t.Properties, resources, resourceTypes, exports, cc, region)
			if err != nil {
				return nil, fmt.Errorf("resolve %s: %w", r.LogicalID, err)
			}
			for k, v := range resolved {
				attrs[k] = v
			}
		}
		res.Attributes = attrs

		// Inline AWS::IAM::Policy: map to aws:iam/rolePolicy:RolePolicy and
		// pre-resolve its RoleName:PolicyName import ID (single-role case).
		if r.CfnType == "AWS::IAM::Policy" {
			if id, pt, ok := inlineRolePolicyImportID(attrs); ok {
				res.PulumiType = pt
				res.ImportID = id
			}
		}

		// API Gateway family: pre-resolve the aws-native composite import ID
		// (used when a node is authored with aws-native — resolve cfn --provider
		// native picks NativeImportID up).
		if id, ok := nativeAPIGatewayImportID(r.CfnType, attrs); ok {
			res.NativeImportID = id
		}

		if id, isLookup, err := LookupImportID(ctx, r.CfnType, attrs, lk); err != nil {
			return nil, fmt.Errorf("lookup %s: %w", r.LogicalID, err)
		} else if isLookup {
			res.ImportID = id // pre-resolved; resolve cfn uses it directly
		}
		digest.Resources = append(digest.Resources, res)
	}

	// NoEcho parameter values are masked by CloudFormation and can't be
	// extracted — surface the names so they can be set as secrets manually.
	for name, p := range tmpl.Parameters {
		if isTruthy(p.NoEcho) {
			digest.NoEchoParameters = append(digest.NoEchoParameters, name)
		}
	}
	sort.Strings(digest.NoEchoParameters)

	return digest, nil
}

// isTruthy reports whether a CFN NoEcho value (bool true or string "true") is set.
func isTruthy(v interface{}) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(t, "true")
	}
	return false
}
