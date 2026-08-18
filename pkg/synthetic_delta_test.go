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
