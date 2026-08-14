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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func propagationSidecar() *NonImportableFile {
	return &NonImportableFile{
		Resources: []NonImportableResource{
			{
				Type:             "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation",
				Name:             "prop0",
				TerraformAddress: "aws_vpn_gateway_route_propagation.prop[0]",
				ID:               "vgw-0cdee3deb918b1983_rtb-0e370d1fdde0890b3",
				Attributes: map[string]interface{}{
					"route_table_id": "rtb-0e370d1fdde0890b3",
					"vpn_gateway_id": "vgw-0cdee3deb918b1983",
				},
			},
		},
	}
}

func propagationPreview(t *testing.T) *PreviewDigest {
	t.Helper()
	d, err := ParsePreviewJSON([]byte(`{
	  "steps": [
	    {
	      "op": "create",
	      "urn": "urn:pulumi:dev::proj::aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation::prop0",
	      "newState": {
	        "urn": "urn:pulumi:dev::proj::aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation::prop0",
	        "type": "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation",
	        "custom": true,
	        "inputs": {"routeTableId": "rtb-0e370d1fdde0890b3", "vpnGatewayId": "vgw-0cdee3deb918b1983"},
	        "parent": "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev",
	        "provider": "urn:pulumi:dev::proj::pulumi:providers:aws::default_7_24_0::9f4c2b1e-0000-4000-8000-000000000001",
	        "protect": true,
	        "dependencies": ["urn:pulumi:dev::proj::aws:ec2/routeTable:RouteTable::rt0"]
	      }
	    }
	  ],
	  "changeSummary": {"create": 1}
	}`))
	require.NoError(t, err)
	return d
}

// injected pulls the last resource out of an injected state document.
func injected(t *testing.T, out []byte) map[string]interface{} {
	t.Helper()
	var doc map[string]interface{}
	require.NoError(t, json.Unmarshal(out, &doc))
	resources := doc["deployment"].(map[string]interface{})["resources"].([]interface{})
	return resources[len(resources)-1].(map[string]interface{})
}

func TestInjectNonImportable_CopiesPreviewMetadata(t *testing.T) {
	t.Parallel()
	out, result, err := InjectNonImportable(
		minimalState(goodProviderRef), propagationSidecar(), propagationPreview(t), nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Injected)

	r := injected(t, out)
	assert.Equal(t,
		"urn:pulumi:dev::proj::aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation::prop0",
		r["urn"])
	assert.Equal(t, true, r["custom"])
	assert.Equal(t, "vgw-0cdee3deb918b1983_rtb-0e370d1fdde0890b3", r["id"])
	assert.Equal(t, "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev", r["parent"])
	assert.Equal(t,
		"urn:pulumi:dev::proj::pulumi:providers:aws::default_7_24_0::9f4c2b1e-0000-4000-8000-000000000001",
		r["provider"])
	assert.Equal(t, true, r["protect"])
	assert.Equal(t, []interface{}{"urn:pulumi:dev::proj::aws:ec2/routeTable:RouteTable::rt0"},
		r["dependencies"])
}

func TestInjectNonImportable_OutputsFromSidecarInputsFromProgram(t *testing.T) {
	t.Parallel()
	out, _, err := InjectNonImportable(
		minimalState(goodProviderRef), propagationSidecar(), propagationPreview(t), nil, nil)
	require.NoError(t, err)

	r := injected(t, out)

	// Outputs come from the sidecar's Terraform attributes, renamed. With no
	// provider schema loaded the camelCase fallback applies.
	outputs := r["outputs"].(map[string]interface{})
	assert.Equal(t, "rtb-0e370d1fdde0890b3", outputs["routeTableId"])
	assert.Equal(t, "vgw-0cdee3deb918b1983", outputs["vpnGatewayId"])

	// Inputs come from the program, so preview diffs them to nothing.
	inputs := r["inputs"].(map[string]interface{})
	assert.Equal(t, "rtb-0e370d1fdde0890b3", inputs["routeTableId"])
	assert.Equal(t, []interface{}{}, inputs["__defaults"])

	// No sidecar delta was supplied here, so none is written.
	assert.NotContains(t, outputs, "__pulumi_raw_state_delta")
}

func TestInjectNonImportable_CarriesDigestOutputsAndDelta(t *testing.T) {
	t.Parallel()
	sidecar := propagationSidecar()
	// What "digest tf" computed while a provider was open (Task 2b).
	sidecar.Resources[0].PulumiOutputs = map[string]interface{}{
		"routeTableId": "rtb-0e370d1fdde0890b3",
		"vpnGatewayId": "vgw-0cdee3deb918b1983",
	}
	// A real digest-computed delta for a resource with no field-level deltas
	// still carries {"obj":{}} at the top: an object's raw state can never be
	// recovered from a bare {} (rawStateRecoverNatural refuses to recover
	// Object-typed values), only from an explicit, even if empty, "obj" node.
	// See ComputeInjectionState (pkg/raw_state_delta.go) and
	// TestComputeInjectionState_RandomPet, which shows the bridge itself
	// produces exactly this shape for a flat resource.
	sidecar.Resources[0].RawStateDelta = map[string]interface{}{"obj": map[string]interface{}{}}
	sidecar.Resources[0].SchemaVersion = 2

	out, _, err := InjectNonImportable(
		minimalState(goodProviderRef), sidecar, propagationPreview(t), nil, nil)
	require.NoError(t, err)

	r := injected(t, out)
	outputs := r["outputs"].(map[string]interface{})
	assert.Equal(t, "rtb-0e370d1fdde0890b3", outputs["routeTableId"])
	assert.Contains(t, outputs, "__pulumi_raw_state_delta")

	// __meta records the schema version the provider reported, so a later
	// provider upgrade runs the right state upgraders.
	assert.Contains(t, outputs["__meta"], "\"schema_version\":\"2\"")
}

func TestInjectNonImportable_DropsDeltaThatNoLongerRecovers(t *testing.T) {
	t.Parallel()
	sidecar := propagationSidecar()
	sidecar.Resources[0].PulumiOutputs = map[string]interface{}{"routeTableId": "rtb-1"}
	// A delta that cannot apply to these outputs: it claims routeTableId's raw
	// representation is an array delta, but the output value is a string.
	// (An arbitrary unknown key under "obj", e.g. {"nope": {}}, does NOT
	// exercise this path — encoding/json silently ignores unrecognized object
	// fields when unmarshalling into objDelta, so such a delta is
	// indistinguishable from an empty one and recovers just fine. Verified
	// empirically against the real tfbridge.UnmarshalRawStateDelta/Recover.)
	// Rather than write state that fails at the next preview, injection drops
	// the delta and falls back to the bridge's pre-delta reconstruction.
	sidecar.Resources[0].RawStateDelta = map[string]interface{}{
		"obj": map[string]interface{}{
			"ps": map[string]interface{}{
				"routeTableId": map[string]interface{}{"arr": map[string]interface{}{}},
			},
		},
	}

	out, result, err := InjectNonImportable(
		minimalState(goodProviderRef), sidecar, propagationPreview(t), nil, nil)
	require.NoError(t, err, "an unusable delta must not fail the injection")
	assert.Equal(t, 1, result.NoDelta)

	outputs := injected(t, out)["outputs"].(map[string]interface{})
	assert.NotContains(t, outputs, "__pulumi_raw_state_delta")
}

func TestInjectNonImportable_NoMatchingCreateFails(t *testing.T) {
	t.Parallel()
	sidecar := propagationSidecar()
	sidecar.Resources[0].Name = "not-in-the-program"

	_, _, err := InjectNonImportable(
		minimalState(goodProviderRef), sidecar, propagationPreview(t), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not-in-the-program")
}

func TestInjectNonImportable_UnresolvedSecretFails(t *testing.T) {
	t.Parallel()
	preview := propagationPreview(t)
	preview.Steps[0].NewState["inputs"].(map[string]interface{})["sharedKey"] = "[secret]"

	sidecar := propagationSidecar()
	sidecar.Resources[0].RedactedAttributes = map[string]string{"shared_key": "route_shared_key"}

	// No config secrets supplied: the placeholder cannot be resolved, so the
	// command must fail rather than write "[secret]" into state.
	_, _, err := InjectNonImportable(
		minimalState(goodProviderRef), sidecar, preview, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "route_shared_key")
}

func TestInjectNonImportable_ResolvesSecretFromConfig(t *testing.T) {
	t.Parallel()
	preview := propagationPreview(t)
	preview.Steps[0].NewState["inputs"].(map[string]interface{})["sharedKey"] = "[secret]"

	sidecar := propagationSidecar()
	sidecar.Resources[0].RedactedAttributes = map[string]string{"shared_key": "route_shared_key"}

	out, result, err := InjectNonImportable(
		minimalState(goodProviderRef), sidecar, preview, nil,
		map[string]string{"route_shared_key": "hunter2"})
	require.NoError(t, err)
	assert.Equal(t, 1, result.SecretsResolved)

	inputs := injected(t, out)["inputs"].(map[string]interface{})
	envelope, ok := inputs["sharedKey"].(map[string]interface{})
	require.True(t, ok, "secret must be written inside Pulumi's secret envelope")
	assert.Equal(t, "1b47061264138c4ac30d75fd1eb44270", envelope["4dabf18193072939515e22adb298388d"])
	assert.Equal(t, `"hunter2"`, envelope["plaintext"])
}

func TestInjectNonImportable_OutputPassesIntegrityCheck(t *testing.T) {
	t.Parallel()
	out, _, err := InjectNonImportable(
		minimalState(goodProviderRef), propagationSidecar(), propagationPreview(t), nil, nil)
	require.NoError(t, err)
	require.NoError(t, VerifyDeploymentIntegrity(out))
}

func TestInjectNonImportable_OrdersDependenciesFirst(t *testing.T) {
	t.Parallel()
	// Two injected resources where the second depends on the first. The
	// dependency must be written earlier in the array: VerifyIntegrity rejects a
	// forward reference.
	preview, err := ParsePreviewJSON([]byte(`{
	  "steps": [
	    {
	      "op": "create",
	      "urn": "urn:pulumi:dev::proj::aws:ec2/vpnConnectionRoute:VpnConnectionRoute::route",
	      "newState": {
	        "urn": "urn:pulumi:dev::proj::aws:ec2/vpnConnectionRoute:VpnConnectionRoute::route",
	        "type": "aws:ec2/vpnConnectionRoute:VpnConnectionRoute",
	        "custom": true,
	        "inputs": {},
	        "provider": "urn:pulumi:dev::proj::pulumi:providers:aws::default_7_24_0::9f4c2b1e-0000-4000-8000-000000000001",
	        "dependencies": ["urn:pulumi:dev::proj::aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation::prop0"]
	      }
	    },
	    {
	      "op": "create",
	      "urn": "urn:pulumi:dev::proj::aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation::prop0",
	      "newState": {
	        "urn": "urn:pulumi:dev::proj::aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation::prop0",
	        "type": "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation",
	        "custom": true,
	        "inputs": {},
	        "provider": "urn:pulumi:dev::proj::pulumi:providers:aws::default_7_24_0::9f4c2b1e-0000-4000-8000-000000000001"
	      }
	    }
	  ]
	}`))
	require.NoError(t, err)

	sidecar := &NonImportableFile{Resources: []NonImportableResource{
		{
			Type: "aws:ec2/vpnConnectionRoute:VpnConnectionRoute",
			Name: "route", ID: "vpn-1_10.0.0.0/16",
			Attributes: map[string]interface{}{"destination_cidr_block": "10.0.0.0/16"},
		},
		{
			Type: "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation",
			Name: "prop0", ID: "vgw-1_rtb-1",
			Attributes: map[string]interface{}{"route_table_id": "rtb-1"},
		},
	}}

	out, result, err := InjectNonImportable(minimalState(goodProviderRef), sidecar, preview, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, result.Injected)

	var doc map[string]interface{}
	require.NoError(t, json.Unmarshal(out, &doc))
	resources := doc["deployment"].(map[string]interface{})["resources"].([]interface{})

	last := resources[len(resources)-1].(map[string]interface{})
	secondLast := resources[len(resources)-2].(map[string]interface{})
	assert.Contains(t, last["urn"], "VpnConnectionRoute", "dependent must come last")
	assert.Contains(t, secondLast["urn"], "VpnGatewayRoutePropagation", "dependency must come first")

	require.NoError(t, VerifyDeploymentIntegrity(out))
}
