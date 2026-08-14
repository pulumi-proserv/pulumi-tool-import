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

func TestInjectNonImportable_MetaWithoutDelta(t *testing.T) {
	t.Parallel()
	sidecar := propagationSidecar()
	// ComputeInjectionState (pkg/raw_state_delta.go) can report a schema
	// version even when it could not compute a delta at all: the schema
	// version comes straight from the provider's schema, independent of
	// whether delta computation succeeded. __meta must not be silently
	// dropped along with the (here, entirely absent) delta.
	sidecar.Resources[0].SchemaVersion = 2

	out, result, err := InjectNonImportable(
		minimalState(goodProviderRef), sidecar, propagationPreview(t), nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.DeltaAbsentFromSidecar)
	assert.Equal(t, 0, result.DeltaDroppedSensitive)
	assert.Equal(t, 0, result.DeltaDroppedUnrecoverable)
	require.Len(t, result.DeltaAbsentNotes, 1)
	assert.Contains(t, result.DeltaAbsentNotes[0], "aws_vpn_gateway_route_propagation.prop[0]")
	assert.Contains(t, result.DeltaAbsentNotes[0], "sidecar carried no raw-state delta")

	outputs := injected(t, out)["outputs"].(map[string]interface{})
	assert.NotContains(t, outputs, "__pulumi_raw_state_delta")
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
	assert.Equal(t, 1, result.DeltaDroppedUnrecoverable)
	assert.Equal(t, 0, result.DeltaAbsentFromSidecar)
	assert.Equal(t, 0, result.DeltaDroppedSensitive)
	require.Len(t, result.DeltaDroppedNotes, 1)
	assert.Contains(t, result.DeltaDroppedNotes[0], "aws_vpn_gateway_route_propagation.prop[0]")
	// The Recover error itself must survive into the note: it is the single
	// most useful fact for deciding whether the delta could be repaired.
	assert.Contains(t, result.DeltaDroppedNotes[0], "recover:")

	outputs := injected(t, out)["outputs"].(map[string]interface{})
	assert.NotContains(t, outputs, "__pulumi_raw_state_delta")
}

// TestInjectNonImportable_FillsOutputsMissingFromTerraform is the regression
// test for the whole-branch finding: "region" is per-resource in the Pulumi
// AWS provider (v7+, bridging terraform-provider-aws v6+) but provider-level
// configuration in Terraform AWS 5.x, so it never appears in a Terraform
// resource's state attributes and so never reaches the sidecar. The program's
// inputs do carry it (the preview create step reflects what the program
// declared), and a successfully created resource's outputs are normally a
// superset of its inputs — so a property Terraform has no opinion on must
// still be filled into outputs from inputs, or the next preview reports a
// spurious "update".
func TestInjectNonImportable_FillsOutputsMissingFromTerraform(t *testing.T) {
	t.Parallel()
	preview := propagationPreview(t)
	preview.Steps[0].NewState["inputs"].(map[string]interface{})["region"] = "us-west-2"

	sidecar := propagationSidecar()
	// The sidecar's Terraform attributes carry no "region": Terraform AWS 5.x
	// models it as provider config, not a resource attribute.

	out, _, err := InjectNonImportable(
		minimalState(goodProviderRef), sidecar, preview, nil, nil)
	require.NoError(t, err)

	r := injected(t, out)
	outputs := r["outputs"].(map[string]interface{})
	// Filled from inputs, since Terraform never reported it.
	assert.Equal(t, "us-west-2", outputs["region"])
	// Terraform-derived outputs are untouched.
	assert.Equal(t, "rtb-0e370d1fdde0890b3", outputs["routeTableId"])

	inputs := r["inputs"].(map[string]interface{})
	assert.Equal(t, "us-west-2", inputs["region"])

	require.NoError(t, VerifyDeploymentIntegrity(out))
}

// TestInjectNonImportable_FillDoesNotOverwriteTerraformOutput asserts the
// fill is one-directional: a property Terraform *did* report is never
// clobbered by the program's input, even when the two disagree (which can
// happen legitimately, e.g. a value Terraform computed differently than what
// was requested).
func TestInjectNonImportable_FillDoesNotOverwriteTerraformOutput(t *testing.T) {
	t.Parallel()
	preview := propagationPreview(t)
	// Disagrees with the sidecar's Attributes value for the same property.
	preview.Steps[0].NewState["inputs"].(map[string]interface{})["routeTableId"] = "rtb-DIFFERENT"

	out, _, err := InjectNonImportable(
		minimalState(goodProviderRef), propagationSidecar(), preview, nil, nil)
	require.NoError(t, err)

	outputs := injected(t, out)["outputs"].(map[string]interface{})
	// The Terraform-derived value wins; the input never overwrites it.
	assert.Equal(t, "rtb-0e370d1fdde0890b3", outputs["routeTableId"])
}

// TestInjectNonImportable_FillSkipsReservedKeys asserts the reserved bridge
// keys are handled the way the fill decided: __defaults is input-only
// bookkeeping and must never reach outputs, even though it is always present
// in inputs by the time the fill runs.
func TestInjectNonImportable_FillSkipsReservedKeys(t *testing.T) {
	t.Parallel()
	out, _, err := InjectNonImportable(
		minimalState(goodProviderRef), propagationSidecar(), propagationPreview(t), nil, nil)
	require.NoError(t, err)

	r := injected(t, out)
	inputs := r["inputs"].(map[string]interface{})
	require.Contains(t, inputs, "__defaults", "precondition: inputs must carry __defaults for this test to mean anything")

	outputs := r["outputs"].(map[string]interface{})
	assert.NotContains(t, outputs, "__defaults", "__defaults must never leak into outputs")
}

// TestInjectNonImportable_FillWrapsSecretInput is the regression test for the
// leak this fix could introduce: a secret input missing from outputs must be
// filled in still wrapped in Pulumi's secret envelope, never as a bare
// plaintext value.
func TestInjectNonImportable_FillWrapsSecretInput(t *testing.T) {
	t.Parallel()
	preview := propagationPreview(t)
	// A property the sidecar's Attributes never carries (like "region"), but
	// this time a masked secret in the program's inputs.
	preview.Steps[0].NewState["inputs"].(map[string]interface{})["presharedKey"] = "[secret]"

	sidecar := propagationSidecar()
	sidecar.Resources[0].RedactedAttributes = map[string]string{"preshared_key": "route_preshared_key"}

	out, result, err := InjectNonImportable(
		minimalState(goodProviderRef), sidecar, preview, nil,
		map[string]string{"route_preshared_key": "hunter2"})
	require.NoError(t, err)
	assert.Equal(t, 1, result.SecretsResolved)

	outputs := injected(t, out)["outputs"].(map[string]interface{})
	envelope, ok := outputs["presharedKey"].(map[string]interface{})
	require.True(t, ok, "a filled secret input must land in outputs still wrapped in the secret envelope")
	assert.Equal(t, "1b47061264138c4ac30d75fd1eb44270", envelope["4dabf18193072939515e22adb298388d"])
	assert.Equal(t, `"hunter2"`, envelope["plaintext"])

	// The backstop placeholder sweep must still pass on the filled value.
	require.NoError(t, VerifyDeploymentIntegrity(out))
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

// TestInjectNonImportable_ResolvedOutputSecretIsWrapped is the regression
// test for the whole-branch finding that resolveOutputSecrets wrote a
// resolved output secret bare, while resolveSecretInputs correctly wrapped
// the same kind of value for inputs. A VPN pre-shared key (or similar)
// resolved from stack config must land in the deployment's outputs inside
// Pulumi's secret envelope, not in plaintext.
func TestInjectNonImportable_ResolvedOutputSecretIsWrapped(t *testing.T) {
	t.Parallel()
	sidecar := propagationSidecar()
	sidecar.Resources[0].RedactedAttributes = map[string]string{"shared_key": "route_shared_key"}
	sidecar.Resources[0].PulumiOutputs = map[string]interface{}{
		"routeTableId": "rtb-0e370d1fdde0890b3",
		"vpnGatewayId": "vgw-0cdee3deb918b1983",
		"sharedKey":    "(sensitive)",
	}

	out, result, err := InjectNonImportable(
		minimalState(goodProviderRef), sidecar, propagationPreview(t), nil,
		map[string]string{"route_shared_key": "hunter2"})
	require.NoError(t, err)
	assert.Equal(t, 1, result.SecretsResolved)

	outputs := injected(t, out)["outputs"].(map[string]interface{})
	envelope, ok := outputs["sharedKey"].(map[string]interface{})
	require.True(t, ok, "resolved output secret must be written inside Pulumi's secret envelope, "+
		"not as a bare string")
	assert.Equal(t, "1b47061264138c4ac30d75fd1eb44270", envelope["4dabf18193072939515e22adb298388d"])
	assert.Equal(t, `"hunter2"`, envelope["plaintext"])

	// The backstop placeholder sweep must still pass: the envelope's own
	// values ("plaintext"'s JSON-encoded content, the sig) are not
	// themselves "(sensitive)" or "[secret]".
	require.NoError(t, VerifyDeploymentIntegrity(out))
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
