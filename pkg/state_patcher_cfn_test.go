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
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStateLogicalIDsByType(t *testing.T) {
	t.Parallel()
	state := []byte(`{"deployment":{"resources":[
		{"urn":"urn:pulumi:dev::p::pulumi:pulumi:Stack::p-dev","type":"pulumi:pulumi:Stack","custom":false},
		{"urn":"urn:pulumi:dev::p::aws:lambda/function:Function::core-authlambda5AE8A89F","type":"aws:lambda/function:Function","custom":true},
		{"urn":"urn:pulumi:dev::p::aws:lambda/function:Function::core-lambdafunction841552AF","type":"aws:lambda/function:Function","custom":true},
		{"urn":"urn:pulumi:dev::p::aws:s3/bucket:Bucket::core-imagesbucketD8E2A22E","type":"aws:s3/bucket:Bucket","custom":true}
	]}}`)

	ids, err := StateLogicalIDsByType(state, "aws:lambda/function:Function")
	require.NoError(t, err)
	require.Equal(t, map[string]bool{
		"authlambda5AE8A89F":     true,
		"lambdafunction841552AF": true,
	}, ids)
	// A function present in a digest but absent from state is not returned, so
	// patch-state cfn won't download its code.
	require.False(t, ids["migrationproviderframeworkonEvent"])
}

func TestPatchStateFromCFN_DefaultPatch(t *testing.T) {
	t.Parallel()

	state := map[string]interface{}{
		"version": 3,
		"deployment": map[string]interface{}{
			"resources": []interface{}{
				map[string]interface{}{
					"urn":    "urn:pulumi:dev::proj::aws:secretsmanager/secret:Secret::core-mysecret",
					"type":   "aws:secretsmanager/secret:Secret",
					"custom": true,
					"id":     "arn:aws:secretsmanager:us-east-1:123:secret:mysecret",
					"inputs": map[string]interface{}{
						"name": "mysecret",
					},
					"outputs": map[string]interface{}{
						"name": "mysecret",
					},
				},
			},
		},
	}
	stateData, err := json.Marshal(state)
	require.NoError(t, err)

	// Digest has no recoveryWindowInDays attribute at all for this logical ID.
	nameMap := map[string]*ModuleResource{
		"mysecret": {
			TerraformAddress: "mysecret",
			Attributes:       map[string]interface{}{},
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

	patched, result, err := PatchStateFromCFN(stateData, nameMap, fields, nil, "")
	require.NoError(t, err)
	assert.Equal(t, 1, result.Patched)
	assert.Equal(t, 1, result.FieldsFromDefaults)
	assert.Equal(t, 0, result.FieldsFromDigest)

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	r := resources[0].(map[string]interface{})
	inputs := r["inputs"].(map[string]interface{})
	assert.Equal(t, float64(30), inputs["recoveryWindowInDays"])
}

func TestPatchStateFromCFN_DigestAttributePatch(t *testing.T) {
	t.Parallel()

	state := map[string]interface{}{
		"version": 3,
		"deployment": map[string]interface{}{
			"resources": []interface{}{
				map[string]interface{}{
					"urn":    "urn:pulumi:dev::proj::aws:secretsmanager/secret:Secret::core-mysecret",
					"type":   "aws:secretsmanager/secret:Secret",
					"custom": true,
					"id":     "arn:aws:secretsmanager:us-east-1:123:secret:mysecret",
					"inputs": map[string]interface{}{
						"name": "mysecret",
					},
					"outputs": map[string]interface{}{
						"name": "mysecret",
					},
				},
			},
		},
	}
	stateData, err := json.Marshal(state)
	require.NoError(t, err)

	// Digest DOES carry a not_read field value for this logical ID.
	nameMap := map[string]*ModuleResource{
		"mysecret": {
			TerraformAddress: "mysecret",
			Attributes: map[string]interface{}{
				"recoveryWindowInDays": float64(7),
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

	patched, result, err := PatchStateFromCFN(stateData, nameMap, fields, nil, "")
	require.NoError(t, err)
	assert.Equal(t, 1, result.Patched)
	assert.Equal(t, 1, result.FieldsFromDigest)
	assert.Equal(t, 0, result.FieldsFromDefaults)

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	r := resources[0].(map[string]interface{})
	inputs := r["inputs"].(map[string]interface{})
	assert.Equal(t, float64(7), inputs["recoveryWindowInDays"])
}

func TestPatchStateFromCFN_LocalZipAssetPatch(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "fn.zip")
	func() {
		f, err := os.Create(zipPath)
		require.NoError(t, err)
		defer f.Close()
		zw := zip.NewWriter(f)
		w, err := zw.Create("index.js")
		require.NoError(t, err)
		_, err = w.Write([]byte("exports.handler = async () => {};"))
		require.NoError(t, err)
		require.NoError(t, zw.Close())
	}()

	state := map[string]interface{}{
		"version": 3,
		"deployment": map[string]interface{}{
			"resources": []interface{}{
				map[string]interface{}{
					"urn":    "urn:pulumi:dev::proj::aws:lambda/function:Function::core-myfunction",
					"type":   "aws:lambda/function:Function",
					"custom": true,
					"id":     "myfunction",
					"inputs": map[string]interface{}{
						"name": "myfunction",
					},
					"outputs": map[string]interface{}{
						"name": "myfunction",
					},
				},
			},
		},
	}
	stateData, err := json.Marshal(state)
	require.NoError(t, err)

	kind := 2   // FileArchive (bridge AssetTranslationKind)
	format := 3 // ZIPArchive
	nameMap := map[string]*ModuleResource{
		"myfunction": {
			TerraformAddress: "myfunction",
			Attributes: map[string]interface{}{
				"code": "fn.zip",
			},
		},
	}

	fields := &FieldsFile{
		Fields: map[string]FieldCategory{
			"function:Function": {
				NotRead: map[string]FieldInfo{
					"code": {
						Asset:         "FileArchive",
						AssetKind:     &kind,
						ArchiveFormat: &format,
						HashField:     "source_code_hash",
					},
				},
			},
		},
	}

	patched, result, err := PatchStateFromCFN(stateData, nameMap, fields, nil, tmpDir)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Patched)
	assert.Equal(t, 1, result.FieldsFromDigest)

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	r := resources[0].(map[string]interface{})
	inputs := r["inputs"].(map[string]interface{})
	code, ok := inputs["code"].(map[string]interface{})
	require.True(t, ok, "expected code to be an archive sentinel map")
	assert.Equal(t, archiveSig, code[sigKey])
	assert.NotEmpty(t, code["hash"])
}

func TestPatchStateFromCFN_NoMatchLeftUntouched(t *testing.T) {
	t.Parallel()

	state := map[string]interface{}{
		"version": 3,
		"deployment": map[string]interface{}{
			"resources": []interface{}{
				map[string]interface{}{
					"urn":    "urn:pulumi:dev::proj::aws:secretsmanager/secret:Secret::core-orphan",
					"type":   "aws:secretsmanager/secret:Secret",
					"custom": true,
					"id":     "arn:aws:secretsmanager:us-east-1:123:secret:orphan",
					"inputs": map[string]interface{}{
						"name": "orphan",
					},
					"outputs": map[string]interface{}{
						"name": "orphan",
					},
				},
			},
		},
	}
	stateData, err := json.Marshal(state)
	require.NoError(t, err)

	// No digest entry for "orphan" logical ID, and the field has no default
	// to fall back to — so nothing should be patched at all.
	nameMap := map[string]*ModuleResource{}

	fields := &FieldsFile{
		Fields: map[string]FieldCategory{
			"secret:Secret": {
				NotRead: map[string]FieldInfo{
					"forceOverwriteReplicaSecret": {},
				},
			},
		},
	}

	patched, result, err := PatchStateFromCFN(stateData, nameMap, fields, nil, "")
	require.NoError(t, err)
	assert.Equal(t, 0, result.Patched)
	assert.Equal(t, 1, result.NoMatch)

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	r := resources[0].(map[string]interface{})
	inputs := r["inputs"].(map[string]interface{})
	_, present := inputs["forceOverwriteReplicaSecret"]
	assert.False(t, present, "field should be left untouched with no digest match and no default")
}
