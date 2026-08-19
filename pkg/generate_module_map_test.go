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

// tofuShowJSONWithLargeInteger is a minimal "tofu show -json" document whose
// resource carries 2^53+1 — the smallest integer float64 cannot represent.
const tofuShowJSONWithLargeInteger = `{
  "format_version": "1.0",
  "values": {"root_module": {"resources": [{
    "address": "aws_x.y", "mode": "managed", "type": "aws_x", "name": "y",
    "provider_name": "registry.terraform.io/hashicorp/aws",
    "values": {"id": "r-1", "big": 9007199254740993}
  }]}}
}`

// TestTofuShowJSON_LargeIntegerSurvivesDecode covers the entry path for
// "tofu show -json" state, which is selected automatically whenever the
// document carries a "format_version" key — with no flag to indicate it.
//
// tfjson.State has a custom UnmarshalJSON that runs its OWN decoder, so setting
// UseNumber on a decoder at the call site is silently ignored;
// State.UseJSONNumber(true) is the only hook that reaches it. Without it every
// large integer is rounded before the digest is even built, and nothing
// downstream can recover: decodeAttrs cannot help, because rawStateFromTfjson
// has already re-marshalled a float64 by the time it runs.
//
// The failure mode is silence. 9007199254740993 becomes 9007199254740992 —
// a different integer that is still valid JSON, so no parser complains.
func TestTofuShowJSON_LargeIntegerSurvivesDecode(t *testing.T) {
	t.Parallel()

	const exact = "9007199254740993"
	const rounded = "9007199254740992"

	// What the tool does.
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

	// Non-vacuity: without the hook the value really is corrupted, so this test
	// is sensitive to the fix rather than merely consistent with it.
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
