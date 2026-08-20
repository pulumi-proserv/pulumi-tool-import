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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loadPreviewFixture(t *testing.T) *PreviewDigest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "preview_create.json"))
	require.NoError(t, err)
	d, err := ParsePreviewJSON(data)
	require.NoError(t, err)
	return d
}

func TestParsePreviewJSON_CreatesByTypeName(t *testing.T) {
	t.Parallel()
	d := loadPreviewFixture(t)

	creates, err := d.CreatesByTypeName()
	require.NoError(t, err)

	key := PreviewKey{
		Type: "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation",
		Name: "prop0",
	}
	state, err := creates.Lookup(key)
	require.NoError(t, err)
	require.NotNil(t, state, "create step should be keyed by type and name")

	assert.Equal(t,
		"urn:pulumi:dev::proj::pulumi:providers:aws::default_7_24_0::9f4c2b1e-0000-4000-8000-000000000001",
		state["provider"])
	assert.Equal(t, "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev", state["parent"])

	deps, ok := state["dependencies"].([]interface{})
	require.True(t, ok, "dependencies should be carried through verbatim")
	assert.Len(t, deps, 2)

	propDeps, ok := state["propertyDependencies"].(map[string]interface{})
	require.True(t, ok, "propertyDependencies should be carried through verbatim")
	assert.Contains(t, propDeps, "routeTableId")
}

func TestParsePreviewJSON_PreservesLargeIntegers(t *testing.T) {
	t.Parallel()
	d := loadPreviewFixture(t)

	var sameState map[string]interface{}
	for _, s := range d.Steps {
		if s.Op == "same" {
			sameState = s.NewState
		}
	}
	require.NotNil(t, sameState)

	inputs := sameState["inputs"].(map[string]interface{})
	num, ok := inputs["ownerId"].(json.Number)
	require.True(t, ok, "numbers must decode as json.Number, got %T", inputs["ownerId"])
	assert.Equal(t, "1234567890123456789", num.String())
}

func TestPreviewDigest_OpsByURN(t *testing.T) {
	t.Parallel()
	d := loadPreviewFixture(t)

	ops := d.OpsByURN()
	assert.Equal(t, "create",
		ops["urn:pulumi:dev::proj::aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation::prop0"])
	assert.Equal(t, "same", ops["urn:pulumi:dev::proj::aws:ec2/routeTable:RouteTable::rt0"])
	assert.Equal(t, map[string]int{"create": 1, "same": 1}, d.ChangeSummary)
}

func TestCreatesByTypeName_AmbiguousKeyOnlyFailsWhenUsed(t *testing.T) {
	t.Parallel()

	d, err := ParsePreviewJSON([]byte(`{"steps":[
		{"op":"create","urn":"urn:pulumi:dev::proj::my:mod:CompA$aws:s3/bucket:Bucket::logs",
		 "newState":{"urn":"urn:pulumi:dev::proj::my:mod:CompA$aws:s3/bucket:Bucket::logs"}},
		{"op":"create","urn":"urn:pulumi:dev::proj::my:mod:CompB$aws:s3/bucket:Bucket::logs",
		 "newState":{"urn":"urn:pulumi:dev::proj::my:mod:CompB$aws:s3/bucket:Bucket::logs"}},
		{"op":"create","urn":"urn:pulumi:dev::proj::aws:ec2/vpc:Vpc::main",
		 "newState":{"urn":"urn:pulumi:dev::proj::aws:ec2/vpc:Vpc::main"}}
	]}`))
	require.NoError(t, err)

	creates, err := d.CreatesByTypeName()
	require.NoError(t, err, "an ambiguous key must not block the whole preview")

	unrelated, err := creates.Lookup(PreviewKey{Type: "aws:ec2/vpc:Vpc", Name: "main"})
	require.NoError(t, err)
	require.NotNil(t, unrelated)

	_, err = creates.Lookup(PreviewKey{Type: "aws:s3/bucket:Bucket", Name: "logs"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CompA")
	assert.Contains(t, err.Error(), "CompB")

	missing, err := creates.Lookup(PreviewKey{Type: "aws:ec2/vpc:Vpc", Name: "nope"})
	require.NoError(t, err)
	assert.Nil(t, missing)
}

func TestSplitURN_NameContainingDoubleColon(t *testing.T) {
	t.Parallel()

	typ, name, err := splitURN(
		"urn:pulumi:dev::proj::aws:iam/rolePolicyAttachment:RolePolicyAttachment::" +
			"attach-arn:aws:iam::123456789012:role/admin")
	require.NoError(t, err)
	assert.Equal(t, "aws:iam/rolePolicyAttachment:RolePolicyAttachment", typ)
	assert.Equal(t, "attach-arn:aws:iam::123456789012:role/admin", name)

	typ, name, err = splitURN("urn:pulumi:dev::proj::my:mod:Certs$aws:iot/certificate:Certificate::cert")
	require.NoError(t, err)
	assert.Equal(t, "aws:iot/certificate:Certificate", typ)
	assert.Equal(t, "cert", name)

	_, _, err = splitURN("not-a-urn")
	require.Error(t, err)
}
