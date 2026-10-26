// Copyright 2026, Pulumi Corporation.
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
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeArchiveScenarioZip(t *testing.T, path string, content []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	header := &zip.FileHeader{Name: "bin/handler", Method: zip.Deflate}
	header.SetMode(0o755)
	entry, err := writer.CreateHeader(header)
	require.NoError(t, err)
	_, err = entry.Write(content)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.NoError(t, os.WriteFile(path, buffer.Bytes(), 0o600))
	return buffer.Bytes()
}

func TestArchiveScenarioA8ExplicitZipWinsOverSiblingDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "function.zip")
	contents := writeArchiveScenarioZip(t, path, []byte("deployed"))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "function"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "function", "different.txt"), []byte("stale"), 0o600))
	archive, err := buildAssetSentinel(path, "FileArchive")
	require.NoError(t, err)
	assert.Equal(t, path, archive["path"])
	digest := sha256.Sum256(contents)
	assert.Equal(t, hex.EncodeToString(digest[:]), archive["hash"])
}

func TestArchiveScenarioA11TerraformPreservesBinaryZip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "function.zip")
	contents := writeArchiveScenarioZip(t, path, []byte{0, 255, 254, 128, 1, 2})
	state := map[string]interface{}{"version": 3, "deployment": map[string]interface{}{"resources": []interface{}{
		map[string]interface{}{"urn": "urn:pulumi:dev::p::aws:lambda/function:Function::fn", "type": "aws:lambda/function:Function", "custom": true, "id": "fn", "inputs": map[string]interface{}{}, "outputs": map[string]interface{}{}},
	}}}
	raw, err := json.Marshal(state)
	require.NoError(t, err)
	digest := &ModuleMap{RootResources: []ModuleResource{{Mode: "managed", TerraformAddress: "aws_lambda_function.fn", TranslatedURN: "urn:pulumi:dev::p::aws:lambda/function:Function::fn", Attributes: map[string]interface{}{"filename": "function.zip"}}}}
	fields := &FieldsFile{Fields: map[string]FieldCategory{"aws:lambda/function:Function": {NotRead: map[string]FieldInfo{"code": {Asset: "FileArchive"}}}}}
	patched, _, err := PatchState(raw, digest, fields, nil, map[string]string{"aws_lambda_function.fn": "fn"}, nil, dir)
	require.NoError(t, err)
	var result map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &result))
	resources := result["deployment"].(map[string]interface{})["resources"].([]interface{})
	resource := resources[0].(map[string]interface{})
	archive := resource["inputs"].(map[string]interface{})["code"].(map[string]interface{})
	assert.Equal(t, path, archive["path"])
	assert.NotContains(t, archive, "assets", "binary ZIP entries must not be converted to StringAsset text")
	hash := sha256.Sum256(contents)
	assert.Equal(t, hex.EncodeToString(hash[:]), archive["hash"])
	assert.Equal(t, archive, resource["outputs"].(map[string]interface{})["code"])
	actual, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, contents, actual)
}

func TestArchiveScenarioA13AbsolutePathIsNotRebased(t *testing.T) {
	source := t.TempDir()
	path := filepath.Join(t.TempDir(), "function.zip")
	writeArchiveScenarioZip(t, path, []byte("deployed"))
	inputs := map[string]interface{}{}
	outputs := map[string]interface{}{}
	_, err := patchResourceFields([]patchFieldDescriptor{{PulumiName: "code", TFName: "filename", AssetType: "FileArchive"}}, inputs, outputs,
		&ModuleResource{Attributes: map[string]interface{}{"filename": path}}, nil, source)
	require.NoError(t, err)
	archive, ok := inputs["code"].(map[string]interface{})
	require.True(t, ok, "archive conversion must not silently leave a plain path string")
	assert.Equal(t, path, archive["path"])
}

func TestArchiveScenarioA2RejectsStaleLocalZip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "code.zip")
	deployed := writeArchiveScenarioZip(t, path, []byte("deployed"))
	expected := sha256.Sum256(deployed)
	writeArchiveScenarioZip(t, path, []byte("stale"))
	inputs := map[string]interface{}{}
	outputs := map[string]interface{}{"codeSha256": base64.StdEncoding.EncodeToString(expected[:])}
	_, err := patchResourceFields([]patchFieldDescriptor{{PulumiName: "code", TFName: "filename", AssetType: "FileArchive"}}, inputs, outputs,
		&ModuleResource{Attributes: map[string]interface{}{"filename": "code.zip"}}, nil, dir)
	require.ErrorContains(t, err, "does not match deployed codeSha256")
	assert.Empty(t, inputs)
	assert.NotContains(t, outputs, "code")
}

func TestArchiveScenarioS4RepairsVerifiedArchiveAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "code.zip")
	writeArchiveScenarioZip(t, path, []byte("stale"))
	old, err := buildAssetSentinel(path, "FileArchive")
	require.NoError(t, err)
	deployed := writeArchiveScenarioZip(t, path, []byte("deployed"))
	expected := sha256.Sum256(deployed)
	inputs := map[string]interface{}{"code": old}
	outputs := map[string]interface{}{"code": old, "codeSha256": base64.StdEncoding.EncodeToString(expected[:])}
	fields := []patchFieldDescriptor{{PulumiName: "code", TFName: "filename", AssetType: "FileArchive"}}
	digest := &ModuleResource{Attributes: map[string]interface{}{"filename": "code.zip"}}
	result, err := patchResourceFields(fields, inputs, outputs, digest, nil, dir)
	require.NoError(t, err)
	assert.True(t, result.patched)
	assert.Equal(t, hex.EncodeToString(expected[:]), inputs["code"].(map[string]interface{})["hash"])
	assert.Equal(t, inputs["code"], outputs["code"])
	result, err = patchResourceFields(fields, inputs, outputs, digest, nil, dir)
	require.NoError(t, err)
	assert.False(t, result.patched)
	outputs["code"] = old
	result, err = patchResourceFields(fields, inputs, outputs, digest, nil, dir)
	require.NoError(t, err)
	assert.True(t, result.patched, "S5: correct inputs must not hide stale outputs")
	assert.Equal(t, inputs["code"], outputs["code"])
}

func TestArchiveScenarioA7ProgramArchiveOverridesStaleTerraformPath(t *testing.T) {
	dir := t.TempDir()
	writeArchiveScenarioZip(t, filepath.Join(dir, "source.zip"), []byte("stale"))
	deployed := writeArchiveScenarioZip(t, filepath.Join(dir, "program.zip"), []byte("deployed"))
	hash := sha256.Sum256(deployed)
	urn := "urn:pulumi:dev::p::aws:lambda/function:Function::fn"
	state := []byte(fmt.Sprintf(`{"version":3,"deployment":{"resources":[{"urn":%q,"type":"aws:lambda/function:Function","custom":true,"id":"fn","inputs":{},"outputs":{"codeSha256":%q}}]}}`, urn, base64.StdEncoding.EncodeToString(hash[:])))
	digest := &ModuleMap{RootResources: []ModuleResource{{Mode: "managed", TerraformAddress: "aws_lambda_function.fn", TranslatedURN: urn, Attributes: map[string]interface{}{"filename": "source.zip"}}}}
	fields := &FieldsFile{Fields: map[string]FieldCategory{"aws:lambda/function:Function": {NotRead: map[string]FieldInfo{"code": {Asset: "FileArchive"}}}}}
	preview := &PreviewDigest{Steps: []PreviewStep{{URN: urn, NewState: map[string]interface{}{"type": "aws:lambda/function:Function", "inputs": map[string]interface{}{"code": map[string]interface{}{sigKey: archiveSig, "path": "program.zip"}}}}}}
	preview.Steps = append(preview.Steps, PreviewStep{URN: "unrelated", NewState: map[string]interface{}{"type": "aws:lambda/function:Function", "inputs": map[string]interface{}{"code": map[string]interface{}{sigKey: archiveSig, "assets": map[string]interface{}{}}}}})
	patched, _, err := PatchStateWithArchiveOptions(state, digest, fields, nil, map[string]string{"aws_lambda_function.fn": "fn"}, nil, dir, ArchiveOptions{Preview: preview, ProjectDir: dir})
	require.NoError(t, err)
	assert.Contains(t, string(patched), filepath.Join(dir, "program.zip"))
	assert.NotContains(t, string(patched), "source.zip")
	assert.Equal(t, "source.zip", digest.RootResources[0].Attributes["filename"], "do not mutate the caller's digest")
}

func TestArchiveScenarioA10EmbeddedProgramArchiveIsExplicitlyRejected(t *testing.T) {
	preview := &PreviewDigest{Steps: []PreviewStep{{URN: "fn", NewState: map[string]interface{}{"type": "aws:lambda/function:Function", "inputs": map[string]interface{}{"code": map[string]interface{}{sigKey: archiveSig, "assets": map[string]interface{}{}}}}}}}
	_, err := preview.LambdaArchivePaths(t.TempDir())
	require.ErrorContains(t, err, "non-file archive")
}

func TestArchiveScenarioS3UnverifiedReplacementIsRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "code.zip")
	writeArchiveScenarioZip(t, path, []byte("old"))
	old, err := buildAssetSentinel(path, "FileArchive")
	require.NoError(t, err)
	writeArchiveScenarioZip(t, path, []byte("new"))
	inputs := map[string]interface{}{"code": old}
	_, err = patchResourceFields([]patchFieldDescriptor{{PulumiName: "code", TFName: "filename", AssetType: "FileArchive"}}, inputs, map[string]interface{}{"code": old},
		&ModuleResource{Attributes: map[string]interface{}{"filename": "code.zip"}}, nil, dir)
	require.ErrorContains(t, err, "refusing an unverified archive replacement")
	assert.Equal(t, old, inputs["code"])
}

func TestArchiveScenarioA4MalformedArchiveDoesNotBecomeStringInput(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "code.zip"), []byte("not a ZIP"), 0o600))
	inputs := map[string]interface{}{}
	_, err := patchResourceFields([]patchFieldDescriptor{{PulumiName: "code", TFName: "filename", AssetType: "FileArchive"}}, inputs, map[string]interface{}{},
		&ModuleResource{Attributes: map[string]interface{}{"filename": "code.zip"}}, nil, dir)
	require.ErrorContains(t, err, "cannot reconstruct code")
	assert.Empty(t, inputs)
}

func archiveLambdaServer(t *testing.T, body []byte, hash string, status int) {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/functions/") {
			w.Header().Set("Content-Type", "application/json")
			response := map[string]interface{}{"Code": map[string]string{"Location": server.URL + "/archive"}, "Configuration": map[string]string{"CodeSha256": hash}}
			assert.NoError(t, json.NewEncoder(w).Encode(response))
			return
		}
		w.WriteHeader(status)
		_, err := w.Write(body)
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	t.Setenv("AWS_ENDPOINT_URL_LAMBDA", server.URL)
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_REGION", "us-west-2")
}

func TestArchiveScenarioA3DownloadPreservesBinaryZip(t *testing.T) {
	dir := t.TempDir()
	contents := writeArchiveScenarioZip(t, filepath.Join(t.TempDir(), "fixture.zip"), []byte{0, 255, 254, 128})
	hash := sha256.Sum256(contents)
	archiveLambdaServer(t, contents, base64.StdEncoding.EncodeToString(hash[:]), http.StatusOK)
	archive, err := downloadLambdaCodeAsArchive("fn", "arn:aws:lambda:us-west-2:123456789012:function:fn", dir)
	require.NoError(t, err)
	path := archive["path"].(string)
	assert.Equal(t, filepath.Join(dir, ".pulumi-import-artifacts", hex.EncodeToString(hash[:])+".zip"), path)
	actual, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, contents, actual)
	second, err := downloadLambdaCodeAsArchive("fn", "arn:aws:lambda:us-west-2:123456789012:function:fn", dir)
	require.NoError(t, err)
	assert.Equal(t, archive, second)
}

func TestArchiveScenarioV4DownloadFailurePreservesDestination(t *testing.T) {
	contents := writeArchiveScenarioZip(t, filepath.Join(t.TempDir(), "fixture.zip"), []byte("deployed"))
	wrongHash := sha256.Sum256([]byte("different"))
	for _, status := range []int{http.StatusOK, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			archiveLambdaServer(t, contents, base64.StdEncoding.EncodeToString(wrongHash[:]), status)
			path := filepath.Join(t.TempDir(), "keep.zip")
			require.NoError(t, os.WriteFile(path, []byte("keep original artifact"), 0o600))
			err := DownloadLambdaCodeToFile(t.Context(), "fn", "", "us-west-2", path)
			require.Error(t, err)
			actual, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			assert.Equal(t, "keep original artifact", string(actual))
		})
	}
}

func TestArchiveScenarioA5UnavailableDownloadLeavesInputUntouched(t *testing.T) {
	dir := t.TempDir()
	hash := sha256.Sum256([]byte("deployed"))
	archiveLambdaServer(t, []byte("unavailable"), base64.StdEncoding.EncodeToString(hash[:]), http.StatusForbidden)
	inputs := map[string]interface{}{}
	_, err := patchResourceFields([]patchFieldDescriptor{{PulumiName: "code", TFName: "filename", AssetType: "FileArchive"}}, inputs, map[string]interface{}{},
		&ModuleResource{Attributes: map[string]interface{}{"filename": "missing.zip", "function_name": "fn"}}, nil, dir)
	require.ErrorContains(t, err, "HTTP 403")
	assert.Empty(t, inputs)
}

func TestArchiveScenarioP5BinaryFileAssetIsUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "object.bin")
	contents := []byte{0, 255, 254, 128}
	require.NoError(t, os.WriteFile(path, contents, 0o600))
	asset, err := buildAssetSentinel(path, "FileAsset")
	require.NoError(t, err)
	hash := sha256.Sum256(contents)
	assert.Equal(t, assetSig, asset[sigKey])
	assert.Equal(t, path, asset["path"])
	assert.Equal(t, hex.EncodeToString(hash[:]), asset["hash"])
}

func TestArchiveScenarioA9ExplicitDirectoryRemainsDirectory(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.js"), []byte("code"), 0o600))
	archive, err := buildAssetSentinel(dir, "FileArchive")
	require.NoError(t, err)
	assert.Equal(t, dir, archive["path"])
	assert.NotContains(t, archive, "assets")
}

func TestArchiveScenarioA13ZipContentWithJarExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "handler.jar")
	contents := writeArchiveScenarioZip(t, path, []byte("compiled code"))
	archive, err := buildAssetSentinel(path, "FileArchive")
	require.NoError(t, err)
	hash := sha256.Sum256(contents)
	assert.Equal(t, path, archive["path"])
	assert.Equal(t, hex.EncodeToString(hash[:]), archive["hash"])
}

func TestArchiveScenarioA12ZipEntriesAreNotExtractedOrCollapsed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "duplicate.zip")
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, body := range []string{"first", "second"} {
		entry, err := writer.Create("same-name")
		require.NoError(t, err)
		_, err = entry.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	require.NoError(t, os.WriteFile(path, buffer.Bytes(), 0o600))
	archive, err := buildAssetSentinel(path, "FileArchive")
	require.NoError(t, err)
	hash := sha256.Sum256(buffer.Bytes())
	assert.Equal(t, hex.EncodeToString(hash[:]), archive["hash"])
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

func TestArchiveScenarioP4ImageLambdaDoesNotReconstructZip(t *testing.T) {
	urn := "urn:pulumi:dev::p::aws:lambda/function:Function::fn"
	state := []byte(fmt.Sprintf(`{"version":3,"deployment":{"resources":[{"urn":%q,"type":"aws:lambda/function:Function","custom":true,"id":"fn","inputs":{"packageType":"Image","imageUri":"image:tag"},"outputs":{"packageType":"Image","imageUri":"image:tag"}}]}}`, urn))
	digest := &ModuleMap{RootResources: []ModuleResource{{Mode: "managed", TerraformAddress: "aws_lambda_function.fn", TranslatedURN: urn, Attributes: map[string]interface{}{"package_type": "Image", "image_uri": "image:tag"}}}}
	fields := &FieldsFile{Fields: map[string]FieldCategory{"aws:lambda/function:Function": {NotRead: map[string]FieldInfo{"code": {Asset: "FileArchive"}}}}}
	preview := &PreviewDigest{Steps: []PreviewStep{{URN: urn, NewState: map[string]interface{}{"type": "aws:lambda/function:Function", "inputs": map[string]interface{}{"packageType": "Image", "imageUri": "image:tag"}}}}}
	patched, result, err := PatchStateWithArchiveOptions(state, digest, fields, nil, map[string]string{"aws_lambda_function.fn": "fn"}, nil, t.TempDir(), ArchiveOptions{Preview: preview})
	require.NoError(t, err)
	assert.Zero(t, result.Patched)
	assert.JSONEq(t, string(state), string(patched))
}

func TestArchiveScenarioS5OutputOnlyRepairValidatesDelta(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "code.zip")
	writeArchiveScenarioZip(t, path, []byte("deployed"))
	archive, err := buildAssetSentinel(path, "FileArchive")
	require.NoError(t, err)
	kind := 2
	format := 3
	inputs := map[string]interface{}{"code": archive}
	outputs := map[string]interface{}{rawStateDeltaKey: map[string]interface{}{"obj": map[string]interface{}{"ps": map[string]interface{}{}}}}
	fields := []patchFieldDescriptor{{PulumiName: "code", TFName: "filename", AssetType: "FileArchive", AssetKind: &kind, ArchiveFormat: &format, HashField: "source_code_hash"}}
	result, _, patchedOutputs, err := patchAndValidateResource("urn:pulumi:dev::p::aws:lambda/function:Function::fn", "fn", fields, inputs, outputs,
		&ModuleResource{Attributes: map[string]interface{}{"filename": "code.zip"}}, nil, dir)
	require.NoError(t, err)
	assert.True(t, result.patched, "an output-only state change must be reported")
	assert.True(t, result.deltaValidated, "output-only archive repair must run Recover validation")
	assert.Equal(t, archive, patchedOutputs["code"])
}

func TestArchiveScenarioS5OtherFieldPatchCannotOverwriteUnchangedOutput(t *testing.T) {
	inputs := map[string]interface{}{"kept": "current"}
	outputs := map[string]interface{}{"kept": "current"}
	fields := []patchFieldDescriptor{
		{PulumiName: "missing", HasDefault: true, Default: "default"},
		{PulumiName: "kept", TFName: "kept"},
	}
	_, err := patchResourceFields(fields, inputs, outputs, &ModuleResource{Attributes: map[string]interface{}{"kept": "old"}}, nil, "")
	require.NoError(t, err)
	assert.Equal(t, "current", inputs["kept"])
	assert.Equal(t, "current", outputs["kept"])
}
