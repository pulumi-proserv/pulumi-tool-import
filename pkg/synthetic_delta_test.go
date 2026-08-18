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
	"testing"

	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge"
	shim "github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfshim"
	shimschema "github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfshim/schema"
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/vendored/opentofu/configs/configschema"
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/vendored/opentofu/providers"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

// This file tests delta computation against SYNTHETIC schemas rather than a
// downloaded provider.
//
// The tool is not AWS-only — Azure, GCP and Kubernetes are planned — so
// coverage has to be driven by what the delta format can express, not by what
// one provider happens to exercise. Measuring terraform-provider-aws 5.100.0
// and concluding a case was unreachable is how an earlier draft of the coverage
// plan went wrong: AWS has ZERO DynamicPseudoType attributes across all 1526
// resource types, while kubernetes_manifest is dynamic by design.
//
// ComputeInjectionState needs exactly one thing from a provider —
// GetProviderSchema — so a fake that embeds providers.Interface and overrides
// that single method unlocks arbitrary shapes with no provider binary, no
// network and no cloud. This is also how the bridge tests its own delta code.

// fakeProvider serves one hand-built schema. Every other method comes from the
// embedded nil interface and panics if called, which is the point: if
// ComputeInjectionState ever starts needing more of a provider than its schema,
// these tests should fail loudly rather than quietly exercising a stub.
type fakeProvider struct {
	providers.Interface
	schema providers.GetProviderSchemaResponse
}

func (f *fakeProvider) GetProviderSchema(context.Context) providers.GetProviderSchemaResponse {
	return f.schema
}
func (f *fakeProvider) Name() string    { return "synthetic" }
func (f *fakeProvider) Version() string { return "0.0.0" }

// syntheticResource builds a provider serving one resource type, from a cty
// attribute-type map, plus the matching Pulumi-side shim schema.
func syntheticResource(
	tfType string, version int64, attrs map[string]*configschema.Attribute, shimAttrs shimschema.SchemaMap,
) (*fakeProvider, shim.SchemaMap) {
	return &fakeProvider{
		schema: providers.GetProviderSchemaResponse{
			ResourceTypes: map[string]providers.Schema{
				tfType: {Version: version, Block: &configschema.Block{Attributes: attrs}},
			},
		},
	}, shimAttrs
}

// TestSyntheticDelta_DynamicTypeProducesReplaceNode covers the delta path that
// AWS cannot reach at all and that Kubernetes will exercise constantly.
//
// rawstate.go:669 emits a Replace node when the schema type at a path is
// dynamic, because nothing about the Pulumi value records what the Terraform
// type was — the raw value has to be carried verbatim. kubernetes_manifest is
// built on exactly this, so the case moves from "unreachable" to "the common
// case" the moment that provider is supported.
//
// It matters beyond mere coverage: a Replace node carries the provider's
// verbatim raw state, is the node the bridge marks sensitive, and is the node
// whose marshalling was silently corrupted until today (a37fd8c).
func TestSyntheticDelta_DynamicTypeProducesReplaceNode(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	prov, schemaMap := syntheticResource("synthetic_dynamic", 0,
		map[string]*configschema.Attribute{
			"id":       {Type: cty.String, Computed: true},
			"manifest": {Type: cty.DynamicPseudoType, Optional: true},
		},
		shimschema.SchemaMap{
			"id":       (&shimschema.Schema{Type: shim.TypeString, Computed: true}).Shim(),
			"manifest": (&shimschema.Schema{Type: shim.TypeString, Optional: true}).Shim(),
		})

	// cty serializes a DynamicPseudoType value in a wrapped {"value","type"}
	// form, and that is what real Terraform state holds for a dynamic
	// attribute — carrying the type alongside the value is the whole point,
	// since the schema does not pin it. That wrapping is also why the bridge
	// has to emit a Replace node: nothing in the Pulumi property map records
	// the Terraform type, so the raw value must travel verbatim.
	attrs := []byte(`{"id":"obj-1","manifest":{"value":{"kind":"ConfigMap","metadata":{"name":"cm"}},` +
		`"type":["object",{"kind":"string","metadata":["object",{"name":"string"}]}]}}`)

	outputs, delta, reason, _, err := ComputeInjectionState(
		ctx, prov, "synthetic_dynamic", attrs, schemaMap, nil)
	require.NoError(t, err)
	require.Empty(t, reason, "a dynamic attribute must not defeat delta computation")
	require.NotEmpty(t, delta)

	deltaJSON, err := json.Marshal(delta)
	require.NoError(t, err)
	assert.Contains(t, string(deltaJSON), `"replace"`,
		"a dynamic-typed attribute should be carried verbatim in a Replace node")

	// And the verbatim payload must survive the sidecar round trip and recover
	// to exactly what Terraform had.
	assertDeltaRecoversExactly(t, attrs, outputs, delta)
}

// TestSyntheticDelta_DynamicReplaceNodeIsEnveloped ties the dynamic case to the
// secret handling added today. A Replace node carries real provider values, the
// bridge marks such nodes secret when IT writes them, and injection must do the
// same — otherwise the identical payload is encrypted via one path and
// plaintext via the other. For a dynamic attribute the payload is the whole
// value, which makes this the case where it matters most.
func TestSyntheticDelta_DynamicReplaceNodeIsEnveloped(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	prov, schemaMap := syntheticResource("synthetic_dynamic", 0,
		map[string]*configschema.Attribute{
			"id":       {Type: cty.String, Computed: true},
			"manifest": {Type: cty.DynamicPseudoType, Optional: true},
		},
		shimschema.SchemaMap{
			"id":       (&shimschema.Schema{Type: shim.TypeString, Computed: true}).Shim(),
			"manifest": (&shimschema.Schema{Type: shim.TypeString, Optional: true}).Shim(),
		})

	attrs := []byte(`{"id":"obj-1","manifest":{"value":{"token":"s3cret-value"},` +
		`"type":["object",{"token":"string"}]}}`)
	_, delta, _, _, err := ComputeInjectionState(ctx, prov, "synthetic_dynamic", attrs, schemaMap, nil)
	require.NoError(t, err)
	require.NotEmpty(t, delta)

	enveloped := envelopeReplaceNodes(delta)
	envelopedJSON, err := json.Marshal(enveloped)
	require.NoError(t, err)
	assert.Contains(t, string(envelopedJSON), secretSig,
		"a dynamic Replace node carries real values and must be enveloped")

	// It must still read back as the same delta: UnmarshalRawStateDelta strips
	// secrets before decoding, so enveloping cannot change what the bridge sees.
	before, err := json.Marshal(delta)
	require.NoError(t, err)
	var doc interface{}
	require.NoError(t, json.Unmarshal(envelopedJSON, &doc))
	recovered, err := tfbridge.UnmarshalRawStateDelta(propertyValueFromState(doc))
	require.NoError(t, err)
	after, err := json.Marshal(recovered)
	require.NoError(t, err)
	assert.JSONEq(t, string(before), string(after),
		"enveloping must not change what the bridge reads back")
}

// TestSyntheticDelta_RedactedPlaceholderIsScreenedOut covers the guard that
// stops a redacted secret riding into state inside a delta.
//
// redactSensitivePaths replaces a sensitive attribute with "(sensitive)" before
// the delta is computed, so a delta computed over redacted attributes can embed
// that literal — and for a dynamic attribute it embeds the whole value
// verbatim, placeholder included. attachRawStateDelta screens for it
// unconditionally rather than only when RedactedAttributes is non-empty,
// because a nested path the digest never recorded can carry one too.
func TestSyntheticDelta_RedactedPlaceholderIsScreenedOut(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	prov, schemaMap := syntheticResource("synthetic_dynamic", 0,
		map[string]*configschema.Attribute{
			"id":       {Type: cty.String, Computed: true},
			"manifest": {Type: cty.DynamicPseudoType, Optional: true},
		},
		shimschema.SchemaMap{
			"id":       (&shimschema.Schema{Type: shim.TypeString, Computed: true}).Shim(),
			"manifest": (&shimschema.Schema{Type: shim.TypeString, Optional: true}).Shim(),
		})

	// A redacted value, as redactSensitivePaths would leave it.
	attrs := []byte(`{"id":"obj-1","manifest":{"value":{"token":"` + redactedPlaceholder + `"},` +
		`"type":["object",{"token":"string"}]}}`)
	outputs, delta, _, _, err := ComputeInjectionState(ctx, prov, "synthetic_dynamic", attrs, schemaMap, nil)
	require.NoError(t, err)
	require.NotEmpty(t, delta)

	// Guard on the premise: the placeholder really is inside the delta, or the
	// screen below would be tested against nothing.
	deltaJSON, err := json.Marshal(delta)
	require.NoError(t, err)
	require.Contains(t, string(deltaJSON), redactedPlaceholder,
		"a delta over redacted attributes should embed the placeholder — otherwise this test is vacuous")

	r := &NonImportableResource{
		Type: "k8s:index:Manifest", Name: "m",
		TerraformAddress: "synthetic_dynamic.m",
		RawStateDelta:    delta,
	}
	injectedOutputs := map[string]interface{}{}
	for k, v := range outputs {
		injectedOutputs[k] = v
	}

	outcome, note := attachRawStateDelta(r, map[string]interface{}{"urn": "urn:test"}, injectedOutputs)
	assert.Equal(t, deltaDroppedSensitive, outcome,
		"a delta embedding the redaction placeholder must be dropped, not injected")
	assert.Contains(t, note, "unresolvable")
	assert.NotContains(t, injectedOutputs, rawStateDeltaKey,
		"the dropped delta must not remain in the outputs")
}

// TestSyntheticDelta_PulumiOnlyPropertyPassesThroughRawState pins what
// actually happens to a property that exists on the Pulumi side but not in
// Terraform — "region" being the live example, per-resource in the Pulumi AWS
// provider but provider-level config in terraform-provider-aws. This is the
// mechanism #30 was filed about.
//
// It is NOT dropped, which is what both the issue and this test's first draft
// assumed. objDelta.Ignored records keys that had no Terraform counterpart WHEN
// THE DELTA WAS COMPUTED; a property added afterwards by fillOutputsFromInputs
// was not there to be recorded, so it is missing from PropertyDeltas entirely
// and recovers "naturally" — meaning it is written straight into the
// reconstructed raw state.
//
// That is harmless, but for a reason worth stating rather than assuming: the
// provider ignores attributes its schema does not declare. Measured against
// terraform-provider-aws 5.100.0 and asserted in
// TestComputeInjectionState_DeltaServesAProviderStateUpgrade, where a real
// provider is available.
//
// So #30's first comment reached the right conclusion — filling outputs
// afterwards does not break Recover — by a different route than it described.
// Pinned here because "an extra attribute appears in Terraform raw state" is
// the kind of thing that stays harmless only as long as providers stay
// permissive.
func TestSyntheticDelta_PulumiOnlyPropertyPassesThroughRawState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	prov, schemaMap := syntheticResource("synthetic_flat", 0,
		map[string]*configschema.Attribute{
			"id":   {Type: cty.String, Computed: true},
			"name": {Type: cty.String, Optional: true},
		},
		shimschema.SchemaMap{
			"id":   (&shimschema.Schema{Type: shim.TypeString, Computed: true}).Shim(),
			"name": (&shimschema.Schema{Type: shim.TypeString, Optional: true}).Shim(),
		})

	attrs := []byte(`{"id":"r-1","name":"thing"}`)
	outputs, delta, _, _, err := ComputeInjectionState(ctx, prov, "synthetic_flat", attrs, schemaMap, nil)
	require.NoError(t, err)

	// Add a property Terraform has no knowledge of, exactly as
	// fillOutputsFromInputs does for "region".
	withExtra := map[string]interface{}{"region": "us-west-2"}
	for k, v := range outputs {
		withExtra[k] = v
	}

	rsd, err := tfbridge.UnmarshalRawStateDelta(propertyValueFromState(delta))
	require.NoError(t, err)
	recovered, err := rsd.Recover(
		resource.NewObjectProperty(resource.NewPropertyMapFromMap(withExtra)))
	require.NoError(t, err, "a Pulumi-only property must not break recovery")

	got := decodeExact(t, recovered)

	// Everything Terraform did have is unchanged.
	assert.Equal(t, "r-1", got["id"])
	assert.Equal(t, "thing", got["name"])

	// And the Pulumi-only property rides along rather than being dropped.
	assert.Equal(t, "us-west-2", got["region"],
		"if this is now absent, the delta started dropping Pulumi-only properties and "+
			"#30 should be revisited — the behaviour changed, for better or worse")
}

// TestSyntheticDelta_EmptyAndNullSurviveTheSidecar covers the distinctions that
// live at OUR seam rather than the bridge's: null vs empty string vs empty list
// vs empty map, after a json.Marshal/Unmarshal round trip. That is where
// omitempty and Go's zero values blur things the bridge kept separate at the
// schema level.
func TestSyntheticDelta_EmptyAndNullSurviveTheSidecar(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	prov, schemaMap := syntheticResource("synthetic_empties", 0,
		map[string]*configschema.Attribute{
			"id":         {Type: cty.String, Computed: true},
			"null_str":   {Type: cty.String, Optional: true},
			"empty_str":  {Type: cty.String, Optional: true},
			"null_list":  {Type: cty.List(cty.String), Optional: true},
			"empty_list": {Type: cty.List(cty.String), Optional: true},
			"null_map":   {Type: cty.Map(cty.String), Optional: true},
			"empty_map":  {Type: cty.Map(cty.String), Optional: true},
		},
		shimschema.SchemaMap{
			"id":        (&shimschema.Schema{Type: shim.TypeString, Computed: true}).Shim(),
			"null_str":  (&shimschema.Schema{Type: shim.TypeString, Optional: true}).Shim(),
			"empty_str": (&shimschema.Schema{Type: shim.TypeString, Optional: true}).Shim(),
			"null_list": (&shimschema.Schema{
				Type: shim.TypeList, Optional: true,
				Elem: (&shimschema.Schema{Type: shim.TypeString}).Shim(),
			}).Shim(),
			"empty_list": (&shimschema.Schema{
				Type: shim.TypeList, Optional: true,
				Elem: (&shimschema.Schema{Type: shim.TypeString}).Shim(),
			}).Shim(),
			"null_map": (&shimschema.Schema{
				Type: shim.TypeMap, Optional: true,
				Elem: (&shimschema.Schema{Type: shim.TypeString}).Shim(),
			}).Shim(),
			"empty_map": (&shimschema.Schema{
				Type: shim.TypeMap, Optional: true,
				Elem: (&shimschema.Schema{Type: shim.TypeString}).Shim(),
			}).Shim(),
		})

	attrs := []byte(`{"id":"r-1","null_str":null,"empty_str":"",` +
		`"null_list":null,"empty_list":[],"null_map":null,"empty_map":{}}`)

	outputs, delta, reason, _, err := ComputeInjectionState(
		ctx, prov, "synthetic_empties", attrs, schemaMap, nil)
	require.NoError(t, err)
	require.Empty(t, reason)

	// Every distinction must survive the sidecar's JSON round trip: an empty
	// list that comes back null, or an empty string that comes back absent,
	// is a diff on the next preview.
	assertDeltaRecoversExactly(t, attrs, outputs, delta)
}
