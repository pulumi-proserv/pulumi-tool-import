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
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg/providermap"
	"github.com/pulumi-proserv/pulumi-tool-import/pkg/tfprovider"
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge"
	shim "github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfshim"
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/vendored/opentofu/providers"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func skipOrFailUnavailable(t *testing.T, err error, what string) {
	t.Helper()
	if os.Getenv("CI") != "" {
		t.Fatalf("%s: %v (CI must not silently skip the nested-block delta test)", what, err)
	}
	t.Skipf("%s: %v", what, err)
}

func TestComputeInjectionState_RandomPet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	prov, err := tfprovider.LoadProvider(ctx, "registry.terraform.io/hashicorp/random", "3.7.2")
	require.NoError(t, err)
	defer prov.Close(ctx)

	attrs := []byte(`{"id":"cool-pet","keepers":null,"length":2,"prefix":null,"separator":"-"}`)

	outputs, delta, deltaReason, version, err := ComputeInjectionState(ctx, prov, "random_pet", attrs, nil, nil)
	require.NoError(t, err)

	assert.Equal(t, "cool-pet", outputs["id"])
	assert.Equal(t, "-", outputs["separator"])

	assert.GreaterOrEqual(t, version, int64(0))

	assert.NotNil(t, delta)
	assert.Empty(t, deltaReason, "a produced delta must not also carry a reason it is missing")
}

const (
	nestedBlockTestAWSProviderAddr    = "registry.opentofu.org/hashicorp/aws"
	nestedBlockTestAWSProviderVersion = "5.100.0"
)

const vpnConnectionAttrsJSON = `{
	"arn": "arn:aws:ec2:us-east-1:123456789012:vpn-connection/vpn-1234abcd",
	"core_network_arn": null,
	"core_network_attachment_arn": null,
	"customer_gateway_configuration": null,
	"customer_gateway_id": "cgw-1234abcd",
	"enable_acceleration": null,
	"id": "vpn-1234abcd",
	"local_ipv4_network_cidr": null,
	"local_ipv6_network_cidr": null,
	"outside_ip_address_type": null,
	"preshared_key_arn": null,
	"preshared_key_storage": null,
	"remote_ipv4_network_cidr": null,
	"remote_ipv6_network_cidr": null,
	"routes": [],
	"static_routes_only": false,
	"tags": {},
	"tags_all": {},
	"transit_gateway_attachment_id": null,
	"transit_gateway_id": null,
	"transport_transit_gateway_attachment_id": null,
	"tunnel1_address": null,
	"tunnel1_bgp_asn": null,
	"tunnel1_bgp_holdtime": null,
	"tunnel1_cgw_inside_address": null,
	"tunnel1_dpd_timeout_action": null,
	"tunnel1_dpd_timeout_seconds": null,
	"tunnel1_enable_tunnel_lifecycle_control": null,
	"tunnel1_ike_versions": [],
	"tunnel1_inside_cidr": null,
	"tunnel1_inside_ipv6_cidr": null,
	"tunnel1_log_options": [{
		"cloudwatch_log_options": [{
			"log_enabled": true,
			"log_group_arn": "arn:aws:logs:us-east-1:123456789012:log-group:/aws/vpn/tunnel1",
			"log_output_format": "json"
		}]
	}],
	"tunnel1_phase1_dh_group_numbers": [],
	"tunnel1_phase1_encryption_algorithms": [],
	"tunnel1_phase1_integrity_algorithms": [],
	"tunnel1_phase1_lifetime_seconds": null,
	"tunnel1_phase2_dh_group_numbers": [],
	"tunnel1_phase2_encryption_algorithms": [],
	"tunnel1_phase2_integrity_algorithms": [],
	"tunnel1_phase2_lifetime_seconds": null,
	"tunnel1_preshared_key": null,
	"tunnel1_rekey_fuzz_percentage": null,
	"tunnel1_rekey_margin_time_seconds": null,
	"tunnel1_replay_window_size": null,
	"tunnel1_startup_action": null,
	"tunnel1_vgw_inside_address": null,
	"tunnel2_address": null,
	"tunnel2_bgp_asn": null,
	"tunnel2_bgp_holdtime": null,
	"tunnel2_cgw_inside_address": null,
	"tunnel2_dpd_timeout_action": null,
	"tunnel2_dpd_timeout_seconds": null,
	"tunnel2_enable_tunnel_lifecycle_control": null,
	"tunnel2_ike_versions": [],
	"tunnel2_inside_cidr": null,
	"tunnel2_inside_ipv6_cidr": null,
	"tunnel2_log_options": [{
		"cloudwatch_log_options": []
	}],
	"tunnel2_phase1_dh_group_numbers": [],
	"tunnel2_phase1_encryption_algorithms": [],
	"tunnel2_phase1_integrity_algorithms": [],
	"tunnel2_phase1_lifetime_seconds": null,
	"tunnel2_phase2_dh_group_numbers": [],
	"tunnel2_phase2_encryption_algorithms": [],
	"tunnel2_phase2_integrity_algorithms": [],
	"tunnel2_phase2_lifetime_seconds": null,
	"tunnel2_preshared_key": null,
	"tunnel2_rekey_fuzz_percentage": null,
	"tunnel2_rekey_margin_time_seconds": null,
	"tunnel2_replay_window_size": null,
	"tunnel2_startup_action": null,
	"tunnel2_vgw_inside_address": null,
	"tunnel_inside_ip_version": "ipv4",
	"type": "ipsec.1",
	"vgw_telemetry": [],
	"vpn_gateway_id": "vgw-1234abcd"
}`

func TestComputeInjectionState_NestedBlockDeltaRecovers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	prov, err := tfprovider.LoadProvider(ctx, nestedBlockTestAWSProviderAddr, nestedBlockTestAWSProviderVersion)
	if err != nil {
		skipOrFailUnavailable(t, err, "aws provider unavailable")
		return
	}
	defer prov.Close(ctx)

	pulumiProviders, err := PulumiProvidersForTerraformProviders(
		[]providermap.TerraformProviderName{nestedBlockTestAWSProviderAddr},
		map[string]string{nestedBlockTestAWSProviderAddr: nestedBlockTestAWSProviderVersion},
	)
	if err != nil {
		skipOrFailUnavailable(t, err, "could not bridge aws provider schema")
		return
	}
	pwm := pulumiProviders[providermap.TerraformProviderName(nestedBlockTestAWSProviderAddr)]
	require.NotNil(t, pwm, "expected a bridged provider for %s", nestedBlockTestAWSProviderAddr)

	shimResource := pwm.P.ResourcesMap().Get("aws_vpn_connection")
	require.NotNil(t, shimResource, "expected aws_vpn_connection in the bridged schema")
	schemaMap := shimResource.Schema()

	var schemaInfos map[string]*tfbridge.SchemaInfo
	if ri := pwm.Resources["aws_vpn_connection"]; ri != nil {
		schemaInfos = ri.Fields
	}

	attrs := []byte(vpnConnectionAttrsJSON)

	outputs, delta, deltaReason, _, err := ComputeInjectionState(
		ctx, prov, "aws_vpn_connection", attrs, schemaMap, schemaInfos)
	require.NoError(t, err)
	require.NotEmpty(t, delta, "a nested-block type should produce a non-empty delta")
	assert.Empty(t, deltaReason, "a produced delta must not also carry a reason it is missing")

	outputsJSON, err := json.Marshal(outputs)
	require.NoError(t, err)
	deltaJSON, err := json.Marshal(delta)
	require.NoError(t, err)

	var outputsFromSidecar map[string]interface{}
	require.NoError(t, json.Unmarshal(outputsJSON, &outputsFromSidecar))
	var deltaFromSidecar map[string]interface{}
	require.NoError(t, json.Unmarshal(deltaJSON, &deltaFromSidecar))

	props := resource.NewPropertyMapFromMap(outputsFromSidecar)
	rsd, err := tfbridge.UnmarshalRawStateDelta(resource.NewPropertyValue(deltaFromSidecar))
	require.NoError(t, err)
	recovered, err := rsd.Recover(resource.NewObjectProperty(props))
	require.NoError(t, err, "delta must apply cleanly to the outputs")

	var want, got interface{}
	require.NoError(t, json.Unmarshal(attrs, &want))
	require.NoError(t, json.Unmarshal(recovered, &got))
	assert.Equal(t, want, got, "recovered raw state must match the original attributes exactly")
}

func TestComputeInjectionState_TimeoutsDeltaRecovers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	prov, err := tfprovider.LoadProvider(ctx, nestedBlockTestAWSProviderAddr, nestedBlockTestAWSProviderVersion)
	if err != nil {
		skipOrFailUnavailable(t, err, "aws provider unavailable")
		return
	}
	defer prov.Close(ctx)

	pulumiProviders, err := PulumiProvidersForTerraformProviders(
		[]providermap.TerraformProviderName{nestedBlockTestAWSProviderAddr},
		map[string]string{nestedBlockTestAWSProviderAddr: nestedBlockTestAWSProviderVersion},
	)
	if err != nil {
		skipOrFailUnavailable(t, err, "could not bridge aws provider schema")
		return
	}
	pwm := pulumiProviders[providermap.TerraformProviderName(nestedBlockTestAWSProviderAddr)]
	require.NotNil(t, pwm, "expected a bridged provider for %s", nestedBlockTestAWSProviderAddr)

	schemaFor := func(tfType string) (shim.SchemaMap, map[string]*tfbridge.SchemaInfo) {
		shimResource := pwm.P.ResourcesMap().Get(tfType)
		require.NotNil(t, shimResource, "expected %s in the bridged schema", tfType)
		var schemaInfos map[string]*tfbridge.SchemaInfo
		if ri := pwm.Resources[tfType]; ri != nil {
			schemaInfos = ri.Fields
		}
		return shimResource.Schema(), schemaInfos
	}

	t.Run("aws_vpn_gateway_route_propagation: schema declares timeouts, delta still produced and recovers", func(t *testing.T) {
		schemaMap, schemaInfos := schemaFor("aws_vpn_gateway_route_propagation")

		schemas := prov.GetProviderSchema(ctx)
		sch, ok := schemas.ResourceTypes["aws_vpn_gateway_route_propagation"]
		require.True(t, ok)
		_, hasTimeouts := sch.Block.ImpliedType().AttributeTypes()["timeouts"]
		require.True(t, hasTimeouts,
			"expected aws_vpn_gateway_route_propagation's schema to declare a timeouts block")

		attrs := []byte(`{
			"id": "vgw-0cdee3deb918b1983_rtb-0e370d1fdde0890b3",
			"route_table_id": "rtb-0e370d1fdde0890b3",
			"vpn_gateway_id": "vgw-0cdee3deb918b1983"
		}`)

		outputs, delta, deltaReason, version, err := ComputeInjectionState(
			ctx, prov, "aws_vpn_gateway_route_propagation", attrs, schemaMap, schemaInfos)

		require.NoError(t, err)
		assert.Equal(t, "rtb-0e370d1fdde0890b3", outputs["routeTableId"])
		assert.GreaterOrEqual(t, version, int64(0))

		require.NotNil(t, delta, "a timeouts-only-mismatch type should now produce a delta")
		assert.Empty(t, deltaReason, "a produced delta must not also carry a reason it is missing")

		outputsJSON, err := json.Marshal(outputs)
		require.NoError(t, err)
		deltaJSON, err := json.Marshal(delta)
		require.NoError(t, err)

		var outputsFromSidecar map[string]interface{}
		require.NoError(t, json.Unmarshal(outputsJSON, &outputsFromSidecar))
		var deltaFromSidecar map[string]interface{}
		require.NoError(t, json.Unmarshal(deltaJSON, &deltaFromSidecar))

		props := resource.NewPropertyMapFromMap(outputsFromSidecar)
		rsd, err := tfbridge.UnmarshalRawStateDelta(resource.NewPropertyValue(deltaFromSidecar))
		require.NoError(t, err)
		recovered, err := rsd.Recover(resource.NewObjectProperty(props))
		require.NoError(t, err, "delta must apply cleanly to the outputs")

		var want, got interface{}
		require.NoError(t, json.Unmarshal(attrs, &want))
		require.NoError(t, json.Unmarshal(recovered, &got))
		assert.Equal(t, want, got, "recovered raw state must match the original attributes exactly")
	})

	t.Run("aws_vpn_gateway_route_propagation: populated timeouts, still excluded from outputs, delta recovers", func(t *testing.T) {
		schemaMap, schemaInfos := schemaFor("aws_vpn_gateway_route_propagation")

		attrs := []byte(`{
			"id": "vgw-0cdee3deb918b1983_rtb-0e370d1fdde0890b3",
			"route_table_id": "rtb-0e370d1fdde0890b3",
			"vpn_gateway_id": "vgw-0cdee3deb918b1983",
			"timeouts": {"create": "10m", "delete": "5m"}
		}`)

		outputs, delta, deltaReason, _, err := ComputeInjectionState(
			ctx, prov, "aws_vpn_gateway_route_propagation", attrs, schemaMap, schemaInfos)

		require.NoError(t, err)
		assert.Empty(t, deltaReason, "a produced delta must not also carry a reason it is missing")
		require.NotNil(t, delta, "a resource with populated timeouts should still produce a delta")

		_, hasTimeouts := outputs["timeouts"]
		assert.False(t, hasTimeouts, "timeouts must not appear in the injected Pulumi outputs")

		outputsJSON, err := json.Marshal(outputs)
		require.NoError(t, err)
		deltaJSON, err := json.Marshal(delta)
		require.NoError(t, err)

		var outputsFromSidecar map[string]interface{}
		require.NoError(t, json.Unmarshal(outputsJSON, &outputsFromSidecar))
		var deltaFromSidecar map[string]interface{}
		require.NoError(t, json.Unmarshal(deltaJSON, &deltaFromSidecar))

		props := resource.NewPropertyMapFromMap(outputsFromSidecar)
		rsd, err := tfbridge.UnmarshalRawStateDelta(resource.NewPropertyValue(deltaFromSidecar))
		require.NoError(t, err)
		recovered, err := rsd.Recover(resource.NewObjectProperty(props))
		require.NoError(t, err, "delta must apply cleanly to the outputs")

		var want, got map[string]interface{}
		require.NoError(t, json.Unmarshal(attrs, &want))
		delete(want, "timeouts")
		require.NoError(t, json.Unmarshal(recovered, &got))
		assert.Equal(t, want, got, "recovered raw state must match the original attributes minus timeouts")
	})

	t.Run("aws_vpn_connection_route: no timeouts block, delta available", func(t *testing.T) {
		schemaMap, schemaInfos := schemaFor("aws_vpn_connection_route")

		schemas := prov.GetProviderSchema(ctx)
		sch, ok := schemas.ResourceTypes["aws_vpn_connection_route"]
		require.True(t, ok)
		_, hasTimeouts := sch.Block.ImpliedType().AttributeTypes()["timeouts"]
		require.False(t, hasTimeouts,
			"expected aws_vpn_connection_route's schema to declare no timeouts block")

		attrs := []byte(`{
			"destination_cidr_block": "10.99.0.0/16",
			"id": "cgw-example:vgw-example",
			"vpn_connection_id": "vpn-0abc123def456"
		}`)

		outputs, delta, deltaReason, _, err := ComputeInjectionState(
			ctx, prov, "aws_vpn_connection_route", attrs, schemaMap, schemaInfos)

		require.NoError(t, err)
		assert.Equal(t, "10.99.0.0/16", outputs["destinationCidrBlock"])
		assert.NotNil(t, delta, "with no timeouts block in the schema, a delta should be produced")
		assert.Empty(t, deltaReason)
	})
}

func TestDeltaMarshalPreservesReplaceNodes(t *testing.T) {
	t.Parallel()

	const canonical = `{"obj":{"ps":{"policy":{"replace":{"raw":"{\"a\":1}"}}}}}`

	var doc interface{}
	require.NoError(t, json.Unmarshal([]byte(canonical), &doc))
	delta, err := tfbridge.UnmarshalRawStateDelta(resource.NewPropertyValue(doc))
	require.NoError(t, err)

	written, err := json.Marshal(delta)
	require.NoError(t, err)
	assert.JSONEq(t, canonical, string(written),
		"the delta must serialize to its own JSON form, not to PropertyValue internals")

	var back interface{}
	require.NoError(t, json.Unmarshal(written, &back))
	roundTripped, err := tfbridge.UnmarshalRawStateDelta(resource.NewPropertyValue(back))
	require.NoError(t, err)
	again, err := json.Marshal(roundTripped)
	require.NoError(t, err)
	assert.JSONEq(t, canonical, string(again), "the Replace node must not be silently dropped")

	lossy, err := json.Marshal(delta.Marshal().Mappable())
	require.NoError(t, err)
	assert.NotEqual(t, canonical, string(lossy),
		"guard on the premise: Mappable() is expected to differ, so this test is not vacuous")
}

func TestComputeInjectionState_DeltaServesAProviderStateUpgrade(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	prov, err := tfprovider.LoadProvider(ctx, nestedBlockTestAWSProviderAddr, nestedBlockTestAWSProviderVersion)
	if err != nil {
		skipOrFailUnavailable(t, err, "aws provider unavailable")
		return
	}
	defer prov.Close(ctx)

	const tfType = "aws_ssm_patch_group"

	sch, ok := prov.GetProviderSchema(ctx).ResourceTypes[tfType]
	require.True(t, ok, "provider has no schema for %s", tfType)
	require.EqualValues(t, 1, sch.Version,
		"this test is pointless if %s stops carrying a schema version", tfType)

	pulumiProviders, err := PulumiProvidersForTerraformProviders(
		[]providermap.TerraformProviderName{nestedBlockTestAWSProviderAddr},
		map[string]string{nestedBlockTestAWSProviderAddr: nestedBlockTestAWSProviderVersion},
	)
	if err != nil {
		skipOrFailUnavailable(t, err, "could not bridge aws provider schema")
		return
	}
	pwm := pulumiProviders[providermap.TerraformProviderName(nestedBlockTestAWSProviderAddr)]
	require.NotNil(t, pwm)
	shimResource := pwm.P.ResourcesMap().Get(tfType)
	require.NotNil(t, shimResource, "expected %s in the bridged schema", tfType)
	schemaMap := shimResource.Schema()
	var schemaInfos map[string]*tfbridge.SchemaInfo
	if ri := pwm.Resources[tfType]; ri != nil {
		schemaInfos = ri.Fields
	}

	attrs := []byte(`{"id":"patch-group-1,pb-0123456789abcdef0",` +
		`"baseline_id":"pb-0123456789abcdef0","patch_group":"patch-group-1"}`)

	outputs, delta, deltaReason, version, err := ComputeInjectionState(
		ctx, prov, tfType, attrs, schemaMap, schemaInfos)
	require.NoError(t, err)
	require.NotEmpty(t, delta, "no delta means nothing to upgrade from")
	assert.Empty(t, deltaReason)
	require.EqualValues(t, 1, version,
		"the recorded schema version is what tells a later bridge which upgraders to run")

	outputsJSON, err := json.Marshal(outputs)
	require.NoError(t, err)
	deltaJSON, err := json.Marshal(delta)
	require.NoError(t, err)
	outputsFromSidecar := decodeExact(t, outputsJSON)
	deltaFromSidecar := decodeExact(t, deltaJSON)

	rsd, err := tfbridge.UnmarshalRawStateDelta(resource.NewPropertyValue(deltaFromSidecar))
	require.NoError(t, err)
	recovered, err := rsd.Recover(
		resource.NewObjectProperty(resource.NewPropertyMapFromMap(outputsFromSidecar)))
	require.NoError(t, err, "the delta must apply to its own outputs")

	resp := prov.UpgradeResourceState(ctx, providers.UpgradeResourceStateRequest{
		TypeName:     tfType,
		Version:      version,
		RawStateJSON: recovered,
	})
	require.False(t, resp.Diagnostics.HasErrors(),
		"the provider rejected raw state reconstructed from our delta: %v", resp.Diagnostics.Err())
	require.False(t, resp.UpgradedState.IsNull(), "upgrade produced a null state")

	upgraded := resp.UpgradedState.AsValueMap()
	assert.Equal(t, "pb-0123456789abcdef0", upgraded["baseline_id"].AsString())
	assert.Equal(t, "patch-group-1", upgraded["patch_group"].AsString())

	t.Run("an undeclared attribute is tolerated", func(t *testing.T) {
		var withExtra map[string]interface{}
		require.NoError(t, json.Unmarshal(recovered, &withExtra))
		withExtra["region"] = "us-west-2"
		extraJSON, err := json.Marshal(withExtra)
		require.NoError(t, err)

		resp := prov.UpgradeResourceState(ctx, providers.UpgradeResourceStateRequest{
			TypeName:     tfType,
			Version:      version,
			RawStateJSON: extraJSON,
		})
		assert.False(t, resp.Diagnostics.HasErrors(),
			"a provider that rejects undeclared attributes would make fillOutputsFromInputs "+
				"corrupt raw state on every operation: %v", resp.Diagnostics.Err())
		assert.False(t, resp.UpgradedState.IsNull())
	})

	t.Run("a corrupt delta does not survive", func(t *testing.T) {
		corrupt := map[string]interface{}{"obj": map[string]interface{}{
			"ps": map[string]interface{}{"baseline_id": map[string]interface{}{"nope": map[string]interface{}{}}},
		}}
		badRSD, err := tfbridge.UnmarshalRawStateDelta(resource.NewPropertyValue(corrupt))
		require.NoError(t, err)
		badRecovered, recoverErr := badRSD.Recover(
			resource.NewObjectProperty(resource.NewPropertyMapFromMap(outputsFromSidecar)))
		if recoverErr != nil {
			return // rejected before the provider ever saw it, which is fine
		}
		assert.NotEqual(t, string(recovered), string(badRecovered),
			"a corrupt delta must not reconstruct the same raw state as a correct one")
	})
}

func TestCorruptDeltaPayloadIsGenuinelyRejected(t *testing.T) {
	t.Parallel()

	const payload = `{"obj":{"ps":{"corrupted":{"asset":` +
		`{"kind":1,"archiveFormat":"definitely-not-a-number"}}}}}`

	var doc interface{}
	require.NoError(t, json.Unmarshal([]byte(payload), &doc),
		"the payload must be valid JSON, or it would be rejected before reaching the bridge")

	_, err := tfbridge.UnmarshalRawStateDelta(resource.NewPropertyValue(doc))
	require.Error(t, err,
		"the e2e corruption payload is no longer rejected by UnmarshalRawStateDelta — "+
			"CorruptDeltaFailsPreview would now report a false alarm")
	assert.Contains(t, err.Error(), "archiveFormat",
		"the rejection should still be the archiveFormat type mismatch this payload targets")

	valid := `{"obj":{"ps":{"corrupted":{"asset":{"kind":1,"archiveFormat":2}}}}}`
	var validDoc interface{}
	require.NoError(t, json.Unmarshal([]byte(valid), &validDoc))
	_, err = tfbridge.UnmarshalRawStateDelta(resource.NewPropertyValue(validDoc))
	assert.NoError(t, err, "a well-formed asset delta must still parse")
}

func TestComputeInjectionState_LargeIntegerExactInFilesRoundsThroughPropertyValue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	prov, err := tfprovider.LoadProvider(ctx, nestedBlockTestAWSProviderAddr, nestedBlockTestAWSProviderVersion)
	if err != nil {
		skipOrFailUnavailable(t, err, "aws provider unavailable")
		return
	}
	defer prov.Close(ctx)

	schemaMap, schemaInfos, ok := bridgedSchemaFor(t, "aws_sqs_queue")
	if !ok {
		return
	}

	const exact = "9007199254740993"
	attrs := []byte(`{"id":"q","name":"q","delay_seconds":` + exact + `,"fifo_queue":false}`)

	outputs, delta, reason, _, err := ComputeInjectionState(
		ctx, prov, "aws_sqs_queue", attrs, schemaMap, schemaInfos)
	require.NoError(t, err)
	require.Empty(t, reason)
	require.NotEmpty(t, delta)

	// restoreLargeIntegers repairs the float64 rounding the bridge conversion
	// imposes (#29), so the sidecar carries the exact digits as a number.
	repaired, ok := outputs["delaySeconds"].(json.Number)
	require.True(t, ok, "a repaired output leaf must be a json.Number, got %T", outputs["delaySeconds"])
	assert.Equal(t, exact, repaired.String())

	deltaJSON, err := json.Marshal(delta)
	require.NoError(t, err)
	assert.Contains(t, string(deltaJSON), `"replace"`,
		"a value the bridge cannot reproduce naturally should produce a Replace node")
	assert.Contains(t, string(deltaJSON), exact,
		"the sidecar delta carries the exact digits too (computed from the cty value)")

	// Recovery through the bridge, converted exactly the way production's
	// validateRecover does (propertyValueFromState + deltaPropertyValue —
	// injection stores the delta inside outputs under rawStateDeltaKey).
	// This is where exactness ENDS: resource.PropertyValue holds numbers as
	// float64, so the value rounds at this boundary regardless of what the
	// files carry. That ceiling is bridge-side (#29); if this assertion ever
	// reports the exact digits, the ceiling is gone — update #29 and the docs.
	outputsFromSidecar := decodeExact(t, mustJSON(t, outputs))
	outputsFromSidecar[rawStateDeltaKey] = decodeExact(t, deltaJSON)
	rsd, err := tfbridge.UnmarshalRawStateDelta(
		deltaPropertyValue(outputsFromSidecar[rawStateDeltaKey].(map[string]interface{})))
	require.NoError(t, err)
	recovered, err := rsd.Recover(propertyValueFromState(outputsFromSidecar))
	require.NoError(t, err)

	const rounded = "9007199254740992"
	assert.Equal(t, json.Number(rounded), decodeExact(t, recovered)["delay_seconds"])
}

func mustJSON(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func TestComputeInjectionState_PopulatedMapRoundTrips(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	prov, err := tfprovider.LoadProvider(ctx, nestedBlockTestAWSProviderAddr, nestedBlockTestAWSProviderVersion)
	if err != nil {
		skipOrFailUnavailable(t, err, "aws provider unavailable")
		return
	}
	defer prov.Close(ctx)

	schemaMap, schemaInfos, ok := bridgedSchemaFor(t, "aws_cloudwatch_log_group")
	if !ok {
		return
	}

	attrs := []byte(`{"id":"lg","name":"lg","name_prefix":null,"retention_in_days":14,` +
		`"kms_key_id":null,"skip_destroy":false,"log_group_class":null,` +
		`"tags":{"Env":"prod","Team":"ce"},"tags_all":{"Env":"prod","Team":"ce"},` +
		`"arn":"arn:aws:logs:us-west-2:123456789012:log-group:lg"}`)

	outputs, delta, reason, _, err := ComputeInjectionState(
		ctx, prov, "aws_cloudwatch_log_group", attrs, schemaMap, schemaInfos)
	require.NoError(t, err)
	require.Empty(t, reason)

	deltaJSON, err := json.Marshal(delta)
	require.NoError(t, err)
	assert.Contains(t, string(deltaJSON), `"map"`,
		"a populated map attribute should produce a map delta node")

	assertDeltaRecoversExactly(t, attrs, outputs, delta)
}

func bridgedSchemaFor(t *testing.T, tfType string) (shim.SchemaMap, map[string]*tfbridge.SchemaInfo, bool) {
	t.Helper()
	pulumiProviders, err := PulumiProvidersForTerraformProviders(
		[]providermap.TerraformProviderName{nestedBlockTestAWSProviderAddr},
		map[string]string{nestedBlockTestAWSProviderAddr: nestedBlockTestAWSProviderVersion},
	)
	if err != nil {
		skipOrFailUnavailable(t, err, "could not bridge aws provider schema")
		return nil, nil, false
	}
	pwm := pulumiProviders[providermap.TerraformProviderName(nestedBlockTestAWSProviderAddr)]
	require.NotNil(t, pwm)
	shimResource := pwm.P.ResourcesMap().Get(tfType)
	require.NotNil(t, shimResource, "expected %s in the bridged schema", tfType)
	var schemaInfos map[string]*tfbridge.SchemaInfo
	if ri := pwm.Resources[tfType]; ri != nil {
		schemaInfos = ri.Fields
	}
	return shimResource.Schema(), schemaInfos, true
}

func assertDeltaRecoversExactly(
	t *testing.T, originalAttrs []byte, outputs, delta map[string]interface{},
) {
	t.Helper()

	outputsJSON, err := json.Marshal(outputs)
	require.NoError(t, err)
	deltaJSON, err := json.Marshal(delta)
	require.NoError(t, err)

	var outputsFromSidecar, deltaFromSidecar map[string]interface{}
	require.NoError(t, json.Unmarshal(outputsJSON, &outputsFromSidecar))
	require.NoError(t, json.Unmarshal(deltaJSON, &deltaFromSidecar))

	rsd, err := tfbridge.UnmarshalRawStateDelta(deltaPropertyValue(deltaFromSidecar))
	require.NoError(t, err)
	recovered, err := rsd.Recover(propertyValueFromState(outputsFromSidecar))
	require.NoError(t, err, "the delta must apply cleanly to its own outputs")

	want := decodeExact(t, originalAttrs)
	got := decodeExact(t, recovered)

	for k, w := range want {
		g, present := got[k]
		require.True(t, present, "recovered raw state is missing %q", k)
		assert.Equal(t, w, g, "recovered raw state differs from the original at %q", k)
	}
	for k, g := range got {
		if _, inOriginal := want[k]; inOriginal {
			continue
		}
		assert.Nil(t, g, "recovered raw state invented a non-null value at %q that the original did not have", k)
	}
}

func decodeExact(t *testing.T, data []byte) map[string]interface{} {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var out map[string]interface{}
	require.NoError(t, dec.Decode(&out))
	return out
}
