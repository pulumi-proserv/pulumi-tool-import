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

	"github.com/pulumi-proserv/pulumi-tool-import/pkg"
	"github.com/stretchr/testify/require"
)

func TestExtractSecrets(t *testing.T) {
	t.Parallel()
	d := &StackDigest{Resources: []CfnResource{
		{LogicalID: "DbSecret", CfnType: "AWS::SecretsManager::Secret",
			Attributes: map[string]interface{}{"SecretString": "s3cr3t", "Name": "db"}},
		{LogicalID: "Db", CfnType: "AWS::RDS::DBInstance",
			Attributes: map[string]interface{}{"MasterUserPassword": "pw123", "Engine": "postgres"}},
		{LogicalID: "Api", CfnType: "AWS::Lambda::Function",
			Attributes: map[string]interface{}{"FunctionName": "api"}},
		{LogicalID: "Meta", CfnType: "AWS::CDK::Metadata", Skipped: true,
			Attributes: map[string]interface{}{"Password": "should-not-touch"}},
	}}

	entries := ExtractSecrets(d)

	require.Len(t, entries, 2, "two sensitive literals extracted")

	// Sensitive values redacted in place; non-sensitive left alone.
	require.Equal(t, "(sensitive)", d.Resources[0].Attributes["SecretString"])
	require.Equal(t, "db", d.Resources[0].Attributes["Name"])
	require.Equal(t, "(sensitive)", d.Resources[1].Attributes["MasterUserPassword"])
	require.Equal(t, "postgres", d.Resources[1].Attributes["Engine"])
	require.Equal(t, "api", d.Resources[2].Attributes["FunctionName"])
	// Skipped resources are ignored.
	require.Equal(t, "should-not-touch", d.Resources[3].Attributes["Password"])

	byKey := map[string]pkg.ConfigEntry{}
	for _, e := range entries {
		byKey[e.ConfigKey] = e
	}
	require.Equal(t, "s3cr3t", byKey["dbsecret_secretstring"].Value)
	require.True(t, byKey["dbsecret_secretstring"].Secret)
	require.Equal(t, "pw123", byKey["db_masteruserpassword"].Value)
	require.True(t, byKey["db_masteruserpassword"].Secret)
}

func TestExtractSecrets_NameHeuristic(t *testing.T) {
	t.Parallel()
	// A password-like property on a type not in the curated map is still caught.
	d := &StackDigest{Resources: []CfnResource{
		{LogicalID: "Thing", CfnType: "AWS::Some::Resource",
			Attributes: map[string]interface{}{"AdminPassword": "hunter2", "Port": "5432"}},
	}}
	entries := ExtractSecrets(d)
	require.Len(t, entries, 1)
	require.Equal(t, "hunter2", entries[0].Value)
	require.Equal(t, "(sensitive)", d.Resources[0].Attributes["AdminPassword"])
	require.Equal(t, "5432", d.Resources[0].Attributes["Port"])
}

func TestExtractSecrets_NoApiGatewayFalsePositives(t *testing.T) {
	t.Parallel()
	// API Gateway ApiKey* flags must NOT be treated as secrets.
	d := &StackDigest{Resources: []CfnResource{
		{LogicalID: "M", CfnType: "AWS::ApiGateway::Method",
			Attributes: map[string]interface{}{"ApiKeyRequired": false, "HttpMethod": "GET"}},
		{LogicalID: "R", CfnType: "AWS::ApiGateway::RestApi",
			Attributes: map[string]interface{}{"ApiKeySourceType": "HEADER"}},
	}}
	entries := ExtractSecrets(d)
	require.Empty(t, entries)
	require.Equal(t, "HEADER", d.Resources[1].Attributes["ApiKeySourceType"], "not redacted")
}

func TestExtractSecrets_SkipsMarkersEmptyAndNonStrings(t *testing.T) {
	t.Parallel()
	d := &StackDigest{Resources: []CfnResource{
		{LogicalID: "A", CfnType: "AWS::SecretsManager::Secret",
			Attributes: map[string]interface{}{"SecretString": "<unresolved-intrinsic:Fn::Sub>"}},
		{LogicalID: "B", CfnType: "AWS::RDS::DBInstance",
			Attributes: map[string]interface{}{"MasterUserPassword": ""}},
		{LogicalID: "C", CfnType: "AWS::RDS::DBInstance",
			Attributes: map[string]interface{}{"MasterUserPassword": map[string]interface{}{"Fn::Sub": "x"}}},
	}}
	entries := ExtractSecrets(d)
	require.Empty(t, entries)
	// Markers / empty / non-strings are not redacted — there is no plaintext to hide.
	require.Equal(t, "<unresolved-intrinsic:Fn::Sub>", d.Resources[0].Attributes["SecretString"])
}
