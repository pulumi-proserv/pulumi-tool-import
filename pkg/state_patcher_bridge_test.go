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
	"testing"

	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/sig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// propertyValueFromJSON converts a JSON-deserialized value into a resource.PropertyValue,
// recognizing Pulumi sentinel maps (assets, archives, secrets) and converting them to
// proper typed PropertyValues. This simulates how the engine deserializes state.
func propertyValueFromJSON(v interface{}) resource.PropertyValue {
	replv := func(v interface{}) (resource.PropertyValue, bool) {
		m, ok := v.(map[string]interface{})
		if !ok {
			return resource.PropertyValue{}, false
		}
		s, hasSig := m[sig.Key].(string)
		if !hasSig {
			return resource.PropertyValue{}, false
		}
		switch s {
		case sig.Secret:
			elem := propertyValueFromJSON(m["value"])
			return resource.MakeSecret(elem), true
		default:
			// Asset/archive: use resource.DeserializeAsset/DeserializeArchive
			if a, isAsset, err := resource.DeserializeAsset(m); err == nil && isAsset {
				return resource.NewAssetProperty(a), true
			}
			if ar, isArchive, err := resource.DeserializeArchive(m); err == nil && isArchive {
				return resource.NewArchiveProperty(ar), true
			}
		}
		return resource.PropertyValue{}, false
	}
	return resource.NewPropertyValueRepl(v, nil, replv)
}

// validatePatchedOutputsAgainstDelta reads the patched state JSON and for every resource
// that has a __pulumi_raw_state_delta, validates that each property delta can Recover
// against the corresponding output value.
func validatePatchedOutputsAgainstDelta(t *testing.T, patchedState []byte) {
	t.Helper()

	var state map[string]interface{}
	require.NoError(t, json.Unmarshal(patchedState, &state))

	resources := state["deployment"].(map[string]interface{})["resources"].([]interface{})
	for _, raw := range resources {
		rMap := raw.(map[string]interface{})
		outputs, ok := rMap["outputs"].(map[string]interface{})
		if !ok {
			continue
		}
		deltaRaw, hasDelta := outputs["__pulumi_raw_state_delta"]
		if !hasDelta {
			continue
		}
		deltaMap, ok := deltaRaw.(map[string]interface{})
		if !ok {
			continue
		}
		urn, _ := rMap["urn"].(string)

		// Build the full outputs as a PropertyValue with proper sentinel handling.
		outputsPV := propertyValueFromJSON(outputs)

		// Build the full resource delta and try to recover the full outputs.
		deltaPV := resource.NewPropertyValue(deltaMap)
		rsd, err := tfbridge.UnmarshalRawStateDelta(deltaPV)
		require.NoError(t, err, "UnmarshalRawStateDelta failed for %s", urn)

		_, err = rsd.Recover(outputsPV)
		assert.NoError(t, err, "Recover failed for %s", urn)
	}
}
