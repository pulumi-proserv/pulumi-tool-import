// Copyright 2016-2026, Pulumi Corporation.
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

package tfaddr

import (
	"testing"

	"github.com/pulumi/opentofu/addrs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseResourcePreservesTypedKeys(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		address                string
		moduleKey, resourceKey addrs.InstanceKey
	}{
		{`module.network[0].aws_route.default[1]`, addrs.IntKey(0), addrs.IntKey(1)},
		{`module.network["0"].aws_route.default["1"]`, addrs.StringKey("0"), addrs.StringKey("1")},
		{`module.network[""].aws_route.default[""]`, addrs.StringKey(""), addrs.StringKey("")},
		{`module.network["prod].eu"].aws_route.default["a].b\"\\c"]`, addrs.StringKey("prod].eu"), addrs.StringKey(`a].b"\c`)},
	} {
		t.Run(tc.address, func(t *testing.T) {
			instance, err := ParseResource(tc.address)
			require.NoError(t, err)
			require.Len(t, instance.Module, 1)
			assert.Equal(t, tc.moduleKey, instance.Module[0].InstanceKey)
			assert.Equal(t, tc.resourceKey, instance.Resource.Key)
			assert.Equal(t, tc.address, instance.String())
		})
	}
}

func TestResourceType(t *testing.T) {
	t.Parallel()
	for _, address := range []string{
		"aws_foo.bar",
		"module.a.module.b.aws_foo.bar[0]",
		`aws_foo.bar["k"]`,
		"module.a[0].aws_foo.bar",
		`module.a["prod.eu"].module.b[1].aws_foo.bar["key.with[brackets]"]`,
		"module.a[0].data.aws_foo.bar",
	} {
		t.Run(address, func(t *testing.T) {
			assert.Equal(t, "aws_foo", ResourceType(address))
		})
	}
	for _, address := range []string{"", "module.a[", "aws_foo", `aws_foo.bar[key]`, "aws_foo.bar.attr"} {
		_, err := ParseResource(address)
		assert.Error(t, err)
		assert.Empty(t, ResourceType(address))
	}
}

func TestParseName(t *testing.T) {
	t.Parallel()
	instance, err := ParseName(`params["a].b\"\\c"]`)
	require.NoError(t, err)
	assert.Equal(t, "params", instance.Resource.Name)
	assert.Equal(t, `a].b"\c`, KeyValue(instance.Key))
	assert.Equal(t, `params["a].b\"\\c"]`, Name(instance.Resource.Name, instance.Key))
	for _, name := range []string{"params[", "params.extra", "params[noquotes]"} {
		_, err := ParseName(name)
		assert.Error(t, err)
	}
}

func TestParseModule(t *testing.T) {
	t.Parallel()
	for _, address := range []string{"", `module.a["0"].module.b[""]`, `module.a["quoted\".key"]`} {
		module, err := ParseModule(address)
		require.NoError(t, err)
		assert.Equal(t, address, module.String())
	}
	_, err := ParseModule("aws_foo.bar")
	assert.Error(t, err)
}
