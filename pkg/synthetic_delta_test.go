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

	"github.com/pulumi/opentofu/addrs"
	"github.com/pulumi/opentofu/states"
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

type fakeProvider struct {
	providers.Interface
	schema providers.GetProviderSchemaResponse
}

func (f *fakeProvider) GetProviderSchema(context.Context) providers.GetProviderSchemaResponse {
	return f.schema
}
func (f *fakeProvider) Name() string    { return "synthetic" }
func (f *fakeProvider) Version() string { return "0.0.0" }

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

	assertDeltaRecoversExactly(t, attrs, outputs, delta)
}

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

	attrs := []byte(`{"id":"obj-1","manifest":{"value":{"token":"` + redactedPlaceholder + `"},` +
		`"type":["object",{"token":"string"}]}}`)
	outputs, delta, _, _, err := ComputeInjectionState(ctx, prov, "synthetic_dynamic", attrs, schemaMap, nil)
	require.NoError(t, err)
	require.NotEmpty(t, delta)

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

	assert.Equal(t, "r-1", got["id"])
	assert.Equal(t, "thing", got["name"])

	assert.Equal(t, "us-west-2", got["region"],
		"if this is now absent, the delta started dropping Pulumi-only properties and "+
			"#30 should be revisited — the behaviour changed, for better or worse")
}

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

	assertDeltaRecoversExactly(t, attrs, outputs, delta)
}

func TestSyntheticDelta_RenamedPropertyRoundTrips(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	prov, schemaMap := syntheticResource("synthetic_renamed", 0,
		map[string]*configschema.Attribute{
			"id":              {Type: cty.String, Computed: true},
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

	require.Contains(t, outputs, "friendlyName",
		"the schema override should have renamed the property on the Pulumi side")

	deltaJSON, err := json.Marshal(delta)
	require.NoError(t, err)
	assert.Contains(t, string(deltaJSON), "renamed",
		"a name the bridge cannot derive must be recorded explicitly")

	assertDeltaRecoversExactly(t, attrs, outputs, delta)
}

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

	assertDeltaRecoversExactly(t, attrs, outputs, delta)
}

func TestSyntheticDelta_NumberSurvivesASchemaTypeMismatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	prov, schemaMap := syntheticResource("synthetic_num", 0,
		map[string]*configschema.Attribute{
			"id":    {Type: cty.String, Computed: true},
			"count": {Type: cty.Number, Optional: true},
		},
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
	t.Logf("delta=%s outputs.count=%#v (no num node expected; see the comment above)",
		deltaJSON, outputs["count"])

	assertDeltaRecoversExactly(t, attrs, outputs, delta)
}

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

	attrs := []byte(`{"id":"sg-1","cidr":["10.0.0.0/8","192.168.0.0/16"],` +
		`"ports":[22,443],"emptyset":[],"nullset":null}`)

	outputs, delta, reason, _, err := ComputeInjectionState(
		ctx, prov, "synthetic_set", attrs, schemaMap, nil)
	require.NoError(t, err)
	require.Empty(t, reason, "a set-typed attribute must not defeat delta computation")

	assertDeltaRecoversExactly(t, attrs, outputs, delta)
}

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

	require.Contains(t, outputs, "cidrBlocks",
		"the bridge should have pluralised the set attribute on the Pulumi side")
	require.Contains(t, outputs, "subnetIds",
		"the bridge should have pluralised the list attribute on the Pulumi side")

	deltaJSON, err := json.Marshal(delta)
	require.NoError(t, err)
	assert.Contains(t, string(deltaJSON), "renamed",
		"a pluralised name cannot be derived back, so it must be recorded explicitly")

	assertDeltaRecoversExactly(t, attrs, outputs, delta)
}

func TestSyntheticDelta_NestedCombinationsRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	inner := cty.Object(map[string]cty.Type{
		"port":  cty.Number,
		"proto": cty.String,
	})
	rule := cty.Object(map[string]cty.Type{
		"name":    cty.String,
		"labels":  cty.Map(cty.String),
		"targets": cty.List(inner),
	})

	prov, schemaMap := syntheticResource("synthetic_nested", 0,
		map[string]*configschema.Attribute{
			"id":    {Type: cty.String, Computed: true},
			"rule":  {Type: cty.List(rule), Optional: true},
			"group": {Type: cty.Set(inner), Optional: true},
		},
		shimschema.SchemaMap{
			"id": (&shimschema.Schema{Type: shim.TypeString, Computed: true}).Shim(),
			"rule": (&shimschema.Schema{
				Type: shim.TypeList, Optional: true,
				Elem: (&shimschema.Resource{Schema: shimschema.SchemaMap{
					"name": (&shimschema.Schema{Type: shim.TypeString, Optional: true}).Shim(),
					"labels": (&shimschema.Schema{
						Type: shim.TypeMap, Optional: true,
						Elem: (&shimschema.Schema{Type: shim.TypeString}).Shim(),
					}).Shim(),
					"targets": (&shimschema.Schema{
						Type: shim.TypeList, Optional: true,
						Elem: (&shimschema.Resource{Schema: shimschema.SchemaMap{
							"port":  (&shimschema.Schema{Type: shim.TypeInt, Optional: true}).Shim(),
							"proto": (&shimschema.Schema{Type: shim.TypeString, Optional: true}).Shim(),
						}}).Shim(),
					}).Shim(),
				}}).Shim(),
			}).Shim(),
			"group": (&shimschema.Schema{
				Type: shim.TypeSet, Optional: true,
				Elem: (&shimschema.Resource{Schema: shimschema.SchemaMap{
					"port":  (&shimschema.Schema{Type: shim.TypeInt, Optional: true}).Shim(),
					"proto": (&shimschema.Schema{Type: shim.TypeString, Optional: true}).Shim(),
				}}).Shim(),
			}).Shim(),
		})

	attrs := []byte(`{"id":"r-1",` +
		`"rule":[{"name":"web","labels":{"env":"prod","tier":"edge"},` +
		`"targets":[{"port":80,"proto":"tcp"},{"port":443,"proto":"tcp"}]},` +
		`{"name":"empty","labels":{},"targets":[]}],` +
		`"group":[{"port":22,"proto":"tcp"}]}`)

	outputs, delta, reason, _, err := ComputeInjectionState(
		ctx, prov, "synthetic_nested", attrs, schemaMap, nil)
	require.NoError(t, err)
	require.Empty(t, reason, "nesting must not defeat delta computation")

	deltaJSON, err := json.Marshal(delta)
	require.NoError(t, err)
	assert.Contains(t, string(deltaJSON), `"arr"`,
		"the delta should describe the list levels")
	t.Logf("nested delta: %s", deltaJSON)

	assertDeltaRecoversExactly(t, attrs, outputs, delta)
}

func TestSyntheticSecrets_SensitiveInputResolvesEndToEnd(t *testing.T) {
	t.Parallel()

	const (
		tfAddr    = "synthetic_secret.thing"
		attrName  = "admin_password"
		realValue = "hunter2-the-real-secret"
	)

	state := states.NewState()
	state.RootModule().SetResourceInstanceCurrent(
		addrs.ResourceInstance{
			Resource: addrs.Resource{
				Mode: addrs.ManagedResourceMode, Type: "synthetic_secret", Name: "thing",
			},
			Key: addrs.NoKey,
		},
		&states.ResourceInstanceObjectSrc{
			AttrsJSON: []byte(`{"id":"t-1","` + attrName + `":"` + realValue + `"}`),
			AttrSensitivePaths: []cty.PathValueMarks{
				{Path: cty.GetAttrPath(attrName), Marks: cty.NewValueMarks("sensitive")},
			},
		},
		addrs.AbsProviderConfig{
			Provider: addrs.MustParseProviderSourceString("registry.opentofu.org/hashicorp/synthetic"),
		},
		nil,
	)

	secrets, err := DiscoverSensitiveSecrets(state, "proj")
	require.NoError(t, err)
	require.Len(t, secrets, 1, "the sensitive attribute should produce exactly one config entry")
	configKey := secrets[0].ConfigKey
	assert.Equal(t, realValue, secrets[0].Value, "config must carry the REAL value")
	assert.True(t, secrets[0].Secret, "it must be written as a secret, not plain config")

	attrs := map[string]interface{}{"id": "t-1", attrName: realValue}
	redactSensitivePaths(attrs, []cty.PathValueMarks{
		{Path: cty.GetAttrPath(attrName), Marks: cty.NewValueMarks("sensitive")},
	})
	require.Equal(t, redactedPlaceholder, attrs[attrName],
		"the digest must not carry the plaintext")

	keys := redactedAttributeKeys(tfAddr, attrs)
	require.Equal(t, configKey, keys[attrName],
		"the sidecar's config key must match the key config was actually written under — "+
			"a mismatch here silently resolves the wrong secret, or none")

	r := &NonImportableResource{
		Type: "synthetic:index:Thing", Name: "thing",
		TerraformAddress:   tfAddr,
		Attributes:         attrs,
		RedactedAttributes: keys,
	}
	inputs := map[string]interface{}{
		attrName: secretPlaceholder, // what "pulumi preview --json" emits
		"id":     "t-1",
	}
	resolved, err := resolveSecretInputs(r, inputs, nil, map[string]string{configKey: realValue})
	require.NoError(t, err)
	assert.Equal(t, 1, resolved, "one secret should have been resolved")

	env, ok := inputs[attrName].(map[string]interface{})
	require.True(t, ok, "the resolved input should be a secret envelope, got %#v", inputs[attrName])
	assert.Equal(t, secretSig, env[sigKey], "the envelope must carry the Pulumi secret sig")
	assert.Contains(t, env["plaintext"], realValue, "the envelope must carry the real value")

	require.NoError(t, checkNoPlaceholders(r, "input", inputs, "inputs"))
}

func TestSyntheticSecrets_SensitiveInputWithNoConfigValueFails(t *testing.T) {
	t.Parallel()

	r := &NonImportableResource{
		Type: "synthetic:index:Thing", Name: "thing",
		TerraformAddress:   "synthetic_secret.thing",
		Attributes:         map[string]interface{}{"admin_password": redactedPlaceholder},
		RedactedAttributes: map[string]string{"admin_password": "thing_admin_password"},
	}
	inputs := map[string]interface{}{"admin_password": secretPlaceholder}

	_, err := resolveSecretInputs(r, inputs, nil, map[string]string{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "which is not set")
}
