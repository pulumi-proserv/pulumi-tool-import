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

package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNonImportablePathSitsBesideTheImportFile(t *testing.T) {
	t.Parallel()
	assert.Equal(t, filepath.Join("out", "imports-ready.non-importable.json"),
		nonImportablePath(filepath.Join("out", "imports-ready.json")))
}

func TestNonImportablePathWithoutExtension(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "imports-ready.non-importable.json", nonImportablePath("imports-ready"))
}

func TestWriteNonImportableRoundTrips(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "imports.non-importable.json")

	resources := []pkg.NonImportableResource{{
		Type:             "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation",
		Name:             "net-route_prop0",
		TerraformAddress: "module.net.aws_vpn_gateway_route_propagation.route_prop0",
		ID:               "vgw-0a1b2c3d4e5f60718_rtb-0a1b2c3d4e5f60001",
		Attributes: map[string]interface{}{
			"route_table_id": "rtb-0a1b2c3d4e5f60001",
			"vpn_gateway_id": "vgw-0a1b2c3d4e5f60718",
		},
	}}

	require.NoError(t, writeNonImportable(path, resources))

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var readBack struct {
		Resources []pkg.NonImportableResource `json:"resources"`
	}
	require.NoError(t, json.Unmarshal(data, &readBack))
	require.Len(t, readBack.Resources, 1)
	assert.Equal(t, resources[0].ID, readBack.Resources[0].ID)
	assert.Equal(t, "rtb-0a1b2c3d4e5f60001", readBack.Resources[0].Attributes["route_table_id"])
}
