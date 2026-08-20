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

func TestLoadNonImportableFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "imports-ready.non-importable.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"_comment": "…",
		"resources": [
			{
				"type": "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation",
				"name": "prop0",
				"terraformAddress": "aws_vpn_gateway_route_propagation.prop[0]",
				"id": "vgw-0cdee3deb918b1983_rtb-0e370d1fdde0890b3",
				"attributes": {"route_table_id": "rtb-0e370d1fdde0890b3", "owner_id": 52848974346},
				"redactedAttributes": {"shared_key": "route_shared_key"}
			}
		]
	}`), 0o600))

	f, err := LoadNonImportableFile(path)
	require.NoError(t, err)
	require.Len(t, f.Resources, 1)

	r := f.Resources[0]
	assert.Equal(t, "prop0", r.Name)
	assert.Equal(t, "vgw-0cdee3deb918b1983_rtb-0e370d1fdde0890b3", r.ID)
	assert.Equal(t, "route_shared_key", r.RedactedAttributes["shared_key"])

	assert.Equal(t, "52848974346", r.Attributes["owner_id"].(json.Number).String())
}

func TestMapTFAttributesToPulumi(t *testing.T) {
	t.Parallel()
	fields := map[string]*SchemaFieldInfo{
		"route_table_id": {TFName: "route_table_id", PulumiName: "routeTableId"},
		"vpn_gateway_id": {TFName: "vpn_gateway_id", PulumiName: "vpnGatewayId"},
	}
	attrs := map[string]interface{}{
		"route_table_id": "rtb-1",
		"vpn_gateway_id": "vgw-1",
		"custom_thing":   "kept",
	}

	got := MapTFAttributesToPulumi(attrs, fields)

	assert.Equal(t, "rtb-1", got["routeTableId"])
	assert.Equal(t, "vgw-1", got["vpnGatewayId"])
	assert.Equal(t, "kept", got["customThing"])
	assert.NotContains(t, got, "route_table_id")
}

func TestPulumiToTFNames(t *testing.T) {
	t.Parallel()
	fields := map[string]*SchemaFieldInfo{
		"shared_key": {TFName: "shared_key", PulumiName: "sharedKey"},
	}
	assert.Equal(t, map[string]string{"sharedKey": "shared_key"}, PulumiToTFNames(fields))
}
