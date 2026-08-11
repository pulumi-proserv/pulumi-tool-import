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

import "strings"

// inlineRolePolicyImportID resolves the aws:iam/rolePolicy:RolePolicy import ID
// for a CFN AWS::IAM::Policy (an inline policy) — RoleName:PolicyName — for the
// single-role case (what CDK's default policies produce). The role is read from
// the resolved Roles list (a Ref to the role, already resolved to its name).
//
// Returns ok=false (leave unmapped, handle manually) when the policy attaches
// to users/groups or to multiple roles (one CFN policy → N Pulumi rolePolicies),
// or when the role reference did not resolve.
func inlineRolePolicyImportID(attrs map[string]interface{}) (importID, pulumiType string, ok bool) {
	policyName := str(attrs, "PolicyName")
	roles, _ := attrs["Roles"].([]interface{})
	if policyName == "" || len(roles) != 1 {
		return "", "", false
	}
	role, _ := roles[0].(string)
	if role == "" || strings.HasPrefix(role, "<unresolved-intrinsic:") {
		return "", "", false
	}
	return role + ":" + policyName, "aws:iam/rolePolicy:RolePolicy", true
}
