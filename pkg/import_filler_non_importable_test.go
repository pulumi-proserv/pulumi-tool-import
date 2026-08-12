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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// vpnDigest is a module holding one importable resource and one whose
// Terraform type declares no importer.
func vpnDigest() *ModuleMap {
	return &ModuleMap{
		Modules: map[string]*ModuleMapEntry{
			"net": {
				TerraformPath: "module.net",
				Resources: []ModuleResource{
					{
						Mode:             "managed",
						TranslatedURN:    "urn:pulumi:prod::proj::aws:ec2/routeTable:RouteTable::net-rtb",
						TerraformAddress: "module.net.aws_route_table.rtb",
						ImportID:         "rtb-0a1b2c3d4e5f60001",
					},
					{
						Mode: "managed",
						TranslatedURN: "urn:pulumi:prod::proj::aws:ec2/vpnGatewayRoutePropagation:" +
							"VpnGatewayRoutePropagation::net-route_prop0",
						TerraformAddress: "module.net.aws_vpn_gateway_route_propagation.route_prop0",
						ImportID:         "vgw-0a1b2c3d4e5f60718_rtb-0a1b2c3d4e5f60001",
						NonImportable:    true,
					},
				},
			},
		},
	}
}

func vpnImportFile() *ImportFile {
	return &ImportFile{
		Resources: []ImportEntry{
			{Type: "example:net:Net", Name: "net", Component: true},
			{Type: "aws:ec2/routeTable:RouteTable", Name: "net-rtb", ID: "<PLACEHOLDER>", Parent: "net"},
			{
				Type:   "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation",
				Name:   "net-route_prop0",
				ID:     "<PLACEHOLDER>",
				Parent: "net",
			},
		},
	}
}

func TestFillImportFileDropsNonImportableEntry(t *testing.T) {
	t.Parallel()

	importFile := vpnImportFile()
	FillImportFile(vpnDigest(), importFile, map[string]string{"module.net": "net"}, nil)

	for _, entry := range importFile.Resources {
		assert.NotEqual(t, "net-route_prop0", entry.Name,
			"a resource whose type declares no importer must not reach the import file")
	}
	require.Len(t, importFile.Resources, 2)
	assert.Equal(t, "rtb-0a1b2c3d4e5f60001", importFile.Resources[1].ID)
}

func TestFillImportFileReportsNonImportableEntry(t *testing.T) {
	t.Parallel()

	result := FillImportFile(vpnDigest(), vpnImportFile(), map[string]string{"module.net": "net"}, nil)

	require.Len(t, result.NonImportable, 1)
	dropped := result.NonImportable[0]
	assert.Equal(t, "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation", dropped.Type)
	assert.Equal(t, "net-route_prop0", dropped.Name)
	assert.Equal(t, "module.net.aws_vpn_gateway_route_propagation.route_prop0", dropped.TerraformAddress)
	assert.Equal(t, "vgw-0a1b2c3d4e5f60718_rtb-0a1b2c3d4e5f60001", dropped.ID)
}

// A dropped resource is neither filled nor left behind as unmatched work.
func TestFillImportFileCountsExcludeNonImportable(t *testing.T) {
	t.Parallel()

	result := FillImportFile(vpnDigest(), vpnImportFile(), map[string]string{"module.net": "net"}, nil)

	assert.Equal(t, 1, result.Filled)
	assert.Equal(t, 0, result.Unmatched)
}

// Resource-level mappings take a different path through the filler than module
// matching, and must drop the entry too.
func TestFillImportFileDropsNonImportableMatchedByResourceMapping(t *testing.T) {
	t.Parallel()

	digest := &ModuleMap{
		RootResources: []ModuleResource{
			{
				Mode: "managed",
				TranslatedURN: "urn:pulumi:prod::proj::aws:ec2/vpnConnectionRoute:" +
					"VpnConnectionRoute::vpn_route",
				TerraformAddress: "aws_vpn_connection_route.vpn_route",
				ImportID:         "10.0.0.10/32:vpn-0a1b2c3d4e5f60718",
				NonImportable:    true,
			},
		},
	}
	importFile := &ImportFile{
		Resources: []ImportEntry{
			{Type: "aws:ec2/vpnConnectionRoute:VpnConnectionRoute", Name: "vpn_route", ID: "<PLACEHOLDER>"},
		},
	}

	result := FillImportFile(digest, importFile,
		nil, map[string]string{"aws_vpn_connection_route.vpn_route": "vpn_route"})

	assert.Empty(t, importFile.Resources)
	require.Len(t, result.NonImportable, 1)
	assert.Equal(t, 0, result.Filled)
	assert.Equal(t, 0, result.Unmatched)
}

// Two resource mappings pointing at the same Pulumi name is an authoring
// mistake, but it must not lose a resource. A dropped entry keeps its
// <PLACEHOLDER> ID, so without a guard the second mapping refills it: the
// entry is counted as filled *and* removed from the import file, which is the
// exact silent-create hazard this feature exists to prevent.
//
// Map iteration order decides which mapping is seen first, so both orders are
// exercised by repetition. Either outcome is acceptable; the invariant is that
// the entry is filled or dropped, never both.
func TestFillImportFileNeverFillsAndDropsTheSameEntry(t *testing.T) {
	t.Parallel()

	for i := 0; i < 50; i++ {
		digest := &ModuleMap{
			RootResources: []ModuleResource{
				{
					Mode:             "managed",
					TranslatedURN:    "urn:pulumi:prod::proj::aws:ec2/vpnConnectionRoute:VpnConnectionRoute::shared",
					TerraformAddress: "aws_vpn_connection_route.blocked",
					ImportID:         "10.0.0.10/32:vpn-0a1b2c3d4e5f60718",
					NonImportable:    true,
				},
				{
					Mode:             "managed",
					TranslatedURN:    "urn:pulumi:prod::proj::aws:ec2/routeTable:RouteTable::shared",
					TerraformAddress: "aws_route_table.fine",
					ImportID:         "rtb-0a1b2c3d4e5f60001",
				},
			},
		}
		importFile := &ImportFile{
			Resources: []ImportEntry{
				{Type: "aws:ec2/routeTable:RouteTable", Name: "shared", ID: "<PLACEHOLDER>"},
			},
		}

		result := FillImportFile(digest, importFile, nil, map[string]string{
			"aws_vpn_connection_route.blocked": "shared",
			"aws_route_table.fine":             "shared",
		})

		require.Equal(t, 1, result.Filled+len(result.NonImportable),
			"entry was both filled and dropped (iteration %d)", i)
		if result.Filled == 1 {
			assert.Len(t, importFile.Resources, 1, "a filled entry must stay in the import file")
		} else {
			assert.Empty(t, importFile.Resources, "a dropped entry must leave the import file")
		}
	}
}

// The digest redacts sensitive top-level attributes to the literal
// "(sensitive)". Those values must not be presented as injectable: writing
// "(sensitive)" into stack state gives the resource a wrong value that, for
// these types, no refresh will ever correct.
func TestFillImportFileFlagsRedactedAttributes(t *testing.T) {
	t.Parallel()

	digest := &ModuleMap{
		RootResources: []ModuleResource{{
			Mode:             "managed",
			TranslatedURN:    "urn:pulumi:prod::proj::aws:ec2/vpnConnectionRoute:VpnConnectionRoute::route",
			TerraformAddress: "aws_vpn_connection_route.route",
			ImportID:         "10.0.0.10/32:vpn-0a1b2c3d4e5f60718",
			NonImportable:    true,
			Attributes: map[string]interface{}{
				"destination_cidr_block": "10.0.0.10/32",
				"shared_key":             "(sensitive)",
			},
		}},
	}
	importFile := &ImportFile{
		Resources: []ImportEntry{
			{Type: "aws:ec2/vpnConnectionRoute:VpnConnectionRoute", Name: "route", ID: "<PLACEHOLDER>"},
		},
	}

	result := FillImportFile(digest, importFile, nil,
		map[string]string{"aws_vpn_connection_route.route": "route"})

	require.Len(t, result.NonImportable, 1)
	// The real value is not lost: "digest tf" already wrote it into Pulumi
	// stack config as a secret, so the sidecar records where to find it.
	assert.Equal(t,
		map[string]string{"shared_key": flattenAddress("aws_vpn_connection_route.route", "shared_key")},
		result.NonImportable[0].RedactedAttributes)
}

func TestFillImportFileReportsNoRedactionWhenNoneIsPresent(t *testing.T) {
	t.Parallel()

	result := FillImportFile(vpnDigest(), vpnImportFile(), map[string]string{"module.net": "net"}, nil)

	require.Len(t, result.NonImportable, 1)
	assert.Empty(t, result.NonImportable[0].RedactedAttributes)
}
