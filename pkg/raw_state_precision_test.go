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

func TestComputeInjectionState_FloatFormSourceMakesRepairAmbiguous(t *testing.T) {
	t.Parallel()
	prov, schemaMap := bigIntResource()

	// A float/exponent-form source that lands on the same float64 as a lossy
	// integer's rounding makes the repair ambiguous: an output leaf holding
	// that value may legitimately be the float-form source, so rewriting it
	// with the integer's digits would corrupt it.
	attrs := []byte(`{"id":"x","big_id":9007199254740993,"ids":[9.007199254740992e15]}`)
	_, _, _, _, err := ComputeInjectionState(
		context.Background(), prov, "synthetic_big", attrs, schemaMap, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "float64")
}

func TestComputeInjectionState_AmbiguousLargeIntegersError(t *testing.T) {
	t.Parallel()
	prov, schemaMap := bigIntResource()

	// Two distinct integers that round to the same float64 (both to 2^60):
	// neither can be restored unambiguously, and a wrong guess writes a wrong
	// value into state, so the caller must fall back to raw renaming.
	attrs := []byte(`{"id":"x","big_id":1152921504606846977,"ids":[1152921504606846978]}`)
	_, _, _, _, err := ComputeInjectionState(
		context.Background(), prov, "synthetic_big", attrs, schemaMap, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "float64")
}
