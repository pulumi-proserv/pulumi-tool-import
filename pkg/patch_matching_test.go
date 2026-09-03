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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTFNameForPulumiField(t *testing.T) {
	t.Parallel()
	cases := []struct{ pulumi, tf string }{
		{"allowMajorVersionUpgrade", "allow_major_version_upgrade"},
		{"userDataBase64", "user_data_base64"},
		{"endpointAutoConfirms", "endpoint_auto_confirms"},
		{"acl", "acl"},
		{"keepers", "keepers"},
		// True renames must keep coming from the hand table, not mechanics.
		{"code", "filename"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.tf, tfNameForPulumiField(tc.pulumi), "pulumi field %s", tc.pulumi)
	}
}

func TestPatchState_DigestResolvesFieldsOutsideTheHandTable(t *testing.T) {
	t.Parallel()

	stateData := buildTestState("aws:rds/instance:Instance", "db", map[string]any{
		"identifier": "db",
	})
	digest := &ModuleMap{
		RootResources: []ModuleResource{{
			Mode:             "managed",
			TranslatedURN:    "urn:pulumi:dev::proj::aws:rds/instance:Instance::db",
			TerraformAddress: "aws_db_instance.db",
			Attributes: map[string]interface{}{
				"allow_major_version_upgrade": true,
				"identifier":                  "db",
			},
		}},
	}
	fields := &FieldsFile{
		Fields: map[string]FieldCategory{
			"aws:rds/instance:Instance": {
				NotRead: map[string]FieldInfo{
					// Falsy default: with the digest lookup broken this field
					// is silently suppressed; with it working, the digest's
					// true wins regardless of the default.
					"allowMajorVersionUpgrade": {Default: false},
				},
			},
		},
	}

	patched, result, err := PatchState(stateData, digest, fields, nil,
		map[string]string{"aws_db_instance.db": "db"}, nil, "")
	require.NoError(t, err)
	assert.Equal(t, 1, result.FieldsFromDigest,
		"a field absent from the hand table must still resolve from the digest by mechanical name mapping")

	var st map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &st))
	res := st["deployment"].(map[string]interface{})["resources"].([]interface{})[0].(map[string]interface{})
	assert.Equal(t, true, res["inputs"].(map[string]interface{})["allowMajorVersionUpgrade"])
	assert.Equal(t, true, res["outputs"].(map[string]interface{})["allowMajorVersionUpgrade"],
		"the digest value must reach outputs — that is what the bridged provider diffs against")
}

func TestPatchState_FullTypeKeyDoesNotMatchOtherTypeWithSameSuffix(t *testing.T) {
	t.Parallel()

	stateData := buildTestState("aws:iam/group:Group", "devs", map[string]any{
		"name": "devs",
	})
	fields := &FieldsFile{
		Fields: map[string]FieldCategory{
			"aws:autoscaling/group:Group": {
				NotRead: map[string]FieldInfo{
					"waitForCapacityTimeout": {Default: "10m"},
				},
			},
		},
	}

	patched, result, err := PatchState(stateData, &ModuleMap{}, fields, nil, nil, nil, "")
	require.NoError(t, err)
	assert.Equal(t, 0, result.Patched,
		"a fields-file entry keyed by full type must not match a different type sharing its suffix")
	assert.Equal(t, 1, result.NoFields)

	var st map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &st))
	res := st["deployment"].(map[string]interface{})["resources"].([]interface{})[0].(map[string]interface{})
	_, has := res["inputs"].(map[string]interface{})["waitForCapacityTimeout"]
	assert.False(t, has, "an autoscaling field must never be written onto an IAM group")
}

func TestPatchState_AuthoredShortKeyStillMatchesBySuffix(t *testing.T) {
	t.Parallel()

	stateData := buildTestState("aws:cloudwatch/logGroup:LogGroup", "lg", map[string]any{
		"name": "lg",
	})
	fields := &FieldsFile{
		Fields: map[string]FieldCategory{
			// Authoring the SHORT form is the explicit opt-in to suffix matching.
			"logGroup:LogGroup": {
				NotRead: map[string]FieldInfo{
					"skipDestroy": {Default: true},
				},
			},
		},
	}

	patched, result, err := PatchState(stateData, &ModuleMap{}, fields, nil, nil, nil, "")
	require.NoError(t, err)
	assert.Equal(t, 1, result.Patched)

	var st map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &st))
	res := st["deployment"].(map[string]interface{})["resources"].([]interface{})[0].(map[string]interface{})
	assert.Equal(t, true, res["inputs"].(map[string]interface{})["skipDestroy"])
}
