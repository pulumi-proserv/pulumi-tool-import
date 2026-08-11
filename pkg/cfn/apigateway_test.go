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

func TestNativeAPIGatewayImportID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		cfnType string
		attrs   map[string]interface{}
		want    string
	}{
		{"AWS::ApiGateway::RestApi", map[string]interface{}{"Id": "abc"}, "abc"},
		{"AWS::ApiGateway::Resource", map[string]interface{}{"RestApiId": "abc", "Id": "res"}, "abc|res"},
		{"AWS::ApiGateway::Method", map[string]interface{}{"RestApiId": "abc", "ResourceId": "res", "HttpMethod": "GET"}, "abc|res|GET"},
		// Deployment native order is REVERSED: DeploymentId|RestApiId.
		{"AWS::ApiGateway::Deployment", map[string]interface{}{"RestApiId": "abc", "Id": "dep"}, "dep|abc"},
		{"AWS::ApiGateway::Stage", map[string]interface{}{"RestApiId": "abc", "StageName": "prod"}, "abc|prod"},
		{"AWS::ApiGateway::Authorizer", map[string]interface{}{"RestApiId": "abc", "Id": "auth"}, "abc|auth"},
	}
	for _, tc := range cases {
		id, ok := nativeAPIGatewayImportID(tc.cfnType, tc.attrs)
		require.True(t, ok, tc.cfnType)
		require.Equal(t, tc.want, id, tc.cfnType)
	}

	// Non-API-Gateway type → not handled.
	_, ok := nativeAPIGatewayImportID("AWS::Lambda::Function", map[string]interface{}{"Id": "x"})
	require.False(t, ok)

	// Missing part → not handled.
	_, ok = nativeAPIGatewayImportID("AWS::ApiGateway::Stage", map[string]interface{}{"RestApiId": "abc"})
	require.False(t, ok)
}
