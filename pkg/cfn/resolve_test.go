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

func TestFillFromDigest(t *testing.T) {
	t.Parallel()
	digest := &StackDigest{Resources: []CfnResource{
		{LogicalID: "ApiPermission", CfnType: "AWS::Lambda::Permission",
			PulumiType: "aws:lambda/permission:Permission",
			Attributes: map[string]interface{}{"FunctionName": "ffs-dev-api", "Id": "AllowApiGw"}},
		{LogicalID: "TaskPolicy", CfnType: "AWS::IAM::Policy",
			PulumiType: "aws:iam/policy:Policy", ImportID: "arn:aws:iam::1:policy/p"},
		{LogicalID: "Dep", CfnType: "AWS::ApiGateway::Deployment",
			Attributes: map[string]interface{}{"RestApiId": "abc", "Id": "dep"}},
	}}
	importFile := &pkg.ImportFile{Resources: []pkg.ImportEntry{
		{Type: "aws:lambda/permission:Permission", Name: "ffs-ApiPermission"},
		{Type: "aws:iam/policy:Policy", Name: "ffs-TaskPolicy"},
		{Type: "aws:apigateway/deployment:Deployment", Name: "ffs-Dep"},
	}}
	res := FillFromDigest(digest, importFile, nil, "native")
	require.Equal(t, 3, res.Filled)
	require.Equal(t, "ffs-dev-api/AllowApiGw", importFile.Resources[0].ID)  // composed
	require.Equal(t, "arn:aws:iam::1:policy/p", importFile.Resources[1].ID) // pre-resolved lookup
	require.Equal(t, "dep|abc", importFile.Resources[2].ID)                 // native reversed
}

func TestFillFromDigest_SecretVersion(t *testing.T) {
	t.Parallel()
	// The SecretVersion is not a CFN resource; it matches the secret's logical ID
	// (via a core-sv-<logicalId> name) and is filled from the secret's live-enriched
	// SecretVersionImportID.
	digest := &StackDigest{Resources: []CfnResource{
		{LogicalID: "iacallbacktoken", CfnType: "AWS::SecretsManager::Secret",
			PulumiType:            "aws:secretsmanager/secret:Secret",
			PhysicalID:            "arn:aws:secretsmanager:us-east-1:1:secret:foo-abc",
			SecretVersionImportID: "arn:aws:secretsmanager:us-east-1:1:secret:foo-abc|v9"},
	}}
	importFile := &pkg.ImportFile{Resources: []pkg.ImportEntry{
		{Type: "aws:secretsmanager/secret:Secret", Name: "core-iacallbacktoken"},
		{Type: "aws:secretsmanager/secretVersion:SecretVersion", Name: "core-sv-iacallbacktoken"},
	}}
	res := FillFromDigest(digest, importFile, nil, "native")
	require.Equal(t, 2, res.Filled)
	require.Equal(t, "arn:aws:secretsmanager:us-east-1:1:secret:foo-abc", importFile.Resources[0].ID)
	require.Equal(t, "arn:aws:secretsmanager:us-east-1:1:secret:foo-abc|v9", importFile.Resources[1].ID)
}

func TestFillFromDigest_SecretVersionMissingEnrichment(t *testing.T) {
	t.Parallel()
	// SecretVersion entry but no live enrichment ran (no AWS access): don't fabricate
	// an ID from the secret ARN (which would be the wrong import format).
	digest := &StackDigest{Resources: []CfnResource{
		{LogicalID: "s", CfnType: "AWS::SecretsManager::Secret",
			PulumiType: "aws:secretsmanager/secret:Secret",
			PhysicalID: "arn:aws:secretsmanager:us-east-1:1:secret:s-abc"},
	}}
	importFile := &pkg.ImportFile{Resources: []pkg.ImportEntry{
		{Type: "aws:secretsmanager/secretVersion:SecretVersion", Name: "core-sv-s"},
	}}
	res := FillFromDigest(digest, importFile, nil, "native")
	require.Equal(t, 0, res.Filled)
	require.Equal(t, 1, res.Unmatched)
	require.Empty(t, importFile.Resources[0].ID)
}

func TestFillFromDigest_ComposeErrorNotFilled(t *testing.T) {
	t.Parallel()
	digest := &StackDigest{Resources: []CfnResource{
		// Missing "Id" (RoleStatement), which is required for Permission composition.
		{LogicalID: "ApiPermission", CfnType: "AWS::Lambda::Permission",
			PulumiType: "aws:lambda/permission:Permission",
			PhysicalID: "some-fallback-physical-id",
			Attributes: map[string]interface{}{"FunctionName": "ffs-dev-api"}},
	}}
	importFile := &pkg.ImportFile{Resources: []pkg.ImportEntry{
		{Type: "aws:lambda/permission:Permission", Name: "ffs-ApiPermission"},
	}}
	res := FillFromDigest(digest, importFile, nil, "native")
	require.Equal(t, 0, res.Filled)
	require.Equal(t, 1, res.Unmatched)
	require.Empty(t, importFile.Resources[0].ID, "must not fabricate an ID from PhysicalID when compose fails")
	require.Len(t, res.Warnings, 1)
	require.Contains(t, res.Warnings[0], "compose failed for ffs-ApiPermission")
}

func TestFillFromDigest_UnresolvedIntrinsicNotFilled(t *testing.T) {
	t.Parallel()
	digest := &StackDigest{Resources: []CfnResource{
		{LogicalID: "ApiPermission", CfnType: "AWS::Lambda::Permission",
			PulumiType: "aws:lambda/permission:Permission",
			Attributes: map[string]interface{}{
				"FunctionName": "<unresolved-intrinsic:Fn::Sub>",
				"Id":           "AllowApiGw",
			}},
	}}
	importFile := &pkg.ImportFile{Resources: []pkg.ImportEntry{
		{Type: "aws:lambda/permission:Permission", Name: "ffs-ApiPermission"},
	}}
	res := FillFromDigest(digest, importFile, nil, "native")
	require.Equal(t, 0, res.Filled)
	require.Equal(t, 1, res.Unmatched)
	require.Empty(t, importFile.Resources[0].ID)
	require.Len(t, res.Warnings, 1)
	require.Contains(t, res.Warnings[0], "unresolved intrinsic in import ID for ffs-ApiPermission")
}

func TestFillFromDigest_ComponentIncrementsSkipped(t *testing.T) {
	t.Parallel()
	digest := &StackDigest{Resources: []CfnResource{}}
	importFile := &pkg.ImportFile{Resources: []pkg.ImportEntry{
		{Name: "core_rds", Component: true},
	}}
	res := FillFromDigest(digest, importFile, nil, "native")
	require.Equal(t, 1, res.Skipped)
	require.Equal(t, 0, res.Filled)
	require.Equal(t, 0, res.Unmatched)
}

func TestFillFromDigest_PrePopulatedIDNotClobbered(t *testing.T) {
	t.Parallel()
	digest := &StackDigest{Resources: []CfnResource{
		{LogicalID: "TaskPolicy", CfnType: "AWS::IAM::Policy",
			PulumiType: "aws:iam/policy:Policy", ImportID: "arn:aws:iam::1:policy/should-not-be-used"},
	}}
	importFile := &pkg.ImportFile{Resources: []pkg.ImportEntry{
		{Type: "aws:iam/policy:Policy", Name: "ffs-TaskPolicy", ID: "arn:aws:iam::1:policy/already-set"},
	}}
	res := FillFromDigest(digest, importFile, nil, "native")
	require.Equal(t, 0, res.Filled)
	require.Equal(t, "arn:aws:iam::1:policy/already-set", importFile.Resources[0].ID)
}
