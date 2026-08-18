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

// TestSyntheticDelta_RenamedPropertyRoundTrips covers objDelta.Renamed.
//
// The bridge derives a Terraform name from a Pulumi one with
// PulumiToTerraformName when it can, and records an explicit mapping only when
// that derivation would be wrong. Provider overlays rename attributes freely —
// this is common across Azure and GCP, where Pulumi names often diverge from
// Terraform's — so the recorded-rename path is not an edge case, and a lost
// rename silently reconstructs raw state under the wrong attribute name.
func TestSyntheticDelta_RenamedPropertyRoundTrips(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	prov, schemaMap := syntheticResource("synthetic_renamed", 0,
		map[string]*configschema.Attribute{
			"id": {Type: cty.String, Computed: true},
			// A name no camelCase derivation would produce from "friendlyName".
			"obscure_tf_name": {Type: cty.String, Optional: true},
		},
		shimschema.SchemaMap{
			"id":              (&shimschema.Schema{Type: shim.TypeString, Computed: true}).Shim(),
			"obscure_tf_name": (&shimschema.Schema{Type: shim.TypeString, Optional: true}).Shim(),
		})

	schemaInfos := map[string]*tfbridge.SchemaInfo{
		"obscure_tf_name": {Name: "friendlyName"},
	}

	attrs := []byte(`{"id":"r-1","obscure_tf_name":"value"}`)
	outputs, delta, reason, _, err := ComputeInjectionState(
		ctx, prov, "synthetic_renamed", attrs, schemaMap, schemaInfos)
	require.NoError(t, err)
	require.Empty(t, reason)

	// The Pulumi side really did use the overridden name — otherwise the
	// rename is never exercised and this test proves nothing.
	require.Contains(t, outputs, "friendlyName",
		"the schema override should have renamed the property on the Pulumi side")

	deltaJSON, err := json.Marshal(delta)
	require.NoError(t, err)
	assert.Contains(t, string(deltaJSON), "renamed",
		"a name the bridge cannot derive must be recorded explicitly")

	assertDeltaRecoversExactly(t, attrs, outputs, delta)
}

// TestSyntheticDelta_PopulatedListRoundTrips covers a list carrying actual
// elements. Every list in the existing fixtures is empty, so element handling
// inside arrayOrSetDelta had no coverage at this seam at all.
func TestSyntheticDelta_PopulatedListRoundTrips(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	prov, schemaMap := syntheticResource("synthetic_list", 0,
		map[string]*configschema.Attribute{
			"id":     {Type: cty.String, Computed: true},
			"algos":  {Type: cty.List(cty.String), Optional: true},
			"counts": {Type: cty.List(cty.Number), Optional: true},
		},
		shimschema.SchemaMap{
			"id": (&shimschema.Schema{Type: shim.TypeString, Computed: true}).Shim(),
			"algos": (&shimschema.Schema{
				Type: shim.TypeList, Optional: true,
				Elem: (&shimschema.Schema{Type: shim.TypeString}).Shim(),
			}).Shim(),
			"counts": (&shimschema.Schema{
				Type: shim.TypeList, Optional: true,
				Elem: (&shimschema.Schema{Type: shim.TypeInt}).Shim(),
			}).Shim(),
		})

	attrs := []byte(`{"id":"r-1","algos":["AES256","AES128",""],"counts":[0,1,42]}`)

	outputs, delta, reason, _, err := ComputeInjectionState(
		ctx, prov, "synthetic_list", attrs, schemaMap, nil)
	require.NoError(t, err)
	require.Empty(t, reason)

	// Order and emptiness both matter: a reordered list or a dropped empty
	// string is a diff on the next preview.
	assertDeltaRecoversExactly(t, attrs, outputs, delta)
}

// TestSyntheticDelta_NumberSurvivesASchemaTypeMismatch covers a numeric
// attribute whose Pulumi-side schema disagrees about its type.
//
// It does NOT reach the num delta node, and the name says so. num requires the
// Pulumi VALUE to be a string against a numeric Terraform type
// (rawstate.go:702), and declaring the shim schema as TypeString is not enough
// — MakeTerraformOutputs still produces the number 42, so the values agree and
// no node is emitted. The remaining route to a num node appears to be a value
// the bridge itself renders as a string, which is the large-integer path
// already covered (and which produces a replace node rather than num, because
// the digits do not survive either).
//
// Kept because the round trip is worth guarding regardless: a schema
// disagreement of this shape must not turn a Terraform number into a quoted
// string in raw state. num itself remains UNREACHED by this tool's pipeline,
// which is a fact worth recording rather than a gap to paper over.
func TestSyntheticDelta_NumberSurvivesASchemaTypeMismatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	prov, schemaMap := syntheticResource("synthetic_num", 0,
		map[string]*configschema.Attribute{
			"id":    {Type: cty.String, Computed: true},
			"count": {Type: cty.Number, Optional: true},
		},
		// Deliberately mismatched: Terraform says number, the Pulumi-side
		// schema says string.
		shimschema.SchemaMap{
			"id":    (&shimschema.Schema{Type: shim.TypeString, Computed: true}).Shim(),
			"count": (&shimschema.Schema{Type: shim.TypeString, Optional: true}).Shim(),
		})

	attrs := []byte(`{"id":"r-1","count":42}`)
	outputs, delta, reason, _, err := ComputeInjectionState(
		ctx, prov, "synthetic_num", attrs, schemaMap, nil)
	require.NoError(t, err)
	require.Empty(t, reason)

	deltaJSON, err := json.Marshal(delta)
	require.NoError(t, err)
	// Recorded, not asserted: if a num node ever appears here, the bridge's
	// conversion changed and the comment above is stale.
	t.Logf("delta=%s outputs.count=%#v (no num node expected; see the comment above)",
		deltaJSON, outputs["count"])

	// Whatever node kind the bridge chooses, the number must come back a
	// number — that is the property worth guarding, not the encoding.
	assertDeltaRecoversExactly(t, attrs, outputs, delta)
}

// TestSyntheticDelta_SetsRoundTrip covers set-typed attributes, which had zero
// coverage anywhere in this repo — cty.Set and shim.TypeSet appeared in no
// delta test at all.
//
// Sets are not a niche: AWS security group rules, Kubernetes, and much of Azure
// use them. They are also the case most likely to break quietly, because
// Terraform treats a set as unordered while its JSON encoding is an ordered
// array, so any reordering during the round trip is invisible until it shows up
// as a diff on the next preview.
//
// The delta node is arrayOrSetDelta — one type serving both — so a list test
// does not stand in for a set test at the schema level, where the shim type
// differs.
func TestSyntheticDelta_SetsRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	prov, schemaMap := syntheticResource("synthetic_set", 0,
		map[string]*configschema.Attribute{
			"id":       {Type: cty.String, Computed: true},
			"cidr":     {Type: cty.Set(cty.String), Optional: true},
			"ports":    {Type: cty.Set(cty.Number), Optional: true},
			"emptyset": {Type: cty.Set(cty.String), Optional: true},
			"nullset":  {Type: cty.Set(cty.String), Optional: true},
		},
		shimschema.SchemaMap{
			"id": (&shimschema.Schema{Type: shim.TypeString, Computed: true}).Shim(),
			"cidr": (&shimschema.Schema{
				Type: shim.TypeSet, Optional: true,
				Elem: (&shimschema.Schema{Type: shim.TypeString}).Shim(),
			}).Shim(),
			"ports": (&shimschema.Schema{
				Type: shim.TypeSet, Optional: true,
				Elem: (&shimschema.Schema{Type: shim.TypeInt}).Shim(),
			}).Shim(),
			"emptyset": (&shimschema.Schema{
				Type: shim.TypeSet, Optional: true,
				Elem: (&shimschema.Schema{Type: shim.TypeString}).Shim(),
			}).Shim(),
			"nullset": (&shimschema.Schema{
				Type: shim.TypeSet, Optional: true,
				Elem: (&shimschema.Schema{Type: shim.TypeString}).Shim(),
			}).Shim(),
		})

	// cty normalises set element order on decode, so the fixture is written in
	// the order ctyjson produces. What is under test is that the round trip
	// preserves whatever order it started with, not that it sorts.
	attrs := []byte(`{"id":"sg-1","cidr":["10.0.0.0/8","192.168.0.0/16"],` +
		`"ports":[22,443],"emptyset":[],"nullset":null}`)

	outputs, delta, reason, _, err := ComputeInjectionState(
		ctx, prov, "synthetic_set", attrs, schemaMap, nil)
	require.NoError(t, err)
	require.Empty(t, reason, "a set-typed attribute must not defeat delta computation")

	assertDeltaRecoversExactly(t, attrs, outputs, delta)
}

// TestSyntheticDelta_PluralizedNamesRoundTrip pins the naming transform with the
// worst track record in this repo.
//
// The bridge pluralises list and set attribute names on the Pulumi side — a
// Terraform "cidr_block" set becomes "cidrBlocks" — and PulumiToTerraformName
// cannot invert that. 513 attributes in pulumi-aws v7.24.0 fail to round-trip
// through it, which is exactly what made resolveSecretInputs silently delete
// program-declared inputs until it was fixed to distinguish a schema-derived
// name from a guess.
//
// The delta records these as objDelta.Renamed. This asserts that it does, and
// that the raw state comes back under the ORIGINAL Terraform names — the
// failure mode otherwise is a reconstruction that looks structurally fine while
// every pluralised attribute sits under the wrong key.
func TestSyntheticDelta_PluralizedNamesRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	prov, schemaMap := syntheticResource("synthetic_plural", 0,
		map[string]*configschema.Attribute{
			"id":         {Type: cty.String, Computed: true},
			"cidr_block": {Type: cty.Set(cty.String), Optional: true},
			"subnet_id":  {Type: cty.List(cty.String), Optional: true},
		},
		shimschema.SchemaMap{
			"id": (&shimschema.Schema{Type: shim.TypeString, Computed: true}).Shim(),
			"cidr_block": (&shimschema.Schema{
				Type: shim.TypeSet, Optional: true,
				Elem: (&shimschema.Schema{Type: shim.TypeString}).Shim(),
			}).Shim(),
			"subnet_id": (&shimschema.Schema{
				Type: shim.TypeList, Optional: true,
				Elem: (&shimschema.Schema{Type: shim.TypeString}).Shim(),
			}).Shim(),
		})

	attrs := []byte(`{"id":"vpc-1","cidr_block":["10.0.0.0/8"],"subnet_id":["subnet-a","subnet-b"]}`)

	outputs, delta, reason, _, err := ComputeInjectionState(
		ctx, prov, "synthetic_plural", attrs, schemaMap, nil)
	require.NoError(t, err)
	require.Empty(t, reason)

	// Guard the premise: the Pulumi side really did pluralise, or the rename
	// below is never exercised.
	require.Contains(t, outputs, "cidrBlocks",
		"the bridge should have pluralised the set attribute on the Pulumi side")
	require.Contains(t, outputs, "subnetIds",
		"the bridge should have pluralised the list attribute on the Pulumi side")

	deltaJSON, err := json.Marshal(delta)
	require.NoError(t, err)
	assert.Contains(t, string(deltaJSON), "renamed",
		"a pluralised name cannot be derived back, so it must be recorded explicitly")

	// And the raw state must come back under the ORIGINAL Terraform names.
	assertDeltaRecoversExactly(t, attrs, outputs, delta)
}
