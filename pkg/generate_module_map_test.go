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

	state := rawStateFromTfjson(&withHook)
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
	bad := rawStateFromTfjson(&withoutHook)
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
