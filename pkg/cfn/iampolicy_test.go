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
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInlineRolePolicyImportID(t *testing.T) {
	t.Parallel()

	// Single-role inline policy → role:policyName, mapped to rolePolicy.
	id, pt, ok := inlineRolePolicyImportID(map[string]interface{}{
		"PolicyName": "lambdafunctionServiceRoleDefaultPolicy33908639",
		"Roles":      []interface{}{"mysvc-cs-lambdafunctionServiceRole-onNcPqJLMnE7"},
	})
	require.True(t, ok)
	require.Equal(t, "aws:iam/rolePolicy:RolePolicy", pt)
	require.Equal(t, "mysvc-cs-lambdafunctionServiceRole-onNcPqJLMnE7:lambdafunctionServiceRoleDefaultPolicy33908639", id)

	// Multi-role → not auto-resolved (one CFN policy = N Pulumi rolePolicies).
	_, _, ok = inlineRolePolicyImportID(map[string]interface{}{
		"PolicyName": "p", "Roles": []interface{}{"roleA", "roleB"},
	})
	require.False(t, ok)

	// Attached to users/groups (no Roles) → not auto-resolved.
	_, _, ok = inlineRolePolicyImportID(map[string]interface{}{
		"PolicyName": "p", "Users": []interface{}{"u"},
	})
	require.False(t, ok)

	// Unresolved role reference → not auto-resolved.
	_, _, ok = inlineRolePolicyImportID(map[string]interface{}{
		"PolicyName": "p", "Roles": []interface{}{"<unresolved-intrinsic:Fn::GetAtt>"},
	})
	require.False(t, ok)

	// Missing PolicyName → not auto-resolved.
	_, _, ok = inlineRolePolicyImportID(map[string]interface{}{
		"Roles": []interface{}{"roleA"},
	})
	require.False(t, ok)
}
