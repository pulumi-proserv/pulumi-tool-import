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

	"github.com/pulumi-proserv/pulumi-tool-import/pkg/importid"
	"github.com/stretchr/testify/require"
)

func TestCfnGetter(t *testing.T) {
	t.Parallel()
	get := CfnGetter(map[string]interface{}{
		"FunctionName": "ffs-dev-api", "Id": "AllowS3", "RestApiId": "abc",
	})
	require.Equal(t, "ffs-dev-api", get(importid.RoleFunction))
	require.Equal(t, "AllowS3", get(importid.RoleStatement)) // CFN "Id" -> statement role
	require.Equal(t, "abc", get(importid.RoleRestApi))
}

func TestCfnGetter_FunctionArnNormalized(t *testing.T) {
	t.Parallel()

	// CDK resolves AWS::Lambda::Permission.FunctionName via Fn::GetAtt Function.Arn,
	// so the attribute is the full ARN. The permission import ID (function/sid) and
	// the imported `function` state must use the BARE name, or a program emitting
	// `function: fn.name` diffs and replaces (function is ForceNew).
	get := CfnGetter(map[string]interface{}{
		"FunctionName": "arn:aws:lambda:us-east-1:133762716682:function:mysvc-console-service-cs-develop-auth-lambda",
	})
	require.Equal(t, "mysvc-console-service-cs-develop-auth-lambda", get(importid.RoleFunction))

	// A qualified ARN (…:function:NAME:VERSION) still yields the bare name.
	getQ := CfnGetter(map[string]interface{}{
		"FunctionName": "arn:aws:lambda:us-east-1:133762716682:function:my-fn:PROD",
	})
	require.Equal(t, "my-fn", getQ(importid.RoleFunction))

	// A bare name passes through unchanged.
	getBare := CfnGetter(map[string]interface{}{"FunctionName": "ffs-dev-api"})
	require.Equal(t, "ffs-dev-api", getBare(importid.RoleFunction))
}

func TestCfnGetter_RoleNameOverload(t *testing.T) {
	t.Parallel()

	// PolicyName present alongside Name: explicit override wins.
	getWithPolicyName := CfnGetter(map[string]interface{}{
		"PolicyName": "my-scaling-policy", "Name": "my-name",
	})
	require.Equal(t, "my-scaling-policy", getWithPolicyName(importid.RoleName))

	// PolicyName absent: falls back to the CFN "Name" property.
	getWithoutPolicyName := CfnGetter(map[string]interface{}{
		"Name": "my-name",
	})
	require.Equal(t, "my-name", getWithoutPolicyName(importid.RoleName))
}
