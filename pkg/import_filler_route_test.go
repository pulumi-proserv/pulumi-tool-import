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
)

// routeDigest builds a digest holding one aws_route whose Terraform state ID is
// the opaque "r-…" hash, with the given destination attributes.
func routeDigest(tfID string, attrs map[string]interface{}) *ModuleMap {
	attrs["route_table_id"] = "rtb-0a1b2c3d4e5f60004"
	return &ModuleMap{
		RootResources: []ModuleResource{{
			Mode:             "managed",
			ImportID:         tfID,
			TerraformAddress: "aws_route.public_to_sgi",
			Attributes:       attrs,
		}},
	}
}

func routeImportFile(tfID string) *ImportFile {
	return &ImportFile{
		Resources: []ImportEntry{
			{Type: "aws:ec2/route:Route", Name: "public_to_sgi", ID: tfID},
		},
	}
}

func TestTranslateImportIDsRouteWithIPv4Destination(t *testing.T) {
	t.Parallel()

	const tfID = "r-rtb-0a1b2c3d4e5f60004315494760"
	importFile := routeImportFile(tfID)
	digest := routeDigest(tfID, map[string]interface{}{
		"destination_cidr_block": "10.0.0.10/32",
	})

	assert.Equal(t, 1, TranslateImportIDs(importFile, digest))
	assert.Equal(t, "rtb-0a1b2c3d4e5f60004_10.0.0.10/32", importFile.Resources[0].ID)
}

func TestTranslateImportIDsRouteWithIPv6Destination(t *testing.T) {
	t.Parallel()

	const tfID = "r-rtb-0a1b2c3d4e5f60004315494761"
	importFile := routeImportFile(tfID)
	digest := routeDigest(tfID, map[string]interface{}{
		"destination_cidr_block":      "",
		"destination_ipv6_cidr_block": "2620:0:2d0:200::8/125",
	})

	assert.Equal(t, 1, TranslateImportIDs(importFile, digest))
	assert.Equal(t, "rtb-0a1b2c3d4e5f60004_2620:0:2d0:200::8/125", importFile.Resources[0].ID)
}

func TestTranslateImportIDsRouteWithPrefixListDestination(t *testing.T) {
	t.Parallel()

	const tfID = "r-rtb-0a1b2c3d4e5f60004315494762"
	importFile := routeImportFile(tfID)
	digest := routeDigest(tfID, map[string]interface{}{
		"destination_prefix_list_id": "pl-0570a1d2d725c16be",
	})

	assert.Equal(t, 1, TranslateImportIDs(importFile, digest))
	assert.Equal(t, "rtb-0a1b2c3d4e5f60004_pl-0570a1d2d725c16be", importFile.Resources[0].ID)
}

// Terraform writes null, not "", for destination attributes that are unset.
func TestTranslateImportIDsRouteIgnoresNullDestinations(t *testing.T) {
	t.Parallel()

	const tfID = "r-rtb-0a1b2c3d4e5f60004315494763"
	importFile := routeImportFile(tfID)
	digest := routeDigest(tfID, map[string]interface{}{
		"destination_cidr_block":      nil,
		"destination_ipv6_cidr_block": nil,
		"destination_prefix_list_id":  "pl-0570a1d2d725c16be",
	})

	assert.Equal(t, 1, TranslateImportIDs(importFile, digest))
	assert.Equal(t, "rtb-0a1b2c3d4e5f60004_pl-0570a1d2d725c16be", importFile.Resources[0].ID)
}

// With no destination there is nothing to compose, so the ID is left alone
// rather than turned into a half-formed one.
func TestTranslateImportIDsRouteWithoutDestinationIsLeftAlone(t *testing.T) {
	t.Parallel()

	const tfID = "r-rtb-0a1b2c3d4e5f60004315494764"
	importFile := routeImportFile(tfID)
	digest := routeDigest(tfID, map[string]interface{}{})

	assert.Equal(t, 0, TranslateImportIDs(importFile, digest))
	assert.Equal(t, tfID, importFile.Resources[0].ID)
}
