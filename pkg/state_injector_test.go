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

// TestCheckNoPlaceholders_NestedDepth covers the backstop's whole point: the
// targeted resolvers only look at top-level properties, so a placeholder
// buried inside a nested block is exactly what this is here to catch. No test
// went deeper than one level before — recorded as a unit-level gap in
// docs/superpowers/plans/2026-08-14-remaining-test-coverage.md.
//
// The path in the error matters as much as the detection: an operator given
// only "a placeholder is somewhere in this resource" cannot act on it.
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

// TestCheckNoPlaceholders_CleanNestedValuePasses is the other half: a deeply
// nested structure with no placeholder must not trip the backstop. Without
// this, a check that always errored would pass the tests above.
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

	// No config secrets supplied: the placeholder cannot be resolved, so the
	// command must fail rather than write "[secret]" into state.
	_, _, err := InjectNonImportable(
		minimalState(goodProviderRef), sidecar, preview, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "route_shared_key")
}

// TestInjectNonImportable_DropsMaskedSecretWithNoTerraformValue is the
// regression test for the e2e run of 2026-08-14, where every scenario failed
// on aws_iot_certificate's "ca_pem".
//
// A Pulumi provider marks a property secret from its schema, so "pulumi
// preview --json" emits "[secret]" for it whether or not it holds a value.
// Terraform's side is value-driven: redactSensitivePaths skips a null
// attribute (module_map.go), so the digest records no config key for it and
// none is written to stack config. Both halves are individually right; the
// mismatch is that the preview masks a property Terraform never had.
//
// The property must be dropped, not resolved and not fatal. There is no
// secret to recover, and leaving the literal "[secret]" in inputs would write
// a known-wrong value into state.
func TestInjectNonImportable_DropsMaskedSecretWithNoTerraformValue(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		attrs map[string]interface{}
	}{
		{
			// The shape aws_iot_certificate produces: the attribute exists in
			// state and is explicitly null.
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

// TestInjectNonImportable_MaskedSecretWithValueButNoConfigKeyFails pins the
// half of the guard that must survive the fix above. Here Terraform DOES have
// a value for the attribute, so a missing config key means the digest and the
// sidecar genuinely disagree — the corruption the hard error exists to catch.
// Dropping the property here would silently discard a real secret.
func TestInjectNonImportable_MaskedSecretWithValueButNoConfigKeyFails(t *testing.T) {
	t.Parallel()
	preview := propagationPreview(t)
	preview.Steps[0].NewState["inputs"].(map[string]interface{})["sharedKey"] = "[secret]"

	sidecar := propagationSidecar()
	sidecar.Resources[0].Attributes["shared_key"] = "(sensitive)"
	// RedactedAttributes deliberately left empty.

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

// TestResolveSecretInputs_AbsentAttributeDependsOnNameTrust covers the
// distinction that decides whether dropping a masked input is safe.
//
// A masked input whose Terraform attribute is ABSENT is the write-only case
// only if the Terraform name is trustworthy. When it came from the provider
// schema it is; when it came from PulumiToTerraformName it is a guess, and a
// wrong guess looks exactly the same — the bridge pluralises list and set
// names, which that transform cannot invert, so hundreds of real properties do
// not round-trip. Dropping on a guess silently deletes a program-declared
// input, and in file mode no preview ever runs to notice.
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
		// No schema: PulumiToTerraformName("subnetMappings") yields
		// "subnet_mappings", which the real attribute "subnet_mapping" does
		// not match. Previously this silently deleted the input.
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

// TestCheckNoPlaceholders_RejectsUnknownSentinel guards the value the engine
// writes for something not yet known at preview time. An injected resource can
// depend on another injected resource — orderInjected exists because those
// edges occur, and the e2e fixture has one — so at the injecting preview the
// dependency's outputs are unknown and the dependent's inputs carry this
// sentinel. Copied into state it becomes an ordinary-looking string that no
// later operation can tell from a real value.
func TestCheckNoPlaceholders_RejectsUnknownSentinel(t *testing.T) {
	t.Parallel()

	r := &NonImportableResource{Type: "aws:iam/x:X", Name: "attach"}

	err := checkNoPlaceholders(r, "input", map[string]interface{}{
		"target": unknownPlaceholder,
	}, "inputs")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown-value sentinel")
	assert.Contains(t, err.Error(), "inputs.target", "the path must be reported")

	// Nested, to confirm the walk reaches it.
	err = checkNoPlaceholders(r, "output", map[string]interface{}{
		"a": []interface{}{map[string]interface{}{"b": unknownPlaceholder}},
	}, "outputs")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outputs.a[0].b")

	// A real value must still pass.
	require.NoError(t, checkNoPlaceholders(r, "input", map[string]interface{}{
		"target": "arn:aws:iot:us-west-2:1234:cert/abc",
	}, "inputs"))
}

// TestFormatImportID_NullIsEmptyNotAngleNil pins the other half of the
// empty-ID guard: fmt.Sprintf("%v", nil) yields the literal "<nil>", which
// would be injected as though it were a real ID.
func TestFormatImportID_NullIsEmptyNotAngleNil(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "", formatImportID(nil))
	assert.Equal(t, "vpc-123", formatImportID("vpc-123"))
}

// TestInjectNonImportable_EmptyIDIsRejected guards the deployment invariant
// nothing else checks. VerifyDeploymentIntegrity only rejects the inverse case
// (a non-custom resource that HAS an ID), so an injected custom resource with
// an empty ID passed every check and then panicked the engine on the next
// operation — contract.Assertf(req.ID != "", "Diff requires an ID"). Some
// resource types are simultaneously id-less and non-importable, which is
// exactly the population injected here.
func TestInjectNonImportable_EmptyIDIsRejected(t *testing.T) {
	t.Parallel()

	preview := propagationPreview(t)
	sidecar := propagationSidecar()
	sidecar.Resources[0].ID = ""

	_, _, err := InjectNonImportable(minimalState(goodProviderRef), sidecar, preview, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no import ID")
}

// TestInjectNonImportable_EmptySidecarStillVerifies covers the only path
// through patch-state that verified nothing at all.
//
// The empty-sidecar early return hands back a NON-nil (empty) InjectResult, so
// the command's fallback check — guarded by "injectResult == nil" — was skipped
// too, and stack mode then imported the state into the live stack unverified.
// A sidecar with "resources": [] is what a run that found nothing writes, so
// this is reachable without hand-editing.
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

	// A sound state still passes, so the guard is not simply rejecting
	// everything.
	out, result, err := InjectNonImportable(minimalState(goodProviderRef), nil, nil, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 0, result.Injected)
	assert.NotEmpty(t, out)
}

// TestResolveUnknownInputs_SubstitutesFromOutputs covers the case the e2e
// fixture actually produces: an injected resource referencing another injected
// resource. At the preview that drives injection the dependency is not in state
// yet, so the engine serializes the referring input as its unknown sentinel.
//
// Before the guard that rejects the sentinel existed, this was written into
// state verbatim and twelve green e2e scenarios never noticed — it is an
// ordinary-looking string. The guard alone is not enough though: the value is
// knowable, because Terraform already created both resources and the real value
// is in the sidecar as this resource's own output.
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

	// And the result now passes the placeholder screen.
	require.NoError(t, checkNoPlaceholders(r, "input", inputs, "inputs"))
}

// TestResolveUnknownInputs_UnresolvableIsLeftForTheScreen pins the degradation.
// An unknown with no corresponding output means the program references
// something Terraform has no record of, so injection would be inventing the
// value. It stays unresolved and checkNoPlaceholders reports it with the path.
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
