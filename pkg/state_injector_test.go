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

	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge"
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

	outputs := r["outputs"].(map[string]interface{})
	assert.Equal(t, "rtb-0e370d1fdde0890b3", outputs["routeTableId"])
	assert.Equal(t, "vgw-0cdee3deb918b1983", outputs["vpnGatewayId"])

	inputs := r["inputs"].(map[string]interface{})
	assert.Equal(t, "rtb-0e370d1fdde0890b3", inputs["routeTableId"])
	assert.Equal(t, []interface{}{}, inputs["__defaults"])

	assert.NotContains(t, outputs, "__pulumi_raw_state_delta")
}

func TestInjectNonImportable_CarriesDigestOutputsAndDelta(t *testing.T) {
	t.Parallel()
	sidecar := propagationSidecar()
	sidecar.Resources[0].PulumiOutputs = map[string]interface{}{
		"routeTableId": "rtb-0e370d1fdde0890b3",
		"vpnGatewayId": "vgw-0cdee3deb918b1983",
	}
	sidecar.Resources[0].RawStateDelta = map[string]interface{}{"obj": map[string]interface{}{}}
	sidecar.Resources[0].SchemaVersion = 2

	out, _, err := InjectNonImportable(
		minimalState(goodProviderRef), sidecar, propagationPreview(t), nil, nil)
	require.NoError(t, err)

	r := injected(t, out)
	outputs := r["outputs"].(map[string]interface{})
	assert.Equal(t, "rtb-0e370d1fdde0890b3", outputs["routeTableId"])
	assert.Contains(t, outputs, "__pulumi_raw_state_delta")

	assert.Contains(t, outputs["__meta"], "\"schema_version\":\"2\"")
}

func TestInjectNonImportable_MetaWithoutDelta(t *testing.T) {
	t.Parallel()
	sidecar := propagationSidecar()
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
	assert.Contains(t, result.DeltaDroppedNotes[0], "recover:")

	outputs := injected(t, out)["outputs"].(map[string]interface{})
	assert.NotContains(t, outputs, "__pulumi_raw_state_delta")
}

func TestInjectNonImportable_FillsOutputsMissingFromTerraform(t *testing.T) {
	t.Parallel()
	preview := propagationPreview(t)
	preview.Steps[0].NewState["inputs"].(map[string]interface{})["region"] = "us-west-2"

	sidecar := propagationSidecar()

	out, _, err := InjectNonImportable(
		minimalState(goodProviderRef), sidecar, preview, nil, nil)
	require.NoError(t, err)

	r := injected(t, out)
	outputs := r["outputs"].(map[string]interface{})
	assert.Equal(t, "us-west-2", outputs["region"])
	assert.Equal(t, "rtb-0e370d1fdde0890b3", outputs["routeTableId"])

	inputs := r["inputs"].(map[string]interface{})
	assert.Equal(t, "us-west-2", inputs["region"])

	require.NoError(t, VerifyDeploymentIntegrity(out))
}

func TestInjectNonImportable_FillDoesNotOverwriteTerraformOutput(t *testing.T) {
	t.Parallel()
	preview := propagationPreview(t)
	preview.Steps[0].NewState["inputs"].(map[string]interface{})["routeTableId"] = "rtb-DIFFERENT"

	out, _, err := InjectNonImportable(
		minimalState(goodProviderRef), propagationSidecar(), preview, nil, nil)
	require.NoError(t, err)

	outputs := injected(t, out)["outputs"].(map[string]interface{})
	assert.Equal(t, "rtb-0e370d1fdde0890b3", outputs["routeTableId"])
}

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

func TestInjectNonImportable_FillWrapsSecretInput(t *testing.T) {
	t.Parallel()
	preview := propagationPreview(t)
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

	require.NoError(t, VerifyDeploymentIntegrity(out))
}

func TestCheckNoPlaceholders_NestedDepth(t *testing.T) {
	t.Parallel()
	r := &NonImportableResource{
		Type: "aws:vpclattice/targetGroupAttachment:TargetGroupAttachment",
		Name: "attach",
	}

	for _, tc := range []struct {
		name        string
		value       interface{}
		placeholder string
		wantPath    string
	}{
		{
			name: "map inside array inside map",
			value: map[string]interface{}{
				"target": []interface{}{
					map[string]interface{}{"id": secretPlaceholder},
				},
			},
			placeholder: secretPlaceholder,
			wantPath:    "outputs.target[0].id",
		},
		{
			name: "four levels of nesting",
			value: map[string]interface{}{
				"a": map[string]interface{}{
					"b": map[string]interface{}{
						"c": map[string]interface{}{"d": redactedPlaceholder},
					},
				},
			},
			placeholder: redactedPlaceholder,
			wantPath:    "outputs.a.b.c.d",
		},
		{
			name: "array of arrays",
			value: map[string]interface{}{
				"rules": []interface{}{
					[]interface{}{"fine", secretPlaceholder},
				},
			},
			placeholder: secretPlaceholder,
			wantPath:    "outputs.rules[0][1]",
		},
		{
			name: "later sibling, after clean branches",
			value: map[string]interface{}{
				"aaa": "clean",
				"bbb": map[string]interface{}{"nested": "also clean"},
				"ccc": []interface{}{"clean", map[string]interface{}{"deep": redactedPlaceholder}},
			},
			placeholder: redactedPlaceholder,
			wantPath:    "outputs.ccc[1].deep",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := checkNoPlaceholders(r, "outputs", tc.value, "outputs")
			require.Error(t, err, "a placeholder at %s must be found", tc.wantPath)
			assert.Contains(t, err.Error(), tc.wantPath,
				"the error must name the path, not just the resource")
			assert.Contains(t, err.Error(), tc.placeholder)
			assert.Contains(t, err.Error(), "attach")
		})
	}
}

func TestCheckNoPlaceholders_CleanNestedValuePasses(t *testing.T) {
	t.Parallel()
	r := &NonImportableResource{Type: "aws:ec2/x:X", Name: "clean"}
	value := map[string]interface{}{
		"target": []interface{}{
			map[string]interface{}{"id": "arn:aws:lambda:us-west-2:1234:function:f", "port": nil},
		},
		"nested": map[string]interface{}{
			"deep": []interface{}{"(not sensitive)", "[not a secret]", ""},
		},
	}
	assert.NoError(t, checkNoPlaceholders(r, "outputs", value, "outputs"))
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

	_, _, err := InjectNonImportable(
		minimalState(goodProviderRef), sidecar, preview, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "route_shared_key")
}

func TestInjectNonImportable_DropsMaskedSecretWithNoTerraformValue(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		attrs map[string]interface{}
	}{
		{
			name: "explicitly null",
			attrs: map[string]interface{}{
				"route_table_id": "rtb-0e370d1fdde0890b3",
				"vpn_gateway_id": "vgw-0cdee3deb918b1983",
				"shared_key":     nil,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			preview := propagationPreview(t)
			preview.Steps[0].NewState["inputs"].(map[string]interface{})["sharedKey"] = "[secret]"

			sidecar := propagationSidecar()
			sidecar.Resources[0].Attributes = tc.attrs

			out, result, err := InjectNonImportable(
				minimalState(goodProviderRef), sidecar, preview, nil, nil)
			require.NoError(t, err)
			assert.Equal(t, 0, result.SecretsResolved,
				"nothing was resolved: there was no secret to recover")

			inputs := injected(t, out)["inputs"].(map[string]interface{})
			_, present := inputs["sharedKey"]
			assert.False(t, present,
				"a masked property with no Terraform value must be dropped, not written as %q",
				secretPlaceholder)
		})
	}
}

func TestInjectNonImportable_MaskedSecretWithValueButNoConfigKeyFails(t *testing.T) {
	t.Parallel()
	preview := propagationPreview(t)
	preview.Steps[0].NewState["inputs"].(map[string]interface{})["sharedKey"] = "[secret]"

	sidecar := propagationSidecar()
	sidecar.Resources[0].Attributes["shared_key"] = "(sensitive)"

	_, _, err := InjectNonImportable(
		minimalState(goodProviderRef), sidecar, preview, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shared_key")
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

func TestResolveSecretInputs_AbsentAttributeDependsOnNameTrust(t *testing.T) {
	t.Parallel()

	newResource := func() *NonImportableResource {
		return &NonImportableResource{
			Type: "aws:ec2/x:X", Name: "r",
			Attributes:         map[string]interface{}{"other": "v"},
			RedactedAttributes: map[string]string{},
		}
	}

	t.Run("schema-mapped name is trusted, so the input is dropped", func(t *testing.T) {
		t.Parallel()
		inputs := map[string]interface{}{"subnetMappings": secretPlaceholder}
		fields := map[string]*SchemaFieldInfo{
			"subnet_mapping": {TFName: "subnet_mapping", PulumiName: "subnetMappings"},
		}
		n, err := resolveSecretInputs(newResource(), inputs, fields, nil)
		require.NoError(t, err)
		assert.Equal(t, 0, n)
		_, present := inputs["subnetMappings"]
		assert.False(t, present, "a schema-mapped name with no attribute is genuinely write-only")
	})

	t.Run("guessed name is not trusted, so it is an error", func(t *testing.T) {
		t.Parallel()
		inputs := map[string]interface{}{"subnetMappings": secretPlaceholder}
		_, err := resolveSecretInputs(newResource(), inputs, nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no provider schema was loaded")
		assert.Contains(t, inputs, "subnetMappings", "the input must not be dropped on a guess")
	})

	t.Run("nil Attributes cannot support any conclusion", func(t *testing.T) {
		t.Parallel()
		r := newResource()
		r.Attributes = nil
		inputs := map[string]interface{}{"password": secretPlaceholder}
		_, err := resolveSecretInputs(r, inputs, nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no Terraform attributes")
	})
}

func TestCheckNoPlaceholders_RejectsUnknownSentinel(t *testing.T) {
	t.Parallel()

	r := &NonImportableResource{Type: "aws:iam/x:X", Name: "attach"}

	err := checkNoPlaceholders(r, "input", map[string]interface{}{
		"target": unknownPlaceholder,
	}, "inputs")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown-value sentinel")
	assert.Contains(t, err.Error(), "inputs.target", "the path must be reported")

	err = checkNoPlaceholders(r, "output", map[string]interface{}{
		"a": []interface{}{map[string]interface{}{"b": unknownPlaceholder}},
	}, "outputs")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outputs.a[0].b")

	require.NoError(t, checkNoPlaceholders(r, "input", map[string]interface{}{
		"target": "arn:aws:iot:us-west-2:1234:cert/abc",
	}, "inputs"))
}

func TestFormatImportID_NullIsEmptyNotAngleNil(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "", formatImportID(nil))
	assert.Equal(t, "vpc-123", formatImportID("vpc-123"))
}

func TestInjectNonImportable_EmptyIDIsRejected(t *testing.T) {
	t.Parallel()

	preview := propagationPreview(t)
	sidecar := propagationSidecar()
	sidecar.Resources[0].ID = ""

	_, _, err := InjectNonImportable(minimalState(goodProviderRef), sidecar, preview, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no import ID")
}

func TestInjectNonImportable_EmptySidecarStillVerifies(t *testing.T) {
	t.Parallel()

	corrupt := minimalState("")

	for _, tc := range []struct {
		name    string
		sidecar *NonImportableFile
	}{
		{"nil sidecar", nil},
		{"empty resources", &NonImportableFile{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := InjectNonImportable(corrupt, tc.sidecar, nil, nil, nil)
			require.Error(t, err, "corrupt state must not pass through unverified")
		})
	}

	out, result, err := InjectNonImportable(minimalState(goodProviderRef), nil, nil, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 0, result.Injected)
	assert.NotEmpty(t, out)
}

func TestResolveUnknownInputs_SubstitutesFromOutputs(t *testing.T) {
	t.Parallel()

	r := &NonImportableResource{Type: "aws:iot/policyAttachment:PolicyAttachment", Name: "policy_attach"}
	inputs := map[string]interface{}{
		"target": unknownPlaceholder,
		"policy": "my-policy",
	}
	outputs := map[string]interface{}{
		"target": "arn:aws:iot:us-west-2:123456789012:cert/abc123",
		"policy": "my-policy",
	}

	require.NoError(t, resolveUnknownInputs(r, inputs, outputs))
	assert.Equal(t, "arn:aws:iot:us-west-2:123456789012:cert/abc123", inputs["target"])
	assert.Equal(t, "my-policy", inputs["policy"], "a resolved input must not be disturbed")

	require.NoError(t, checkNoPlaceholders(r, "input", inputs, "inputs"))
}

func TestResolveUnknownInputs_UnresolvableIsLeftForTheScreen(t *testing.T) {
	t.Parallel()

	r := &NonImportableResource{Type: "aws:iot/policyAttachment:PolicyAttachment", Name: "attach"}
	for _, tc := range []struct {
		name    string
		outputs map[string]interface{}
	}{
		{"no such output", map[string]interface{}{"other": "v"}},
		{"output is null", map[string]interface{}{"target": nil}},
		{"output is itself unknown", map[string]interface{}{"target": unknownPlaceholder}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			inputs := map[string]interface{}{"target": unknownPlaceholder}
			require.NoError(t, resolveUnknownInputs(r, inputs, tc.outputs))
			assert.Equal(t, unknownPlaceholder, inputs["target"])

			err := checkNoPlaceholders(r, "input", inputs, "inputs")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "unknown-value sentinel")
		})
	}
}

func TestInjectResult_DeltaAttachedIsReportedPositively(t *testing.T) {
	t.Parallel()

	preview := propagationPreview(t)
	sidecar := propagationSidecar()

	_, result, err := InjectNonImportable(
		minimalState(goodProviderRef), sidecar, preview, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Injected)
	assert.Equal(t, 0, result.DeltaAttached)
	assert.Equal(t, 1, result.DeltaAbsentFromSidecar,
		"the absence must still be counted and explained")

	withDelta := propagationSidecar()
	withDelta.Resources[0].RawStateDelta = map[string]interface{}{
		"obj": map[string]interface{}{},
	}
	_, result, err = InjectNonImportable(
		minimalState(goodProviderRef), withDelta, preview, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Injected)
	assert.Equal(t, 1, result.DeltaAttached)
	assert.Equal(t, 0, result.DeltaAbsentFromSidecar)
	assert.Equal(t, 0, result.DeltaDroppedSensitive)
	assert.Equal(t, 0, result.DeltaDroppedUnrecoverable)

	total := result.DeltaAttached + result.DeltaAbsentFromSidecar +
		result.DeltaDroppedSensitive + result.DeltaDroppedUnrecoverable
	assert.Equal(t, result.Injected, total,
		"every injected resource must land in exactly one delta bucket")
}

func TestEnvelopeReplaceNodes_MatchesWhatTheBridgeDoes(t *testing.T) {
	t.Parallel()

	delta := map[string]interface{}{
		"obj": map[string]interface{}{
			"ps": map[string]interface{}{
				"policy": map[string]interface{}{
					"replace": map[string]interface{}{"raw": `{"secretish":"value"}`},
				},
				"plain": map[string]interface{}{"obj": map[string]interface{}{}},
			},
			"ignored": []interface{}{"region"},
		},
	}

	got := envelopeReplaceNodes(delta).(map[string]interface{})
	ps := got["obj"].(map[string]interface{})["ps"].(map[string]interface{})

	policy := ps["policy"].(map[string]interface{})
	assert.Equal(t, secretSig, policy[sigKey], "a Replace node must carry the secret envelope")
	assert.NotContains(t, policy, "replace", "the raw payload must be inside the envelope")
	assert.Contains(t, policy["plaintext"], "secretish")

	assert.NotContains(t, ps["plain"], sigKey, "structural nodes must stay plain")
	assert.Equal(t, []interface{}{"region"},
		got["obj"].(map[string]interface{})["ignored"], "non-map values pass through")

	pv := propertyValueFromState(got)
	recovered, err := tfbridge.UnmarshalRawStateDelta(pv)
	require.NoError(t, err)
	roundTripped, err := json.Marshal(recovered)
	require.NoError(t, err)
	original, err := json.Marshal(delta)
	require.NoError(t, err)

	var a, b interface{}
	require.NoError(t, json.Unmarshal(roundTripped, &a))
	require.NoError(t, json.Unmarshal(original, &b))
	assert.Equal(t, b, a, "enveloping must not change what the bridge reads back")
}

func TestEnvelopeReplaceNodes_LeavesADeltaWithoutReplaceUntouched(t *testing.T) {
	t.Parallel()

	delta := map[string]interface{}{"obj": map[string]interface{}{"ps": map[string]interface{}{}}}
	before, err := json.Marshal(delta)
	require.NoError(t, err)
	after, err := json.Marshal(envelopeReplaceNodes(delta))
	require.NoError(t, err)
	assert.JSONEq(t, string(before), string(after))
}
