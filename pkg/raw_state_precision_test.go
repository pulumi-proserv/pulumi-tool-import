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

	shim "github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfshim"
	shimschema "github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfshim/schema"
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/vendored/opentofu/configs/configschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func bigIntResource() (*fakeProvider, shim.SchemaMap) {
	return syntheticResource("synthetic_big", 0,
		map[string]*configschema.Attribute{
			"id":     {Type: cty.String, Computed: true},
			"big_id": {Type: cty.Number, Optional: true},
			"ids":    {Type: cty.List(cty.Number), Optional: true},
		},
		shimschema.SchemaMap{
			"id":     (&shimschema.Schema{Type: shim.TypeString, Computed: true}).Shim(),
			"big_id": (&shimschema.Schema{Type: shim.TypeInt, Optional: true}).Shim(),
			"ids": (&shimschema.Schema{Type: shim.TypeList,
				Elem: (&shimschema.Schema{Type: shim.TypeInt}).Shim()}).Shim(),
		})
}

func TestComputeInjectionState_PreservesLargeIntegers(t *testing.T) {
	t.Parallel()
	prov, schemaMap := bigIntResource()

	// 2^53+1 rounds to 2^53 through float64; 2^60+1 rounds to 2^60.
	attrs := []byte(`{"id":"x","big_id":9007199254740993,"ids":[1152921504606846977]}`)
	outputs, _, _, _, err := ComputeInjectionState(
		context.Background(), prov, "synthetic_big", attrs, schemaMap, nil)
	require.NoError(t, err)

	out, err := json.Marshal(outputs)
	require.NoError(t, err)
	assert.Contains(t, string(out), "9007199254740993")
	assert.NotContains(t, string(out), "9007199254740992")
	assert.Contains(t, string(out), "1152921504606846977")
	assert.NotContains(t, string(out), "1152921504606846976")
}

func TestComputeInjectionState_FloatFormSourceMakesLeafAmbiguous(t *testing.T) {
	t.Parallel()
	prov, schemaMap := bigIntResource()

	// A float/exponent-form source that lands on the same float64 as a lossy
	// integer's rounding makes that VALUE ambiguous: an output leaf holding
	// it may legitimately be the float-form source, so rewriting it with the
	// integer's digits would corrupt it. The leaf stays rounded — never a
	// guess — while the rest of the conversion (and the delta, which carries
	// the exact value downstream) is kept.
	attrs := []byte(`{"id":"x","big_id":9007199254740993,"ids":[9.007199254740992e15]}`)
	outputs, delta, _, _, err := ComputeInjectionState(
		context.Background(), prov, "synthetic_big", attrs, schemaMap, nil)
	require.NoError(t, err)
	require.NotEmpty(t, delta, "an ambiguous leaf must not cost the resource its delta")

	assert.Equal(t, float64(9007199254740992), outputs["bigId"],
		"an ambiguous leaf stays rounded rather than being guessed")
}

func TestComputeInjectionState_AmbiguousLeavesStayRoundedOthersRepair(t *testing.T) {
	t.Parallel()
	prov, schemaMap := bigIntResource()

	// Two distinct integers rounding to the same float64 (both to 2^60) are
	// unrepairable and stay rounded; an unrelated repairable integer in the
	// same resource is still restored, and the delta survives.
	attrs := []byte(`{"id":"x","big_id":1152921504606846977,` +
		`"ids":[1152921504606846978,9007199254740993]}`)
	outputs, delta, _, _, err := ComputeInjectionState(
		context.Background(), prov, "synthetic_big", attrs, schemaMap, nil)
	require.NoError(t, err)
	require.NotEmpty(t, delta)

	assert.Equal(t, float64(1152921504606846976), outputs["bigId"],
		"a colliding value stays rounded")
	ids, ok := outputs["ids"].([]interface{})
	require.True(t, ok, "ids: %T", outputs["ids"])
	require.Len(t, ids, 2)
	assert.Equal(t, float64(1152921504606846976), ids[0],
		"the other colliding leaf stays rounded too")
	assert.Equal(t, json.Number("9007199254740993"), ids[1],
		"an unambiguous leaf in the same resource is still repaired")
}
