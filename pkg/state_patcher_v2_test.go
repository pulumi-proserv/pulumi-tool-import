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

	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge/info"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper to build a FieldsFile using the flat v2 JSON format (no not_read wrapper).
func fieldsFileFromJSON(t *testing.T, jsonStr string) *FieldsFile {
	t.Helper()
	var ff FieldsFile
	require.NoError(t, json.Unmarshal([]byte(jsonStr), &ff))
	return &ff
}

func TestPatchState_V2FieldsFile_FlatFormat(t *testing.T) {
	t.Parallel()

	ff := fieldsFileFromJSON(t, `{
		"fields": {
			"aws:s3/bucket:Bucket": {
				"forceDestroy": { "default": false }
			}
		}
	}`)

	// Verify parsing worked — NotRead should be populated via UnmarshalJSON.
	require.Len(t, ff.Fields, 1)
	cat := ff.Fields["aws:s3/bucket:Bucket"]
	require.Len(t, cat.NotRead, 1)
	assert.Equal(t, false, cat.NotRead["forceDestroy"].Default)
}

func TestPatchState_V2FieldsFile_AssetField(t *testing.T) {
	t.Parallel()

	ff := fieldsFileFromJSON(t, `{
		"fields": {
			"aws:lambda/function:Function": {
				"publish": { "default": false },
				"code": { "asset": "FileArchive", "assetKind": 2, "archiveFormat": 3, "hashField": "source_code_hash" }
			}
		}
	}`)

	cat := ff.Fields["aws:lambda/function:Function"]
	require.Len(t, cat.NotRead, 2)
	assert.Equal(t, false, cat.NotRead["publish"].Default)
	assert.Equal(t, "FileArchive", cat.NotRead["code"].Asset)
	require.NotNil(t, cat.NotRead["code"].AssetKind)
	assert.Equal(t, 2, *cat.NotRead["code"].AssetKind)
	require.NotNil(t, cat.NotRead["code"].ArchiveFormat)
	assert.Equal(t, 3, *cat.NotRead["code"].ArchiveFormat)
	assert.Equal(t, "source_code_hash", cat.NotRead["code"].HashField)
}

func TestPatchState_V2FieldsFile_LoadFromDisk(t *testing.T) {
	t.Parallel()

	ff, err := LoadFieldsFile("../data/aws-import-diff-fields.json")
	require.NoError(t, err)

	// Verify a few known entries parsed correctly.
	bucket := ff.Fields["aws:s3/bucket:Bucket"]
	require.Contains(t, bucket.NotRead, "forceDestroy")
	assert.Equal(t, false, bucket.NotRead["forceDestroy"].Default)

	lambda := ff.Fields["aws:lambda/function:Function"]
	require.Contains(t, lambda.NotRead, "code")
	assert.Equal(t, "FileArchive", lambda.NotRead["code"].Asset)
}

func TestPatchState_V2_FileAsset_BridgeRecover(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "swagger-ui", "index.html")
	require.NoError(t, os.MkdirAll(filepath.Dir(testFile), 0o755))
	require.NoError(t, os.WriteFile(testFile, []byte("<html>hello</html>"), 0o644))

	h := sha256.New()
	h.Write([]byte("<html>hello</html>"))
	expectedHash := hex.EncodeToString(h.Sum(nil))

	ff := fieldsFileFromJSON(t, `{
		"fields": {
			"aws:s3/bucketObject:BucketObject": {
				"source": { "asset": "FileAsset", "assetKind": 0 },
				"forceDestroy": { "default": false },
				"acl": { "default": "private" }
			}
		}
	}`)

	stateData := buildTestStateIO("aws:s3/bucketObject:BucketObject", "my-obj",
		map[string]any{"source": "swagger-ui/index.html", "bucket": "my-bucket"},
		map[string]any{
			"source": "swagger-ui/index.html", "bucket": "my-bucket",
			"__pulumi_raw_state_delta": map[string]any{"obj": map[string]any{"ps": map[string]any{}}},
		},
	)

	digest := &ModuleMap{
		RootResources: []ModuleResource{{
			Mode: "managed", TranslatedURN: "urn:pulumi:dev::proj::aws:s3/bucketObject:BucketObject::my-obj",
			TerraformAddress: "aws_s3_object.my_obj",
			Attributes:       map[string]interface{}{"source": "swagger-ui/index.html", "bucket": "my-bucket"},
		}},
	}

	patched, result, err := PatchState(stateData, digest, ff, nil, map[string]string{"aws_s3_object.my_obj": "my-obj"}, nil, tmpDir)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, result.Patched, 1)

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	inputs := resources[0].(map[string]interface{})["inputs"].(map[string]interface{})
	outputs := resources[0].(map[string]interface{})["outputs"].(map[string]interface{})

	inputSentinel, ok := inputs["source"].(map[string]interface{})
	require.True(t, ok, "input source should be asset sentinel")
	assert.Equal(t, assetSig, inputSentinel[sigKey])
	assert.Equal(t, expectedHash, inputSentinel["hash"])

	outputSentinel, ok := outputs["source"].(map[string]interface{})
	require.True(t, ok, "output source should be asset sentinel")
	assert.Equal(t, assetSig, outputSentinel[sigKey])

	validatePatchedOutputsAgainstDelta(t, patched)
}

func TestPatchState_V2_FileArchive_BridgeRecover(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	deployDir := filepath.Join(tmpDir, "deploy")
	require.NoError(t, os.MkdirAll(deployDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(deployDir, "handler.py"), []byte("def handler(): pass"), 0o644))

	ff := fieldsFileFromJSON(t, `{
		"fields": {
			"aws:lambda/function:Function": {
				"code": { "asset": "FileArchive", "assetKind": 2, "archiveFormat": 3, "hashField": "source_code_hash" },
				"publish": { "default": false }
			}
		}
	}`)

	stateData := buildTestStateIO("aws:lambda/function:Function", "my-fn",
		map[string]any{"code": "deploy.zip", "functionName": "my-fn", "role": "arn:aws:iam::123:role/test"},
		map[string]any{
			"code": "deploy.zip", "functionName": "my-fn", "role": "arn:aws:iam::123:role/test",
			"__pulumi_raw_state_delta": map[string]any{
				"obj": map[string]any{
					"ps":      map[string]any{},
					"renamed": map[string]any{"code": "filename", "name": "function_name"},
				},
			},
		},
	)

	digest := &ModuleMap{
		RootResources: []ModuleResource{{
			Mode: "managed", TranslatedURN: "urn:pulumi:dev::proj::aws:lambda/function:Function::my-fn",
			TerraformAddress: "aws_lambda_function.my_fn",
			Attributes:       map[string]interface{}{"filename": "deploy.zip", "function_name": "my-fn", "role": "arn:aws:iam::123:role/test", "source_code_hash": "abc123hash"},
		}},
	}

	patched, result, err := PatchState(stateData, digest, ff, nil, map[string]string{"aws_lambda_function.my_fn": "my-fn"}, nil, tmpDir)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, result.Patched, 1)

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	inputs := resources[0].(map[string]interface{})["inputs"].(map[string]interface{})
	outputs := resources[0].(map[string]interface{})["outputs"].(map[string]interface{})

	inputCode, ok := inputs["code"].(map[string]interface{})
	require.True(t, ok, "input code should be archive sentinel")
	assert.Equal(t, archiveSig, inputCode[sigKey])

	outputCode, ok := outputs["code"].(map[string]interface{})
	require.True(t, ok, "output code should be archive sentinel")
	assert.Equal(t, archiveSig, outputCode[sigKey])

	delta := outputs["__pulumi_raw_state_delta"].(map[string]interface{})
	ps := delta["obj"].(map[string]interface{})["ps"].(map[string]interface{})
	codeDelta := ps["code"].(map[string]interface{})
	assetEntry := codeDelta["asset"].(map[string]interface{})
	assert.EqualValues(t, 2, assetEntry["kind"])

	validatePatchedOutputsAgainstDelta(t, patched)
}

func TestPatchState_V2_DefaultApplication_BridgeRecover(t *testing.T) {
	t.Parallel()

	ff := fieldsFileFromJSON(t, `{
		"fields": {
			"aws:s3/bucketObject:BucketObject": {
				"forceDestroy": { "default": false },
				"acl": { "default": "private" }
			}
		}
	}`)

	stateData := buildTestStateIO("aws:s3/bucketObject:BucketObject", "my-obj",
		map[string]any{"bucket": "my-bucket", "key": "test.txt"},
		map[string]any{
			"bucket": "my-bucket", "key": "test.txt",
			"__pulumi_raw_state_delta": map[string]any{"obj": map[string]any{"ps": map[string]any{}}},
		},
	)

	digest := &ModuleMap{
		RootResources: []ModuleResource{{
			Mode: "managed", TranslatedURN: "urn:pulumi:dev::proj::aws:s3/bucketObject:BucketObject::my-obj",
			TerraformAddress: "aws_s3_object.my_obj",
			Attributes:       map[string]interface{}{"bucket": "my-bucket", "key": "test.txt"},
		}},
	}

	patched, result, err := PatchState(stateData, digest, ff, nil, map[string]string{"aws_s3_object.my_obj": "my-obj"}, nil, "")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, result.FieldsFromDefaults, 1)

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	inputs := resources[0].(map[string]interface{})["inputs"].(map[string]interface{})

	assert.Equal(t, false, inputs["forceDestroy"])
	assert.Equal(t, "private", inputs["acl"])

	validatePatchedOutputsAgainstDelta(t, patched)
}

func TestPatchState_V2_RecoverFailure_RevertsOutputs(t *testing.T) {
	t.Parallel()

	ff := fieldsFileFromJSON(t, `{
		"fields": {
			"aws:s3/bucketObject:BucketObject": {
				"acl": { "default": "private" }
			}
		}
	}`)

	stateData := buildTestStateIO("aws:s3/bucketObject:BucketObject", "my-obj",
		map[string]any{"bucket": "my-bucket", "key": "test.txt"},
		map[string]any{
			"bucket": "my-bucket", "key": "test.txt", "acl": nil,
			"__pulumi_raw_state_delta": map[string]any{
				"obj": map[string]any{
					"ps": map[string]any{
						"acl": map[string]any{
							"plu": map[string]any{
								"i": map[string]any{"obj": map[string]any{"ps": map[string]any{}}},
							},
						},
					},
				},
			},
		},
	)

	digest := &ModuleMap{
		RootResources: []ModuleResource{{
			Mode: "managed", TranslatedURN: "urn:pulumi:dev::proj::aws:s3/bucketObject:BucketObject::my-obj",
			TerraformAddress: "aws_s3_object.my_obj",
			Attributes:       map[string]interface{}{"bucket": "my-bucket", "key": "test.txt", "acl": "private"},
		}},
	}

	patched, result, err := PatchState(stateData, digest, ff, nil, map[string]string{"aws_s3_object.my_obj": "my-obj"}, nil, "")
	require.NoError(t, err)

	assert.Equal(t, 1, result.DeltaFailed, "expected 1 delta failure")
	assert.Equal(t, 0, result.DeltaValidated, "expected 0 delta validated")

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	rMap := resources[0].(map[string]interface{})
	outputs := rMap["outputs"].(map[string]interface{})
	inputs := rMap["inputs"].(map[string]interface{})

	assert.Nil(t, outputs["acl"], "acl output should be reverted to nil")
	_, hasDelta := outputs["__pulumi_raw_state_delta"]
	assert.True(t, hasDelta, "delta should still be present after revert")
	// Inputs are also reverted to keep state consistent.
	assert.Nil(t, inputs["acl"], "acl input should also be reverted")
}

func TestIsFalsyDefault(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		value    interface{}
		expected bool
	}{
		{"bool false", false, true},
		{"bool true", true, false},
		{"float64 zero", float64(0), true},
		{"float64 nonzero", float64(30), false},
		{"json.Number zero", json.Number("0"), true},
		{"json.Number nonzero", json.Number("1"), false},
		{"string empty", "", true},
		{"string nonempty", "private", false},
		{"nil", nil, false},
		{"map", map[string]interface{}{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, isFalsyDefault(tc.value))
		})
	}
}

func TestSemverAtLeast(t *testing.T) {
	t.Parallel()
	tests := []struct {
		version, minimum string
		expected         bool
	}{
		{"7.27.0", "7.27.0", true},
		{"7.34.0", "7.27.0", true},
		{"7.26.0", "7.27.0", false},
		{"8.0.0", "7.27.0", true},
		{"6.99.99", "7.27.0", false},
		{"7.27.1", "7.27.0", true},
		{"", "7.27.0", false},
		{"invalid", "7.27.0", false},
		{"7.27.0", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.version+"_>=_"+tc.minimum, func(t *testing.T) {
			assert.Equal(t, tc.expected, semverAtLeast(tc.version, tc.minimum))
		})
	}
}

func TestProviderPackage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		resourceType, expected string
	}{
		{"aws:s3/bucket:Bucket", "aws"},
		{"aws:lambda/function:Function", "aws"},
		{"gcp:compute/instance:Instance", "gcp"},
		{"pulumi:pulumi:Stack", "pulumi"},
		{"", ""},
	}
	for _, tc := range tests {
		t.Run(tc.resourceType, func(t *testing.T) {
			assert.Equal(t, tc.expected, providerPackage(tc.resourceType))
		})
	}
}

func TestPatchState_FalsyDefaultSuppression(t *testing.T) {
	t.Parallel()

	ff := fieldsFileFromJSON(t, `{
		"falsyDefaultSuppression": {
			"aws": "7.27.0"
		},
		"fields": {
			"aws:s3/bucket:Bucket": {
				"forceDestroy": { "default": false }
			},
			"aws:secretsmanager/secret:Secret": {
				"recoveryWindowInDays": { "default": 30 },
				"forceOverwriteReplicaSecret": { "default": false }
			}
		}
	}`)

	stateJSON := `{
		"version": 3,
		"deployment": {
			"resources": [
				{
					"urn": "urn:pulumi:test::proj::pulumi:providers:aws::my-aws",
					"type": "pulumi:providers:aws",
					"custom": true,
					"inputs": { "version": "7.34.0" },
					"outputs": {}
				},
				{
					"urn": "urn:pulumi:test::proj::aws:s3/bucket:Bucket::my-bucket",
					"type": "aws:s3/bucket:Bucket",
					"custom": true,
					"provider": "urn:pulumi:test::proj::pulumi:providers:aws::my-aws::fake-uuid",
					"inputs": {},
					"outputs": {}
				},
				{
					"urn": "urn:pulumi:test::proj::aws:secretsmanager/secret:Secret::my-secret",
					"type": "aws:secretsmanager/secret:Secret",
					"custom": true,
					"provider": "urn:pulumi:test::proj::pulumi:providers:aws::my-aws::fake-uuid",
					"inputs": {},
					"outputs": {}
				}
			]
		}
	}`

	digest := &ModuleMap{}

	patched, result, err := PatchState([]byte(stateJSON), digest, ff, nil, nil, nil, "")
	require.NoError(t, err)

	assert.Equal(t, 2, result.SkippedFalsySuppressed, "should skip 2 falsy defaults")
	assert.Equal(t, 1, result.FieldsFromDefaults, "should patch 1 non-falsy default")

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	deployment := patchedState["deployment"].(map[string]interface{})
	resources := deployment["resources"].([]interface{})

	bucket := resources[1].(map[string]interface{})
	bucketInputs := bucket["inputs"].(map[string]interface{})
	assert.NotContains(t, bucketInputs, "forceDestroy", "falsy default should be skipped")

	secret := resources[2].(map[string]interface{})
	secretInputs := secret["inputs"].(map[string]interface{})
	assert.Contains(t, secretInputs, "recoveryWindowInDays", "non-falsy default should be patched")
	assert.NotContains(t, secretInputs, "forceOverwriteReplicaSecret", "falsy default should be skipped")
}

func TestPatchState_FalsyDefaultSuppression_OldProvider(t *testing.T) {
	t.Parallel()

	ff := fieldsFileFromJSON(t, `{
		"falsyDefaultSuppression": {
			"aws": "7.27.0"
		},
		"fields": {
			"aws:s3/bucket:Bucket": {
				"forceDestroy": { "default": false }
			}
		}
	}`)

	stateJSON := `{
		"version": 3,
		"deployment": {
			"resources": [
				{
					"urn": "urn:pulumi:test::proj::pulumi:providers:aws::my-aws",
					"type": "pulumi:providers:aws",
					"custom": true,
					"inputs": { "version": "7.26.0" },
					"outputs": {}
				},
				{
					"urn": "urn:pulumi:test::proj::aws:s3/bucket:Bucket::my-bucket",
					"type": "aws:s3/bucket:Bucket",
					"custom": true,
					"provider": "urn:pulumi:test::proj::pulumi:providers:aws::my-aws::fake-uuid",
					"inputs": {},
					"outputs": {}
				}
			]
		}
	}`

	digest := &ModuleMap{}

	_, result, err := PatchState([]byte(stateJSON), digest, ff, nil, nil, nil, "")
	require.NoError(t, err)

	assert.Equal(t, 0, result.SkippedFalsySuppressed)
	assert.Equal(t, 1, result.FieldsFromDefaults, "falsy default should be patched for old provider")
}

func TestPatchState_FalsyDefaultSuppression_DigestOverridesDefault(t *testing.T) {
	t.Parallel()

	// Field has falsy default (false) but digest has non-falsy value (true).
	// The falsy default should be suppressed, but the digest value should still be patched.
	ff := fieldsFileFromJSON(t, `{
		"falsyDefaultSuppression": {
			"aws": "7.27.0"
		},
		"fields": {
			"aws:s3/bucket:Bucket": {
				"forceDestroy": { "default": false }
			}
		}
	}`)

	stateJSON := `{
		"version": 3,
		"deployment": {
			"resources": [
				{
					"urn": "urn:pulumi:test::proj::pulumi:providers:aws::my-aws",
					"type": "pulumi:providers:aws",
					"custom": true,
					"inputs": { "version": "7.34.0" },
					"outputs": {}
				},
				{
					"urn": "urn:pulumi:test::proj::aws:s3/bucket:Bucket::my-bucket",
					"type": "aws:s3/bucket:Bucket",
					"custom": true,
					"provider": "urn:pulumi:test::proj::pulumi:providers:aws::my-aws::fake-uuid",
					"inputs": {},
					"outputs": {}
				}
			]
		}
	}`

	// Digest has force_destroy=true for this bucket.
	digest := &ModuleMap{
		RootResources: []ModuleResource{{
			Mode:             "managed",
			TranslatedURN:    "urn:pulumi:test::proj::aws:s3/bucket:Bucket::my-bucket",
			TerraformAddress: "aws_s3_bucket.my_bucket",
			Attributes:       map[string]interface{}{"force_destroy": true},
		}},
	}

	patched, result, err := PatchState([]byte(stateJSON), digest, ff, nil,
		map[string]string{"aws_s3_bucket.my_bucket": "my-bucket"}, nil, "")
	require.NoError(t, err)

	// Falsy default suppressed (counter incremented) but digest value patched.
	assert.Equal(t, 1, result.SkippedFalsySuppressed, "falsy default should be counted as suppressed")
	assert.Equal(t, 1, result.FieldsFromDigest, "digest value should still be patched")
	assert.Equal(t, 0, result.FieldsFromDefaults, "falsy default should not be used as fallback")

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	deployment := patchedState["deployment"].(map[string]interface{})
	resources := deployment["resources"].([]interface{})

	bucket := resources[1].(map[string]interface{})
	bucketInputs := bucket["inputs"].(map[string]interface{})
	assert.Equal(t, true, bucketInputs["forceDestroy"], "digest value true should be patched")
}

func TestPatchState_FalsyDefaultSuppression_DigestMatchesDefault_Skipped(t *testing.T) {
	t.Parallel()

	// Field has falsy default (false) and digest also has false (TF SDK stored default).
	// Both should be skipped — patching false would cause the same phantom diff.
	ff := fieldsFileFromJSON(t, `{
		"falsyDefaultSuppression": {
			"aws": "7.27.0"
		},
		"fields": {
			"aws:ec2/securityGroup:SecurityGroup": {
				"revokeRulesOnDelete": { "default": false }
			}
		}
	}`)

	stateJSON := `{
		"version": 3,
		"deployment": {
			"resources": [
				{
					"urn": "urn:pulumi:test::proj::pulumi:providers:aws::my-aws",
					"type": "pulumi:providers:aws",
					"custom": true,
					"inputs": { "version": "7.34.0" },
					"outputs": {}
				},
				{
					"urn": "urn:pulumi:test::proj::aws:ec2/securityGroup:SecurityGroup::my-sg",
					"type": "aws:ec2/securityGroup:SecurityGroup",
					"custom": true,
					"provider": "urn:pulumi:test::proj::pulumi:providers:aws::my-aws::fake-uuid",
					"inputs": {},
					"outputs": {}
				}
			]
		}
	}`

	// Digest has revoke_rules_on_delete=false (TF SDK default stored in state).
	digest := &ModuleMap{
		RootResources: []ModuleResource{{
			Mode:             "managed",
			TranslatedURN:    "urn:pulumi:test::proj::aws:ec2/securityGroup:SecurityGroup::my-sg",
			TerraformAddress: "aws_security_group.my_sg",
			Attributes:       map[string]interface{}{"revoke_rules_on_delete": false},
		}},
	}

	patched, result, err := PatchState([]byte(stateJSON), digest, ff, nil,
		map[string]string{"aws_security_group.my_sg": "my-sg"}, nil, "")
	require.NoError(t, err)

	assert.Equal(t, 1, result.SkippedFalsySuppressed)
	assert.Equal(t, 0, result.FieldsFromDigest, "digest value matching falsy default should be skipped")
	assert.Equal(t, 0, result.FieldsFromDefaults)

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	deployment := patchedState["deployment"].(map[string]interface{})
	resources := deployment["resources"].([]interface{})

	sg := resources[1].(map[string]interface{})
	sgInputs := sg["inputs"].(map[string]interface{})
	assert.NotContains(t, sgInputs, "revokeRulesOnDelete", "falsy digest value should not be patched")
}

func TestPatchState_FalsyDefaultSuppression_NumericDigestMatchesDefault_Skipped(t *testing.T) {
	t.Parallel()

	ff := fieldsFileFromJSON(t, `{
		"falsyDefaultSuppression": {
			"aws": "7.27.0"
		},
		"fields": {
			"aws:secretsmanager/secret:Secret": {
				"recoveryWindowInDays": { "default": 0 }
			}
		}
	}`)

	stateJSON := `{
		"version": 3,
		"deployment": {
			"resources": [
				{
					"urn": "urn:pulumi:test::proj::pulumi:providers:aws::my-aws",
					"type": "pulumi:providers:aws",
					"custom": true,
					"inputs": { "version": "7.34.0" },
					"outputs": {}
				},
				{
					"urn": "urn:pulumi:test::proj::aws:secretsmanager/secret:Secret::my-secret",
					"type": "aws:secretsmanager/secret:Secret",
					"custom": true,
					"provider": "urn:pulumi:test::proj::pulumi:providers:aws::my-aws::fake-uuid",
					"inputs": {},
					"outputs": {}
				}
			]
		}
	}`

	digestJSON := `{
		"rootResources": [
			{
				"mode": "managed",
				"translatedUrn": "urn:pulumi:test::proj::aws:secretsmanager/secret:Secret::my-secret",
				"terraformAddress": "aws_secretsmanager_secret.my_secret",
				"importId": "",
				"attributes": { "recovery_window_in_days": 0 }
			}
		]
	}`
	var digest ModuleMap
	dec := json.NewDecoder(strings.NewReader(digestJSON))
	dec.UseNumber()
	require.NoError(t, dec.Decode(&digest))

	patched, result, err := PatchState([]byte(stateJSON), &digest, ff, nil,
		map[string]string{"aws_secretsmanager_secret.my_secret": "my-secret"}, nil, "")
	require.NoError(t, err)

	assert.Equal(t, 1, result.SkippedFalsySuppressed)
	assert.Equal(t, 0, result.FieldsFromDigest, "digest value matching falsy default should be skipped")
	assert.Equal(t, 0, result.FieldsFromDefaults)

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	deployment := patchedState["deployment"].(map[string]interface{})
	resources := deployment["resources"].([]interface{})

	secret := resources[1].(map[string]interface{})
	secretInputs := secret["inputs"].(map[string]interface{})
	assert.NotContains(t, secretInputs, "recoveryWindowInDays", "falsy digest value should not be patched")
}

func TestPatchState_FalsyDefaultSuppression_NoDigest_NoFallback(t *testing.T) {
	t.Parallel()

	// Field has falsy default, no digest match. Default should NOT be used as fallback.
	ff := fieldsFileFromJSON(t, `{
		"falsyDefaultSuppression": {
			"aws": "7.27.0"
		},
		"fields": {
			"aws:s3/bucket:Bucket": {
				"forceDestroy": { "default": false }
			}
		}
	}`)

	stateJSON := `{
		"version": 3,
		"deployment": {
			"resources": [
				{
					"urn": "urn:pulumi:test::proj::pulumi:providers:aws::my-aws",
					"type": "pulumi:providers:aws",
					"custom": true,
					"inputs": { "version": "7.34.0" },
					"outputs": {}
				},
				{
					"urn": "urn:pulumi:test::proj::aws:s3/bucket:Bucket::my-bucket",
					"type": "aws:s3/bucket:Bucket",
					"custom": true,
					"provider": "urn:pulumi:test::proj::pulumi:providers:aws::my-aws::fake-uuid",
					"inputs": {},
					"outputs": {}
				}
			]
		}
	}`

	// No digest match — empty digest.
	digest := &ModuleMap{}

	patched, result, err := PatchState([]byte(stateJSON), digest, ff, nil, nil, nil, "")
	require.NoError(t, err)

	assert.Equal(t, 1, result.SkippedFalsySuppressed)
	assert.Equal(t, 0, result.FieldsFromDefaults, "falsy default should not be used as fallback")
	assert.Equal(t, 0, result.FieldsFromDigest)

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	deployment := patchedState["deployment"].(map[string]interface{})
	resources := deployment["resources"].([]interface{})

	bucket := resources[1].(map[string]interface{})
	bucketInputs := bucket["inputs"].(map[string]interface{})
	assert.NotContains(t, bucketInputs, "forceDestroy", "falsy default should not be patched")
}

// Suppress unused import warnings.
var _ = info.FileAsset
var _ = resource.ZIPArchive
