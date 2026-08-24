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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPropertyValueFromState_JSONNumberBecomesNumberProperty(t *testing.T) {
	t.Parallel()
	pv := propertyValueFromState(json.Number("5"))
	require.True(t, pv.IsNumber(), "a json.Number must recover as a Number PropertyValue, got %#v", pv)
	assert.Equal(t, float64(5), pv.NumberValue())
	assert.False(t, pv.IsString())
}

func TestValidateRecover_NumberOutputRecoversAsNumberNotString(t *testing.T) {
	t.Parallel()

	outputs := map[string]interface{}{
		"count": json.Number("5"),
	}
	outputsPV := propertyValueFromState(outputs)

	deltaMap := map[string]interface{}{"obj": map[string]interface{}{}}
	deltaPV := resource.NewPropertyValue(deltaMap)
	rsd, err := tfbridge.UnmarshalRawStateDelta(deltaPV)
	require.NoError(t, err)

	recovered, err := rsd.Recover(outputsPV)
	require.NoError(t, err, "Recover reporting no error is exactly the vacuous-guard bug: "+
		"it must still reconstruct the correct raw JSON type")

	recoveredJSON, err := json.Marshal(recovered)
	require.NoError(t, err)

	var back map[string]interface{}
	require.NoError(t, json.Unmarshal(recoveredJSON, &back))

	_, isNumber := back["count"].(float64)
	assert.True(t, isNumber, "count must recover as a raw JSON number, got %#v (%T)", back["count"], back["count"])
}

func TestNormalizeTFName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input, expected string
	}{
		{"this[\"/clf-DEV/cf_rds_credentials\"]", "/clf-DEV/cf_rds_credentials"},
		{"bucket[\"my-bucket\"]", "my-bucket"},
		{"public[0]", "0"},
		{"plain_name", "plain_name"},
		{"this", "this"},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.expected, normalizeTFName(tc.input), "input: %s", tc.input)
	}
}

func TestShortPulumiType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input, expected string
	}{
		{"aws:secretsmanager/secret:Secret", "secret:Secret"},
		{"aws:s3/bucket:Bucket", "bucket:Bucket"},
		{"aws:rds/cluster:Cluster", "cluster:Cluster"},
		{"pulumi:pulumi:Stack", "pulumi:Stack"},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.expected, shortPulumiType(tc.input), "input: %s", tc.input)
	}
}

func TestPatchState_PatchesFromDigest(t *testing.T) {
	t.Parallel()

	// State: a secret with nil recoveryWindowInDays
	state := map[string]interface{}{
		"version": 3,
		"deployment": map[string]interface{}{
			"resources": []interface{}{
				map[string]interface{}{
					"urn":    "urn:pulumi:dev::proj::aws:secretsmanager/secret:Secret::my-secret",
					"type":   "aws:secretsmanager/secret:Secret",
					"custom": true,
					"id":     "arn:aws:secretsmanager:us-east-1:123:secret:my-secret",
					"inputs": map[string]interface{}{
						"name": "my-secret",
					},
					"outputs": map[string]interface{}{
						"name": "my-secret",
					},
				},
			},
		},
	}
	stateData, _ := json.Marshal(state)

	// Digest: the secret has recovery_window_in_days = 0
	digest := ModuleMap{
		RootResources: []ModuleResource{
			{
				Mode:             "managed",
				TranslatedURN:    "urn:pulumi:dev::proj::aws:secretsmanager/secret:Secret::my-secret",
				TerraformAddress: "aws_secretsmanager_secret.my_secret",
				ImportID:         "arn:aws:secretsmanager:us-east-1:123:secret:my-secret",
				Attributes: map[string]interface{}{
					"recovery_window_in_days": float64(0),
					"name":                    "my-secret",
				},
			},
		},
	}

	// Fields: secret:Secret has recoveryWindowInDays as not_read with default 30
	fields := &FieldsFile{
		Fields: map[string]FieldCategory{
			"secret:Secret": {
				NotRead: map[string]FieldInfo{
					"recoveryWindowInDays":        {Default: float64(30)},
					"forceOverwriteReplicaSecret": {Default: false},
				},
			},
		},
	}

	// Resource mapping: direct
	resourceMappings := map[string]string{
		"aws_secretsmanager_secret.my_secret": "my-secret",
	}

	patched, result, err := PatchState(stateData, &digest, fields, nil, resourceMappings, nil, "")
	require.NoError(t, err)
	assert.Equal(t, 1, result.Patched)
	assert.Equal(t, 1, result.FieldsFromDigest)   // recovery_window_in_days=0 from digest
	assert.Equal(t, 1, result.FieldsFromDefaults) // forceOverwriteReplicaSecret=false from default

	// Verify the patched value
	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	r := resources[0].(map[string]interface{})
	inputs := r["inputs"].(map[string]interface{})
	assert.Equal(t, float64(0), inputs["recoveryWindowInDays"])   // from digest, not default 30
	assert.Equal(t, false, inputs["forceOverwriteReplicaSecret"]) // from default
}

func TestPatchState_NotReadNumericField_JSONNumber_PatchesInputAndOutput(t *testing.T) {
	t.Parallel()

	state := map[string]interface{}{
		"version": 3,
		"deployment": map[string]interface{}{
			"resources": []interface{}{
				map[string]interface{}{
					"urn":    "urn:pulumi:dev::proj::aws:secretsmanager/secret:Secret::my-secret",
					"type":   "aws:secretsmanager/secret:Secret",
					"custom": true,
					"id":     "arn:aws:secretsmanager:us-east-1:123:secret:my-secret",
					"inputs": map[string]interface{}{
						"name": "my-secret",
					},
					"outputs": map[string]interface{}{
						"name": "my-secret",
					},
				},
			},
		},
	}
	stateData, _ := json.Marshal(state)

	digest := ModuleMap{
		RootResources: []ModuleResource{
			{
				Mode:             "managed",
				TranslatedURN:    "urn:pulumi:dev::proj::aws:secretsmanager/secret:Secret::my-secret",
				TerraformAddress: "aws_secretsmanager_secret.my_secret",
				ImportID:         "arn:aws:secretsmanager:us-east-1:123:secret:my-secret",
				Attributes: map[string]interface{}{
					"recovery_window_in_days": json.Number("14"),
					"name":                    "my-secret",
				},
			},
		},
	}

	fields := &FieldsFile{
		Fields: map[string]FieldCategory{
			"secret:Secret": {
				NotRead: map[string]FieldInfo{
					"recoveryWindowInDays": {Default: float64(30)},
				},
			},
		},
	}

	resourceMappings := map[string]string{
		"aws_secretsmanager_secret.my_secret": "my-secret",
	}

	patched, result, err := PatchState(stateData, &digest, fields, nil, resourceMappings, nil, "")
	require.NoError(t, err)
	assert.Equal(t, 1, result.Patched)
	assert.Equal(t, 1, result.FieldsFromDigest)

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	r := resources[0].(map[string]interface{})
	inputs := r["inputs"].(map[string]interface{})
	outputs := r["outputs"].(map[string]interface{})

	assert.Equal(t, float64(14), inputs["recoveryWindowInDays"], "input must be patched from the digest")
	assert.Equal(t, float64(14), outputs["recoveryWindowInDays"],
		"output must be patched to match the input; a numeric-only input with no matching "+
			"output is the divergence outputStale/outputIsBadPlain exist to prevent")
}

func TestPatchState_OutputStaleBoolean(t *testing.T) {
	t.Parallel()

	// State: an IAM role where import set input forceDetachPolicies=nil
	// and output forceDetachPolicies=false (bridge schema default).
	// The digest has force_detach_policies=true.
	// The patcher should patch both input AND output to true.
	state := map[string]interface{}{
		"version": 3,
		"deployment": map[string]interface{}{
			"resources": []interface{}{
				map[string]interface{}{
					"urn":    "urn:pulumi:dev::proj::aws:iam/role:Role::my-role",
					"type":   "aws:iam/role:Role",
					"custom": true,
					"id":     "my-role",
					"inputs": map[string]interface{}{
						"name": "my-role",
						// forceDetachPolicies is nil (not present) — not_read field
					},
					"outputs": map[string]interface{}{
						"name":                "my-role",
						"forceDetachPolicies": false, // bridge applied schema default
					},
				},
			},
		},
	}
	stateData, _ := json.Marshal(state)

	// Digest: the role has force_detach_policies = true
	digest := ModuleMap{
		RootResources: []ModuleResource{
			{
				Mode:             "managed",
				TranslatedURN:    "urn:pulumi:dev::proj::aws:iam/role:Role::my-role",
				TerraformAddress: "aws_iam_role.my_role",
				ImportID:         "my-role",
				Attributes: map[string]interface{}{
					"force_detach_policies": true,
					"name":                  "my-role",
				},
			},
		},
	}

	// Fields: role has forceDetachPolicies as not_read with default false
	fields := &FieldsFile{
		Fields: map[string]FieldCategory{
			"role:Role": {
				NotRead: map[string]FieldInfo{
					"forceDetachPolicies": {Default: false},
				},
			},
		},
	}

	resourceMappings := map[string]string{
		"aws_iam_role.my_role": "my-role",
	}

	patched, result, err := PatchState(stateData, &digest, fields, nil, resourceMappings, nil, "")
	require.NoError(t, err)
	assert.Equal(t, 1, result.Patched)
	assert.Equal(t, 1, result.FieldsFromDigest)

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	r := resources[0].(map[string]interface{})
	inputs := r["inputs"].(map[string]interface{})
	outputs := r["outputs"].(map[string]interface{})

	// Both input and output should be patched to true (simple boolean value)
	assert.Equal(t, true, inputs["forceDetachPolicies"], "input should be patched to true from digest")
	assert.Equal(t, true, outputs["forceDetachPolicies"], "output should be patched to true for simple value")
}

func TestPatchState_ConformToDelta_NoDeltaPassthrough(t *testing.T) {
	t.Parallel()

	// When there's no delta, digest values pass through unchanged.
	state := map[string]interface{}{
		"version": 3,
		"deployment": map[string]interface{}{
			"resources": []interface{}{
				map[string]interface{}{
					"urn":    "urn:pulumi:dev::proj::aws:iam/role:Role::my-role",
					"type":   "aws:iam/role:Role",
					"custom": true,
					"id":     "my-role",
					"inputs": map[string]interface{}{
						"name": "my-role",
					},
					"outputs": map[string]interface{}{
						"name":                "my-role",
						"forceDetachPolicies": false,
						// No __pulumi_raw_state_delta
					},
				},
			},
		},
	}
	stateData, _ := json.Marshal(state)

	digest := ModuleMap{
		RootResources: []ModuleResource{
			{
				Mode:             "managed",
				TranslatedURN:    "urn:pulumi:dev::proj::aws:iam/role:Role::my-role",
				TerraformAddress: "aws_iam_role.my_role",
				ImportID:         "my-role",
				Attributes: map[string]interface{}{
					"force_detach_policies": true,
					"name":                  "my-role",
				},
			},
		},
	}

	fields := &FieldsFile{
		Fields: map[string]FieldCategory{
			"role:Role": {
				NotRead: map[string]FieldInfo{
					"forceDetachPolicies": {Default: false},
				},
			},
		},
	}

	resourceMappings := map[string]string{
		"aws_iam_role.my_role": "my-role",
	}

	patched, _, err := PatchState(stateData, &digest, fields, nil, resourceMappings, nil, "")
	require.NoError(t, err)

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	outputs := resources[0].(map[string]interface{})["outputs"].(map[string]interface{})

	// Output should also be patched (simple boolean, no delta issue)
	assert.Equal(t, true, outputs["forceDetachPolicies"],
		"output should be patched to true for simple value")
}

func TestPatchState_ModuleLevelMatching(t *testing.T) {
	t.Parallel()

	// State: component child with nil recoveryWindowInDays
	state := map[string]interface{}{
		"version": 3,
		"deployment": map[string]interface{}{
			"resources": []interface{}{
				// Component
				map[string]interface{}{
					"urn":    "urn:pulumi:dev::proj::data:index:SecretsManager::my-secrets",
					"type":   "data:index:SecretsManager",
					"custom": false,
					"parent": "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev",
				},
				// Child secret
				map[string]interface{}{
					"urn":    "urn:pulumi:dev::proj::data:index:SecretsManager$aws:secretsmanager/secret:Secret::my-secrets-/my/creds",
					"type":   "aws:secretsmanager/secret:Secret",
					"custom": true,
					"id":     "arn:aws:secretsmanager:us-east-1:123:secret:my-creds",
					"parent": "urn:pulumi:dev::proj::data:index:SecretsManager::my-secrets",
					"inputs": map[string]interface{}{
						"name": "/my/creds",
					},
					"outputs": map[string]interface{}{
						"name": "/my/creds",
					},
				},
			},
		},
	}
	stateData, _ := json.Marshal(state)

	// Digest: module with the secret, recovery_window=0
	digest := ModuleMap{
		Modules: map[string]*ModuleMapEntry{
			"my-secrets": {
				TerraformPath: "module.my_secrets",
				Resources: []ModuleResource{
					{
						Mode:             "managed",
						TranslatedURN:    "urn:pulumi:dev::proj::aws:secretsmanager/secret:Secret::my_secrets_this[\"/my/creds\"]",
						TerraformAddress: "module.my_secrets.aws_secretsmanager_secret.this[\"/my/creds\"]",
						ImportID:         "arn:aws:secretsmanager:us-east-1:123:secret:my-creds",
						Attributes: map[string]interface{}{
							"recovery_window_in_days": float64(0),
						},
					},
				},
			},
		},
	}

	fields := &FieldsFile{
		Fields: map[string]FieldCategory{
			"secret:Secret": {
				NotRead: map[string]FieldInfo{
					"recoveryWindowInDays": {Default: float64(30)},
				},
			},
		},
	}

	// Module mapping (no resource mapping — must use module-level matching)
	moduleMappings := map[string]string{
		"module.my_secrets": "my-secrets",
	}

	patched, result, err := PatchState(stateData, &digest, fields, moduleMappings, nil, nil, "")
	require.NoError(t, err)
	assert.Equal(t, 1, result.Patched)
	assert.Equal(t, 1, result.FieldsFromDigest) // 0 from digest, not default 30

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	child := resources[1].(map[string]interface{})
	inputs := child["inputs"].(map[string]interface{})
	assert.Equal(t, float64(0), inputs["recoveryWindowInDays"])
}

func TestPatchState_DefaultFallback(t *testing.T) {
	t.Parallel()

	// State: bucket with nil forceDestroy, no digest match
	state := map[string]interface{}{
		"version": 3,
		"deployment": map[string]interface{}{
			"resources": []interface{}{
				map[string]interface{}{
					"urn":    "urn:pulumi:dev::proj::aws:s3/bucket:Bucket::orphan-bucket",
					"type":   "aws:s3/bucket:Bucket",
					"custom": true,
					"id":     "orphan-bucket",
					"parent": "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev",
					"inputs": map[string]interface{}{
						"bucket": "orphan-bucket",
					},
					"outputs": map[string]interface{}{
						"bucket": "orphan-bucket",
					},
				},
			},
		},
	}
	stateData, _ := json.Marshal(state)

	// Empty digest — no match
	digest := ModuleMap{}

	fields := &FieldsFile{
		Fields: map[string]FieldCategory{
			"bucket:Bucket": {
				NotRead: map[string]FieldInfo{
					"forceDestroy": {Default: false},
				},
			},
		},
	}

	patched, result, err := PatchState(stateData, &digest, fields, nil, nil, nil, "")
	require.NoError(t, err)
	assert.Equal(t, 1, result.Patched)
	assert.Equal(t, 0, result.FieldsFromDigest)
	assert.Equal(t, 1, result.FieldsFromDefaults)

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	r := resources[0].(map[string]interface{})
	inputs := r["inputs"].(map[string]interface{})
	assert.Equal(t, false, inputs["forceDestroy"])
}

func TestPatchState_SkipsSensitive(t *testing.T) {
	t.Parallel()

	state := map[string]interface{}{
		"version": 3,
		"deployment": map[string]interface{}{
			"resources": []interface{}{
				map[string]interface{}{
					"urn":     "urn:pulumi:dev::proj::aws:rds/cluster:Cluster::my-cluster",
					"type":    "aws:rds/cluster:Cluster",
					"custom":  true,
					"id":      "my-cluster",
					"parent":  "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev",
					"inputs":  map[string]interface{}{},
					"outputs": map[string]interface{}{},
				},
			},
		},
	}
	stateData, _ := json.Marshal(state)

	digest := ModuleMap{
		RootResources: []ModuleResource{
			{
				Mode:             "managed",
				TranslatedURN:    "urn:pulumi:dev::proj::aws:rds/cluster:Cluster::my-cluster",
				TerraformAddress: "aws_rds_cluster.my_cluster",
				Attributes: map[string]interface{}{
					"master_password":   "(sensitive)",
					"apply_immediately": nil,
				},
			},
		},
	}

	fields := &FieldsFile{
		Fields: map[string]FieldCategory{
			"cluster:Cluster": {
				NotRead: map[string]FieldInfo{
					"masterPassword":   {Default: nil},
					"applyImmediately": {Default: false},
				},
			},
		},
	}

	resourceMappings := map[string]string{
		"aws_rds_cluster.my_cluster": "my-cluster",
	}

	_, result, err := PatchState(stateData, &digest, fields, nil, resourceMappings, nil, "")
	require.NoError(t, err)
	assert.Equal(t, 1, result.SkippedSensitive)   // masterPassword
	assert.Equal(t, 1, result.FieldsFromDefaults) // applyImmediately=false
}

func TestPatchState_ResolveSensitiveFromConfig(t *testing.T) {
	state := map[string]interface{}{
		"version": 3,
		"deployment": map[string]interface{}{
			"resources": []interface{}{
				map[string]interface{}{
					"urn":     "urn:pulumi:dev::proj::aws:rds/cluster:Cluster::my-cluster",
					"type":    "aws:rds/cluster:Cluster",
					"custom":  true,
					"id":      "my-cluster",
					"parent":  "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev",
					"inputs":  map[string]interface{}{},
					"outputs": map[string]interface{}{},
				},
			},
		},
	}
	stateData, _ := json.Marshal(state)

	digest := ModuleMap{
		RootResources: []ModuleResource{
			{
				Mode:             "managed",
				TranslatedURN:    "urn:pulumi:dev::proj::aws:rds/cluster:Cluster::my-cluster",
				TerraformAddress: "aws_rds_cluster.my_cluster",
				Attributes: map[string]interface{}{
					"master_password": "(sensitive)",
				},
			},
		},
	}

	fields := &FieldsFile{
		Fields: map[string]FieldCategory{
			"cluster:Cluster": {
				NotRead: map[string]FieldInfo{
					"masterPassword": {Default: nil},
				},
			},
		},
	}

	resourceMappings := map[string]string{
		"aws_rds_cluster.my_cluster": "my-cluster",
	}

	// flattenAddress("aws_rds_cluster.my_cluster", "master_password") = "my_cluster_master_password"
	configSecrets := map[string]string{
		"my_cluster_master_password": "super-secret-pw",
	}

	patched, result, err := PatchState(stateData, &digest, fields, nil, resourceMappings, configSecrets, "")
	require.NoError(t, err)
	assert.Equal(t, 1, result.Patched)
	assert.Equal(t, 1, result.FieldsFromDigest) // resolved from config
	assert.Equal(t, 0, result.SkippedSensitive) // none skipped

	// Verify the patched value is wrapped in the secret sentinel.
	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	inputs := resources[0].(map[string]interface{})["inputs"].(map[string]interface{})
	sentinel, ok := inputs["masterPassword"].(map[string]interface{})
	require.True(t, ok, "masterPassword should be a secret sentinel map")
	assert.Equal(t, "1b47061264138c4ac30d75fd1eb44270", sentinel["4dabf18193072939515e22adb298388d"])
	assert.Equal(t, `"super-secret-pw"`, sentinel["plaintext"])

	// Output should be the unwrapped raw value (simple string)
	outputs := resources[0].(map[string]interface{})["outputs"].(map[string]interface{})
	assert.Equal(t, "super-secret-pw", outputs["masterPassword"], "output should be unwrapped raw value")
}

func TestPatchState_ResolveSensitiveReplacesNullSentinel(t *testing.T) {
	// Simulates a cloud API import where the provider Read returns nil for
	// a write-only field. The import process wraps it in a secret sentinel
	// with "null" plaintext. The patcher should replace it with the actual value.
	state := map[string]interface{}{
		"version": 3,
		"deployment": map[string]interface{}{
			"resources": []interface{}{
				map[string]interface{}{
					"urn":    "urn:pulumi:dev::proj::aws:rds/cluster:Cluster::my-cluster",
					"type":   "aws:rds/cluster:Cluster",
					"custom": true,
					"id":     "my-cluster",
					"parent": "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev",
					"inputs": map[string]interface{}{},
					"outputs": map[string]interface{}{
						"masterPassword": map[string]interface{}{
							"4dabf18193072939515e22adb298388d": "1b47061264138c4ac30d75fd1eb44270",
							"plaintext":                        "null",
						},
					},
				},
			},
		},
	}
	stateData, _ := json.Marshal(state)

	digest := ModuleMap{
		RootResources: []ModuleResource{
			{
				Mode:             "managed",
				TranslatedURN:    "urn:pulumi:dev::proj::aws:rds/cluster:Cluster::my-cluster",
				TerraformAddress: "aws_rds_cluster.my_cluster",
				Attributes: map[string]interface{}{
					"master_password": "(sensitive)",
				},
			},
		},
	}

	fields := &FieldsFile{
		Fields: map[string]FieldCategory{
			"cluster:Cluster": {
				NotRead: map[string]FieldInfo{
					"masterPassword": {Default: nil},
				},
			},
		},
	}

	resourceMappings := map[string]string{
		"aws_rds_cluster.my_cluster": "my-cluster",
	}

	configSecrets := map[string]string{
		"my_cluster_master_password": "super-secret-pw",
	}

	patched, result, err := PatchState(stateData, &digest, fields, nil, resourceMappings, configSecrets, "")
	require.NoError(t, err)
	assert.Equal(t, 1, result.Patched)
	assert.Equal(t, 1, result.FieldsFromDigest)
	assert.Equal(t, 0, result.SkippedSensitive)

	// Verify input was patched.
	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	inputs := resources[0].(map[string]interface{})["inputs"].(map[string]interface{})
	inSentinel, ok := inputs["masterPassword"].(map[string]interface{})
	require.True(t, ok, "input masterPassword should be a secret sentinel")
	assert.Equal(t, `"super-secret-pw"`, inSentinel["plaintext"])

	// Output should be the unwrapped raw value (simple string)
	outputs := resources[0].(map[string]interface{})["outputs"].(map[string]interface{})
	assert.Equal(t, "super-secret-pw", outputs["masterPassword"], "output should be unwrapped raw value")
}

func TestPatchState_AssetSentinel(t *testing.T) {
	// Create a temp file to act as the asset source.
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "swagger-ui", "index.html")
	require.NoError(t, os.MkdirAll(filepath.Dir(testFile), 0o755))
	require.NoError(t, os.WriteFile(testFile, []byte("<html>hello</html>"), 0o644))

	// Compute expected hash.
	h := sha256.New()
	h.Write([]byte("<html>hello</html>"))
	expectedHash := hex.EncodeToString(h.Sum(nil))

	state := map[string]interface{}{
		"version": 3,
		"deployment": map[string]interface{}{
			"resources": []interface{}{
				map[string]interface{}{
					"urn":    "urn:pulumi:dev::proj::aws:s3/bucketObject:BucketObject::my-obj",
					"type":   "aws:s3/bucketObject:BucketObject",
					"custom": true,
					"id":     "bucket/index.html",
					"parent": "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev",
					// source is a plain TF string (from import)
					"inputs":  map[string]interface{}{"source": "swagger-ui/index.html"},
					"outputs": map[string]interface{}{"source": "swagger-ui/index.html"},
				},
			},
		},
	}
	stateData, _ := json.Marshal(state)

	digest := ModuleMap{
		RootResources: []ModuleResource{
			{
				Mode:             "managed",
				TranslatedURN:    "urn:pulumi:dev::proj::aws:s3/bucketObject:BucketObject::my-obj",
				TerraformAddress: "aws_s3_object.my_obj",
				Attributes: map[string]interface{}{
					"source": "swagger-ui/index.html",
				},
			},
		},
	}

	fields := &FieldsFile{
		Fields: map[string]FieldCategory{
			"bucketObject:BucketObject": {
				NotRead: map[string]FieldInfo{
					"source": {Default: nil, Asset: "FileAsset"},
				},
			},
		},
	}

	resourceMappings := map[string]string{
		"aws_s3_object.my_obj": "my-obj",
	}

	patched, result, err := PatchState(stateData, &digest, fields, nil, resourceMappings, nil, tmpDir)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Patched)
	assert.Equal(t, 1, result.FieldsFromDigest)

	// Verify the input was patched to an asset sentinel.
	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	inputs := resources[0].(map[string]interface{})["inputs"].(map[string]interface{})
	sentinel, ok := inputs["source"].(map[string]interface{})
	require.True(t, ok, "source should be an asset sentinel map")
	assert.Equal(t, "c44067f5952c0a294b673a41bacd8c17", sentinel["4dabf18193072939515e22adb298388d"])
	assert.Equal(t, expectedHash, sentinel["hash"])
	assert.Equal(t, testFile, sentinel["path"])

	// Output should also be patched with the asset sentinel
	outputs := resources[0].(map[string]interface{})["outputs"].(map[string]interface{})
	outSentinel, ok := outputs["source"].(map[string]interface{})
	require.True(t, ok, "output source should be an asset sentinel map")
	assert.Equal(t, expectedHash, outSentinel["hash"])
}

func TestPatchState_PreservesRawStateDelta(t *testing.T) {
	// When a non-asset resource is patched, __pulumi_raw_state_delta should
	// be preserved. The delta handles simple value changes (string/number/bool)
	// naturally — only asset fields need delta updates.
	state := map[string]interface{}{
		"version": 3,
		"deployment": map[string]interface{}{
			"resources": []interface{}{
				map[string]interface{}{
					"urn":    "urn:pulumi:dev::proj::aws:secretsmanager/secret:Secret::my-secret",
					"type":   "aws:secretsmanager/secret:Secret",
					"custom": true,
					"id":     "arn:aws:secretsmanager:us-east-1:123:secret:foo",
					"parent": "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev",
					"inputs": map[string]interface{}{},
					"outputs": map[string]interface{}{
						"__pulumi_raw_state_delta": map[string]interface{}{
							"obj": map[string]interface{}{
								"ps": map[string]interface{}{},
							},
						},
						"arn": "arn:aws:secretsmanager:us-east-1:123:secret:foo",
					},
				},
				// Unpatched resource should keep its delta.
				map[string]interface{}{
					"urn":    "urn:pulumi:dev::proj::aws:s3/bucket:Bucket::my-bucket",
					"type":   "aws:s3/bucket:Bucket",
					"custom": true,
					"id":     "my-bucket",
					"parent": "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev",
					"inputs": map[string]interface{}{"bucket": "my-bucket"},
					"outputs": map[string]interface{}{
						"__pulumi_raw_state_delta": map[string]interface{}{
							"obj": map[string]interface{}{},
						},
						"bucket": "my-bucket",
					},
				},
			},
		},
	}
	stateData, _ := json.Marshal(state)

	digest := ModuleMap{
		RootResources: []ModuleResource{
			{
				Mode:             "managed",
				TranslatedURN:    "urn:pulumi:dev::proj::aws:secretsmanager/secret:Secret::my-secret",
				TerraformAddress: "aws_secretsmanager_secret.foo",
				Attributes: map[string]interface{}{
					"recovery_window_in_days": float64(0),
				},
			},
		},
	}

	fields := &FieldsFile{
		Fields: map[string]FieldCategory{
			"secret:Secret": {
				NotRead: map[string]FieldInfo{
					"recoveryWindowInDays": {Default: float64(30)},
				},
			},
		},
	}

	resourceMappings := map[string]string{
		"aws_secretsmanager_secret.foo": "my-secret",
	}

	patched, result, err := PatchState(stateData, &digest, fields, nil, resourceMappings, nil, "")
	require.NoError(t, err)
	assert.Equal(t, 1, result.Patched)

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})

	// Patched non-asset resource: delta should be preserved.
	patchedOutputs := resources[0].(map[string]interface{})["outputs"].(map[string]interface{})
	_, hasDelta := patchedOutputs["__pulumi_raw_state_delta"]
	assert.True(t, hasDelta, "__pulumi_raw_state_delta should be preserved on non-asset patched resource")
	assert.Equal(t, "arn:aws:secretsmanager:us-east-1:123:secret:foo", patchedOutputs["arn"])

	// Unpatched resource: __pulumi_raw_state_delta should be preserved.
	unpatchedOutputs := resources[1].(map[string]interface{})["outputs"].(map[string]interface{})
	_, hasDelta = unpatchedOutputs["__pulumi_raw_state_delta"]
	assert.True(t, hasDelta, "__pulumi_raw_state_delta should be preserved on unpatched resource")
}

func TestPatchState_InjectsAssetDelta(t *testing.T) {
	// When an asset field is patched, the __pulumi_raw_state_delta should be
	// updated with an asset delta entry, not removed. This allows the bridge
	// to correctly reconstruct the TF raw state via TranslateAsset/TranslateArchive.
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "swagger-ui", "index.html")
	require.NoError(t, os.MkdirAll(filepath.Dir(testFile), 0o755))
	require.NoError(t, os.WriteFile(testFile, []byte("<html>hello</html>"), 0o644))

	assetKind := 0 // FileAsset
	state := map[string]interface{}{
		"version": 3,
		"deployment": map[string]interface{}{
			"resources": []interface{}{
				map[string]interface{}{
					"urn":    "urn:pulumi:dev::proj::aws:s3/bucketObject:BucketObject::my-obj",
					"type":   "aws:s3/bucketObject:BucketObject",
					"custom": true,
					"id":     "bucket/index.html",
					"parent": "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev",
					"inputs": map[string]interface{}{"source": "swagger-ui/index.html"},
					"outputs": map[string]interface{}{
						"source": "swagger-ui/index.html",
						"__pulumi_raw_state_delta": map[string]interface{}{
							"obj": map[string]interface{}{
								"ps": map[string]interface{}{},
							},
						},
					},
				},
			},
		},
	}
	stateData, _ := json.Marshal(state)

	digest := ModuleMap{
		RootResources: []ModuleResource{
			{
				Mode:             "managed",
				TranslatedURN:    "urn:pulumi:dev::proj::aws:s3/bucketObject:BucketObject::my-obj",
				TerraformAddress: "aws_s3_object.my_obj",
				Attributes: map[string]interface{}{
					"source": "swagger-ui/index.html",
				},
			},
		},
	}

	fields := &FieldsFile{
		Fields: map[string]FieldCategory{
			"bucketObject:BucketObject": {
				NotRead: map[string]FieldInfo{
					"source": {Default: nil, Asset: "FileAsset", AssetKind: &assetKind},
				},
			},
		},
	}

	resourceMappings := map[string]string{
		"aws_s3_object.my_obj": "my-obj",
	}

	patched, result, err := PatchState(stateData, &digest, fields, nil, resourceMappings, nil, tmpDir)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Patched)

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	outputs := resources[0].(map[string]interface{})["outputs"].(map[string]interface{})

	// Delta should still exist (not removed).
	delta, hasDelta := outputs["__pulumi_raw_state_delta"].(map[string]interface{})
	require.True(t, hasDelta, "delta should be present after asset patching")

	// Delta should have the asset entry injected for "source".
	obj := delta["obj"].(map[string]interface{})
	ps := obj["ps"].(map[string]interface{})
	sourceDelta, hasSource := ps["source"].(map[string]interface{})
	require.True(t, hasSource, "delta should have source property delta")

	assetEntry, hasAsset := sourceDelta["asset"].(map[string]interface{})
	require.True(t, hasAsset, "source delta should be an asset delta")
	assert.Equal(t, float64(0), assetEntry["kind"]) // FileAsset = 0

	// No other delta entries should have been added.
	assert.Equal(t, 1, len(ps), "only source delta should be in ps")
}

func TestPatchState_InjectsArchiveDelta(t *testing.T) {
	// For FileArchive fields, the delta should include archiveFormat and hashField.
	tmpDir := t.TempDir()
	lambdaDir := filepath.Join(tmpDir, "my_lambda")
	require.NoError(t, os.MkdirAll(lambdaDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(lambdaDir, "index.py"), []byte("print('hello')"), 0o644))

	assetKind := 1     // FileArchive
	archiveFormat := 3 // ZIPArchive
	state := map[string]interface{}{
		"version": 3,
		"deployment": map[string]interface{}{
			"resources": []interface{}{
				map[string]interface{}{
					"urn":    "urn:pulumi:dev::proj::aws:lambda/function:Function::my-fn",
					"type":   "aws:lambda/function:Function",
					"custom": true,
					"id":     "my-fn",
					"parent": "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev",
					"inputs": map[string]interface{}{},
					"outputs": map[string]interface{}{
						"__pulumi_raw_state_delta": map[string]interface{}{
							"obj": map[string]interface{}{
								"ps": map[string]interface{}{},
							},
						},
					},
				},
			},
		},
	}
	stateData, _ := json.Marshal(state)

	digest := ModuleMap{
		RootResources: []ModuleResource{
			{
				Mode:             "managed",
				TranslatedURN:    "urn:pulumi:dev::proj::aws:lambda/function:Function::my-fn",
				TerraformAddress: "aws_lambda_function.my_fn",
				Attributes: map[string]interface{}{
					"filename": "./my_lambda.zip",
				},
			},
		},
	}

	fields := &FieldsFile{
		Fields: map[string]FieldCategory{
			"function:Function": {
				NotRead: map[string]FieldInfo{
					"code": {
						Default:       nil,
						Asset:         "FileArchive",
						AssetKind:     &assetKind,
						ArchiveFormat: &archiveFormat,
						HashField:     "source_code_hash",
					},
				},
			},
		},
	}

	resourceMappings := map[string]string{
		"aws_lambda_function.my_fn": "my-fn",
	}

	patched, result, err := PatchState(stateData, &digest, fields, nil, resourceMappings, nil, tmpDir)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Patched)

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	outputs := resources[0].(map[string]interface{})["outputs"].(map[string]interface{})

	// Delta should have archive entry for "code".
	delta := outputs["__pulumi_raw_state_delta"].(map[string]interface{})
	obj := delta["obj"].(map[string]interface{})
	ps := obj["ps"].(map[string]interface{})
	codeDelta := ps["code"].(map[string]interface{})
	assetEntry := codeDelta["asset"].(map[string]interface{})

	assert.Equal(t, float64(1), assetEntry["kind"])          // FileArchive
	assert.Equal(t, float64(3), assetEntry["archiveFormat"]) // ZIPArchive
	assert.Equal(t, "source_code_hash", assetEntry["hashField"])

	// The code input sentinel should have a hash computed by the Pulumi archive package.
	inputs := resources[0].(map[string]interface{})["inputs"].(map[string]interface{})
	codeSentinel, ok := inputs["code"].(map[string]interface{})
	require.True(t, ok, "code input should be an archive sentinel")
	codeHash, ok := codeSentinel["hash"].(string)
	require.True(t, ok, "code sentinel should have a hash")
	assert.NotEmpty(t, codeHash, "code hash should not be empty")
	assert.Len(t, codeHash, 64, "hash should be 64-char SHA256 hex")
}

func TestPatchState_CamelCasesNestedDigestKeys(t *testing.T) {
	// When the digest has an array of objects with snake_case keys (e.g.,
	// parameter: [{apply_method: "immediate", name: "rds.force_ssl", value: "1"}]),
	// the patcher should convert to camelCase for Pulumi state.
	state := map[string]interface{}{
		"version": 3,
		"deployment": map[string]interface{}{
			"resources": []interface{}{
				map[string]interface{}{
					"urn":     "urn:pulumi:dev::proj::aws:rds/clusterParameterGroup:ClusterParameterGroup::my-params",
					"type":    "aws:rds/clusterParameterGroup:ClusterParameterGroup",
					"custom":  true,
					"id":      "my-params",
					"parent":  "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev",
					"inputs":  map[string]interface{}{"parameters": nil},
					"outputs": map[string]interface{}{"parameters": nil},
				},
			},
		},
	}
	stateData, _ := json.Marshal(state)

	digest := ModuleMap{
		RootResources: []ModuleResource{
			{
				Mode:             "managed",
				TranslatedURN:    "urn:pulumi:dev::proj::aws:rds/clusterParameterGroup:ClusterParameterGroup::my-params",
				TerraformAddress: "aws_rds_cluster_parameter_group.my_params",
				Attributes: map[string]interface{}{
					"parameter": []interface{}{
						map[string]interface{}{
							"apply_method": "immediate",
							"name":         "rds.force_ssl",
							"value":        "1",
						},
					},
				},
			},
		},
	}

	fields := &FieldsFile{
		Fields: map[string]FieldCategory{
			"clusterParameterGroup:ClusterParameterGroup": {
				NotRead: map[string]FieldInfo{
					"parameters": {Default: nil},
				},
			},
		},
	}

	resourceMappings := map[string]string{
		"aws_rds_cluster_parameter_group.my_params": "my-params",
	}

	patched, result, err := PatchState(stateData, &digest, fields, nil, resourceMappings, nil, "")
	require.NoError(t, err)
	assert.Equal(t, 1, result.Patched)
	assert.Equal(t, 1, result.FieldsFromDigest)

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	inputs := resources[0].(map[string]interface{})["inputs"].(map[string]interface{})

	params, ok := inputs["parameters"].([]interface{})
	require.True(t, ok, "parameters should be an array")
	require.Len(t, params, 1)

	param := params[0].(map[string]interface{})
	// Keys should be camelCase, not snake_case.
	assert.Equal(t, "immediate", param["applyMethod"])
	assert.Equal(t, "rds.force_ssl", param["name"])
	assert.Equal(t, "1", param["value"])
	// Snake case key should NOT be present.
	_, hasSnake := param["apply_method"]
	assert.False(t, hasSnake, "apply_method should not be present (should be applyMethod)")
}

func TestPropertyValueFromState_CiphertextSecretStaysSecret(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		in   map[string]interface{}
	}{
		{"ciphertext (stack export, no --show-secrets)", map[string]interface{}{
			sigKey: "1b47061264138c4ac30d75fd1eb44270", "ciphertext": "v1:abc:def",
		}},
		{"value (engine in-memory form)", map[string]interface{}{
			sigKey: "1b47061264138c4ac30d75fd1eb44270", "value": "hunter2",
		}},
		{"plaintext (--show-secrets)", map[string]interface{}{
			sigKey: "1b47061264138c4ac30d75fd1eb44270", "plaintext": `"hunter2"`,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pv := propertyValueFromState(tc.in)
			assert.True(t, pv.IsSecret(), "must recover as a Secret, got %s", pv.TypeString())
			assert.False(t, pv.IsObject(), "recovering as an Object is what breaks Recover")
		})
	}
}

func TestDeltaPropertyValue_JSONNumberSurvives(t *testing.T) {
	t.Parallel()

	delta := `{"obj":{"ps":{"code":{"asset":{"kind":1,"archiveFormat":2}}}}}`

	var plain map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(delta), &plain))

	dec := json.NewDecoder(strings.NewReader(delta))
	dec.UseNumber()
	var numbered map[string]interface{}
	require.NoError(t, dec.Decode(&numbered))

	_, plainErr := tfbridge.UnmarshalRawStateDelta(deltaPropertyValue(plain))
	_, numErr := tfbridge.UnmarshalRawStateDelta(deltaPropertyValue(numbered))
	assert.NoError(t, plainErr)
	assert.NoError(t, numErr, "a UseNumber-decoded delta must unmarshal like a plain one")
}

func TestPatchRevert_DoesNotKeepInjectedAssetDeltas(t *testing.T) {
	t.Parallel()

	outputs := map[string]interface{}{
		"code": "x",
		rawStateDeltaKey: map[string]interface{}{
			"obj": map[string]interface{}{"ps": map[string]interface{}{}},
		},
	}

	snapshot := make(map[string]interface{}, len(outputs))
	for k, v := range outputs {
		snapshot[k] = v
	}
	snapshot[rawStateDeltaKey] = deepCopyJSONValue(outputs[rawStateDeltaKey])

	before, err := json.Marshal(snapshot[rawStateDeltaKey])
	require.NoError(t, err)

	outputs[rawStateDeltaKey] = injectAssetDeltas(outputs[rawStateDeltaKey],
		[]assetFieldDeltaInfo{{pulumiField: "code", kind: 0}})

	after, err := json.Marshal(snapshot[rawStateDeltaKey])
	require.NoError(t, err)
	assert.JSONEq(t, string(before), string(after),
		"the snapshot must be unaffected by injectAssetDeltas, or a revert cannot restore it")

	live, err := json.Marshal(outputs[rawStateDeltaKey])
	require.NoError(t, err)
	assert.NotEqual(t, string(before), string(live))
}

func TestDeepCopyJSONValue_SharesNothingMutable(t *testing.T) {
	t.Parallel()

	orig := map[string]interface{}{
		"m": map[string]interface{}{"k": "v"},
		"a": []interface{}{map[string]interface{}{"k": "v"}},
		"s": "scalar",
	}
	cp := deepCopyJSONValue(orig).(map[string]interface{})

	cp["m"].(map[string]interface{})["k"] = "changed"
	cp["a"].([]interface{})[0].(map[string]interface{})["k"] = "changed"

	assert.Equal(t, "v", orig["m"].(map[string]interface{})["k"])
	assert.Equal(t, "v", orig["a"].([]interface{})[0].(map[string]interface{})["k"])
}

func TestPatchState_NoMatchIsNamedPerResource(t *testing.T) {
	t.Parallel()

	// The unmatched resources use a type with no digest entry at all, so even
	// the single-unused-candidate guess cannot match them.
	state := []byte(`{"version":3,"deployment":{"resources":[
		{"urn":"urn:pulumi:dev::proj::aws:ecs/service:Service::unmatched-one","type":"aws:ecs/service:Service","custom":true,"id":"a","inputs":{},"outputs":{}},
		{"urn":"urn:pulumi:dev::proj::aws:ecs/service:Service::unmatched-two","type":"aws:ecs/service:Service","custom":true,"id":"b","inputs":{},"outputs":{}},
		{"urn":"urn:pulumi:dev::proj::aws:s3/bucketObject:BucketObject::matched","type":"aws:s3/bucketObject:BucketObject","custom":true,"id":"c","inputs":{},"outputs":{}}
	]}}`)
	fields := &FieldsFile{
		Fields: map[string]FieldCategory{
			// No defaults, so an unmatched resource has nothing to patch from.
			"service:Service":           {NotRead: map[string]FieldInfo{"waitForSteadyState": {}}},
			"bucketObject:BucketObject": {NotRead: map[string]FieldInfo{"content": {}}},
		},
	}
	digest := &ModuleMap{
		RootResources: []ModuleResource{{
			Mode:             "managed",
			TranslatedURN:    "urn:pulumi:dev::proj::aws:s3/bucketObject:BucketObject::matched",
			TerraformAddress: "aws_s3_bucket_object.matched",
			Attributes:       map[string]interface{}{"content": "hello"},
		}},
		Modules: map[string]*ModuleMapEntry{},
	}

	_, result, err := PatchState(state, digest, fields, nil,
		map[string]string{"aws_s3_bucket_object.matched": "matched"}, nil, "")
	require.NoError(t, err)
	require.Equal(t, 2, result.NoMatch)
	require.Len(t, result.NoMatchNotes, 2, "one note per unmatched resource")
	joined := strings.Join(result.NoMatchNotes, "\n")
	assert.Contains(t, joined, "unmatched-one")
	assert.Contains(t, joined, "unmatched-two")
	assert.NotContains(t, joined, `"matched"`, "a matched resource must not be listed")
}

func TestPatchStateFromCFN_NoMatchIsNamed(t *testing.T) {
	t.Parallel()

	state := buildTestState("aws:s3/bucketObject:BucketObject", "cfn-unmatched", map[string]any{})
	fields := &FieldsFile{
		Fields: map[string]FieldCategory{
			"bucketObject:BucketObject": {
				NotRead: map[string]FieldInfo{"content": {}},
			},
		},
	}

	_, result, err := PatchStateFromCFN(state, map[string]*ModuleResource{}, fields, nil, "")
	require.NoError(t, err)
	require.Equal(t, 1, result.NoMatch)
	require.Len(t, result.NoMatchNotes, 1)
	assert.Contains(t, result.NoMatchNotes[0], "cfn-unmatched")
}
