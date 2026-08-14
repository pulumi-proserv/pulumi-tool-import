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
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg/providermap"
	"github.com/pulumi-proserv/pulumi-tool-import/pkg/tfprovider"
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge"
	shim "github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfshim"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// skipOrFailUnavailable reports a provider-unavailable condition for
// TestComputeInjectionState_NestedBlockDeltaRecovers. It skips on a
// developer machine, where the aws 5.100.0 provider cache is best-effort
// (see the comment on nestedBlockTestAWSProviderAddr — it depends on a
// prior, unrelated e2e run having populated the local plugin cache), but
// fails outright in CI: a cold CI cache silently skipping this test would
// yield a green build having proved nothing about the riskiest computation
// on this branch (nested-block raw state delta recovery). GitHub Actions
// (and most other CI systems) set CI=true unconditionally, so this needs no
// workflow change to take effect.
//
// Warming this provider unconditionally in TestMain, the way
// pkg/importsupport warms "random", was considered and rejected: unlike
// random, the aws provider is a large network fetch, and forcing every run
// of this package's tests — most of which have nothing to do with AWS or
// nested blocks — to download and cache it would slow down and could break
// sandboxed or offline test runs that never exercise this path.
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

	// LoadProvider requires an exact version — it calls getproviders.ParseVersion,
	// which rejects "". These are the constants pkg/importsupport's tests use, and
	// TestMain there already warms this provider into the plugin cache.
	prov, err := tfprovider.LoadProvider(ctx, "registry.terraform.io/hashicorp/random", "3.7.2")
	require.NoError(t, err)
	defer prov.Close(ctx)

	attrs := []byte(`{"id":"cool-pet","keepers":null,"length":2,"prefix":null,"separator":"-"}`)

	outputs, delta, deltaReason, version, err := ComputeInjectionState(ctx, prov, "random_pet", attrs, nil, nil)
	require.NoError(t, err)

	// Outputs carry Pulumi property names.
	assert.Equal(t, "cool-pet", outputs["id"])
	assert.Equal(t, "-", outputs["separator"])

	// The schema version comes from the provider, not from a mock that reports 0.
	assert.GreaterOrEqual(t, version, int64(0))

	// A delta is produced and is not a secret-bearing blob.
	assert.NotNil(t, delta)
	assert.Empty(t, deltaReason, "a produced delta must not also carry a reason it is missing")
}

// nestedBlockTestAWSProviderAddr and nestedBlockTestAWSProviderVersion pin the
// provider and version used by TestComputeInjectionState_NestedBlockDeltaRecovers.
//
// The brief for this test specified "registry.terraform.io/hashicorp/aws" at
// "7.24.0", but that version does not exist in the Terraform registry (the aws
// provider's newest major is 6.x as of this writing) — LoadProvider fails
// ParseVersion/PackageMeta for it outright. This uses 5.100.0 instead, which
// was already present in this machine's provider cache from prior AWS e2e
// testing (see the "AWS end-to-end testing" memory doc) and still declares
// aws_vpn_connection's tunnel1_log_options/tunnel2_log_options as nested
// (MaxItems=1) blocks with a further-nested cloudwatch_log_options block,
// giving the two levels of MaxItems=1 pluralization this test needs to
// exercise.
const (
	nestedBlockTestAWSProviderAddr    = "registry.opentofu.org/hashicorp/aws"
	nestedBlockTestAWSProviderVersion = "5.100.0"
)

// vpnConnectionAttrsJSON is a full set of aws_vpn_connection attributes
// (every attribute the real 5.100.0 schema declares, most left null) with
// tunnel1_log_options populated one level deep (cloudwatch_log_options) and
// tunnel2_log_options populated with an empty nested block list, generated
// and round-tripped against the live schema's cty.Type via ctyjson.Unmarshal
// before being pasted in here.
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

	// schemaMap/schemaInfos come from the same Pulumi-bridged mock schema
	// "digest tf" already has for every provider (pkg/pulumi_providers.go),
	// exactly as the module_map.go integration will use them. A nil schemaMap
	// works for flat types (see TestComputeInjectionState_RandomPet) but
	// panics partway into a nested-block delta, because
	// tfbridge.RawStateComputeDelta must look up each nested attribute's
	// Pulumi name from the schema to match it against the outputs.
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

	// PulumiOutputs and RawStateDelta are written to the sidecar as JSON and
	// read back by a later task — that JSON round trip, not the in-memory
	// value, is the real contract between this task and the next. Numbers,
	// nulls and nested maps must all survive it and still satisfy Recover.
	outputsJSON, err := json.Marshal(outputs)
	require.NoError(t, err)
	deltaJSON, err := json.Marshal(delta)
	require.NoError(t, err)

	var outputsFromSidecar map[string]interface{}
	require.NoError(t, json.Unmarshal(outputsJSON, &outputsFromSidecar))
	var deltaFromSidecar map[string]interface{}
	require.NoError(t, json.Unmarshal(deltaJSON, &deltaFromSidecar))

	// The delta must recover the exact attributes it was computed from, not
	// merely apply without an error: compare the recovered raw state against
	// the original attribute JSON.
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

// TestComputeInjectionState_TimeoutsOnlyTypeHasNoDelta reproduces, without
// AWS, why aws_vpn_gateway_route_propagation gets no raw state delta while
// aws_vpn_connection_route — structurally similar, also a flat pair of string
// attributes — does.
//
// aws_vpn_gateway_route_propagation's live schema (confirmed via
// sch.Block.ImpliedType() against the same 5.100.0 provider as
// TestComputeInjectionState_NestedBlockDeltaRecovers) declares a "timeouts"
// block (create/delete), even though the resource itself never sets one.
// aws_vpn_connection_route's schema has no such block.
//
// The bridge's RawStateComputeDelta has an unconditional early return for any
// path exactly equal to the top-level "timeouts" attribute: it always
// produces a non-empty, Object-shaped delta node for it, regardless of the
// actual Pulumi property value there. When that property is present but null
// — which it is here, since MakeTerraformOutputs converts the schema's
// always-present, never-set "timeouts" attribute into an explicit null output
// — the delta's own turnaround self-check (Recover applied to Marshal, before
// RawStateComputeDelta returns) fails: it demands an Object PropertyValue at
// "timeouts" and gets Null. That failure is what makes RawStateComputeDelta
// return an error for this type, which is what ComputeInjectionState reports
// as deltaUnavailableReason.
//
// This is why the resource genuinely never reaches the "empty but present
// delta" case that pkg/module_map.go's RawStateDelta doc comment argues is
// impossible: RawStateComputeDelta either succeeds with a non-empty delta or
// fails outright before returning one.
func TestComputeInjectionState_TimeoutsOnlyTypeHasNoDelta(t *testing.T) {
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

	t.Run("aws_vpn_gateway_route_propagation: schema declares timeouts, delta unavailable", func(t *testing.T) {
		schemaMap, schemaInfos := schemaFor("aws_vpn_gateway_route_propagation")

		// Confirm the root cause directly against the live schema: a
		// "timeouts" attribute is present in the implied type even though
		// this resource never sets one.
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

		// Not fatal: outputs and a schema version are still produced, so the
		// resource can still be injected without a delta.
		require.NoError(t, err)
		assert.Equal(t, "rtb-0e370d1fdde0890b3", outputs["routeTableId"])
		assert.GreaterOrEqual(t, version, int64(0))

		// The delta itself is unavailable, and the reason is no longer
		// discarded: it names the turnaround-check failure caused by the
		// "timeouts" mismatch described above.
		assert.Nil(t, delta)
		assert.Contains(t, deltaReason, "aws_vpn_gateway_route_propagation")
		assert.Contains(t, deltaReason, "turnaround check")
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
