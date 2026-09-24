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
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
	"github.com/pulumi/opentofu/addrs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const tofuShowJSONWithLargeInteger = `{
  "format_version": "1.0",
  "values": {"root_module": {"resources": [{
    "address": "aws_x.y", "mode": "managed", "type": "aws_x", "name": "y",
    "provider_name": "registry.terraform.io/hashicorp/aws",
    "values": {"id": "r-1", "big": 9007199254740993}
  }]}}
}`

func TestTofuShowJSON_LargeIntegerSurvivesDecode(t *testing.T) {
	t.Parallel()

	const exact = "9007199254740993"
	const rounded = "9007199254740992"

	var withHook tfjson.State
	withHook.UseJSONNumber(true)
	require.NoError(t, json.Unmarshal([]byte(tofuShowJSONWithLargeInteger), &withHook))

	state, err := rawStateFromTfjson(&withHook)
	require.NoError(t, err)
	res := state.RootModule().Resources
	require.Len(t, res, 1, "expected the fixture's single resource")
	var attrs []byte
	for _, r := range res {
		for _, inst := range r.Instances {
			attrs = inst.Current.AttrsJSON
		}
	}
	require.NotEmpty(t, attrs)
	assert.Contains(t, string(attrs), exact,
		"the exact digits must reach AttrsJSON; rawStateFromTfjson re-marshals "+
			"whatever the decode produced, so a float64 here is unrecoverable")
	assert.NotContains(t, string(attrs), rounded)

	var withoutHook tfjson.State
	require.NoError(t, json.Unmarshal([]byte(tofuShowJSONWithLargeInteger), &withoutHook))
	bad, err := rawStateFromTfjson(&withoutHook)
	require.NoError(t, err)
	var badAttrs []byte
	for _, r := range bad.RootModule().Resources {
		for _, inst := range r.Instances {
			badAttrs = inst.Current.AttrsJSON
		}
	}
	require.True(t, strings.Contains(string(badAttrs), rounded),
		"expected a plain decode to round the value, but it did not — "+
			"terraform-json's behaviour changed and this test needs revisiting")
}

func TestTofuShowJSONPreservesInstanceAddresses(t *testing.T) {
	t.Parallel()
	root := &tfjson.StateModule{}
	moduleCases := []struct {
		address string
		key     addrs.InstanceKey
	}{
		{"module.network[0]", addrs.IntKey(0)},
		{`module.network["0"]`, addrs.StringKey("0")},
		{`module.network[""]`, addrs.StringKey("")},
		{`module.network["prod].eu"]`, addrs.StringKey("prod].eu")},
	}
	resourceCases := []struct {
		suffix string
		key    addrs.InstanceKey
	}{
		{"[0]", addrs.IntKey(0)},
		{"[1]", addrs.IntKey(1)},
		{`["0"]`, addrs.StringKey("0")},
		{`[""]`, addrs.StringKey("")},
	}
	for _, mod := range moduleCases {
		child := &tfjson.StateModule{Address: mod.address}
		for _, res := range resourceCases {
			address := mod.address + ".aws_subnet.this" + res.suffix
			child.Resources = append(child.Resources, &tfjson.StateResource{
				Address: address, Mode: tfjson.ManagedResourceMode, Type: "aws_subnet", Name: "this",
				ProviderName:    "registry.terraform.io/hashicorp/aws",
				AttributeValues: map[string]interface{}{"id": address},
			})
		}
		root.ChildModules = append(root.ChildModules, child)
	}
	state, err := rawStateFromTfjson(&tfjson.State{Values: &tfjson.StateValues{RootModule: root}})
	require.NoError(t, err)
	for _, mod := range moduleCases {
		module := state.Module(addrs.RootModuleInstance.Child("network", mod.key))
		require.NotNil(t, module, mod.address)
		resource := module.Resource(addrs.Resource{Mode: addrs.ManagedResourceMode, Type: "aws_subnet", Name: "this"})
		require.NotNil(t, resource)
		require.Len(t, resource.Instances, len(resourceCases))
		for _, res := range resourceCases {
			instance := resource.Instances[res.key]
			require.NotNil(t, instance)
			var attrs map[string]interface{}
			require.NoError(t, json.Unmarshal(instance.Current.AttrsJSON, &attrs))
			assert.Equal(t, mod.address+".aws_subnet.this"+res.suffix, attrs["id"])
		}
	}
}

func TestTofuShowJSONRejectsMalformedAddress(t *testing.T) {
	state, err := rawStateFromTfjson(&tfjson.State{Values: &tfjson.StateValues{
		RootModule: &tfjson.StateModule{Resources: []*tfjson.StateResource{{Address: "module.bad["}}},
	}})
	require.ErrorContains(t, err, "parsing resource address")
	assert.Nil(t, state)
}
