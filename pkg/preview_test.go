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

	// Only the create step is collected; the "same" step is ignored.
	require.Len(t, creates, 1)

	key := PreviewKey{
		Type: "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation",
		Name: "prop0",
	}
	state, ok := creates[key]
	require.True(t, ok, "create step should be keyed by type and name")

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

	// The "same" step carries a 19-digit integer, beyond float64's exact
	// integer range (2^53). Decoding without UseNumber silently turns it
	// into a different integer with no error and no malformed output.
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
