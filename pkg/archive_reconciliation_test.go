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
	"time"

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
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%v", reverse), func(t *testing.T) {
			inputs := map[string]interface{}{"kept": "current"}
			outputs := map[string]interface{}{"kept": "current"}
			fields := []patchFieldDescriptor{
				{PulumiName: "missing", HasDefault: true, Default: "default"},
				{PulumiName: "kept", TFName: "kept"},
			}
			if reverse {
				fields[0], fields[1] = fields[1], fields[0]
			}
			result, err := patchResourceFields(fields, inputs, outputs,
				&ModuleResource{Attributes: map[string]interface{}{"kept": "old"}}, nil, "")
			require.NoError(t, err)
			assert.True(t, result.patched)
			assert.Equal(t, "default", inputs["missing"])
			assert.Equal(t, "default", outputs["missing"])
			assert.Equal(t, "current", inputs["kept"])
			assert.Equal(t, "current", outputs["kept"])
		})
	}
}

func TestArchivePolicyEvidenceAdapter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "code.zip")
	deployed := writeArchiveScenarioZip(t, path, []byte("deployed"))
	hash := sha256.Sum256(deployed)
	encodedHash := base64.StdEncoding.EncodeToString(hash[:])
	wrongHash := sha256.Sum256([]byte("different artifact"))
	candidate, err := buildAssetSentinel(path, "FileArchive")
	require.NoError(t, err)
	stale := map[string]interface{}{sigKey: archiveSig, "path": "stale.zip", "hash": "stale"}
	fields := []patchFieldDescriptor{{PulumiName: "code", TFName: "filename", AssetType: "FileArchive"}}
	digest := &ModuleResource{Attributes: map[string]interface{}{"filename": "code.zip"}}

	for _, tc := range []struct {
		name     string
		hash     interface{}
		existing interface{}
		wantErr  string
	}{
		{name: "missing hash permits empty reconstruction"},
		{name: "missing hash rejects existing conflict", existing: stale, wantErr: "refusing an unverified archive replacement"},
		{name: "invalid base64", hash: "%%%", wantErr: "invalid deployed codeSha256"},
		{name: "wrong hash length", hash: base64.StdEncoding.EncodeToString([]byte("short")), wantErr: "invalid deployed codeSha256"},
		{name: "mismatching hash", hash: base64.StdEncoding.EncodeToString(wrongHash[:]), wantErr: "does not match deployed codeSha256"},
		{name: "matching hash repairs conflict", hash: encodedHash, existing: stale},
		{name: "secret-wrapped matching hash", hash: map[string]interface{}{sigKey: secretSig, "plaintext": fmt.Sprintf("%q", encodedHash)}, existing: stale},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inputs := map[string]interface{}{"kept": "input"}
			outputs := map[string]interface{}{"kept": "output"}
			if tc.hash != nil {
				outputs["codeSha256"] = tc.hash
			}
			if tc.existing != nil {
				inputs["code"] = tc.existing
				outputs["code"] = tc.existing
			}
			beforeInputs := deepCopyJSONValue(inputs)
			beforeOutputs := deepCopyJSONValue(outputs)
			result, err := patchResourceFields(fields, inputs, outputs, digest, nil, dir)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				assert.Equal(t, beforeInputs, inputs)
				assert.Equal(t, beforeOutputs, outputs)
				return
			}
			require.NoError(t, err)
			assert.True(t, result.patched)
			assert.Equal(t, candidate, inputs["code"])
			assert.Equal(t, candidate, outputs["code"])
			assert.Equal(t, "input", inputs["kept"])
			assert.Equal(t, "output", outputs["kept"])
			assert.Equal(t, "code.zip", digest.Attributes["filename"])
		})
	}
}

func TestArchivePolicyClassifiesInputAndOutputIndependently(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "code.zip")
	deployed := writeArchiveScenarioZip(t, path, []byte("deployed"))
	hash := sha256.Sum256(deployed)
	candidate, err := buildAssetSentinel(path, "FileArchive")
	require.NoError(t, err)
	stale := map[string]interface{}{sigKey: archiveSig, "path": path, "hash": "stale"}
	fields := []patchFieldDescriptor{{PulumiName: "code", TFName: "filename", AssetType: "FileArchive"}}
	digest := &ModuleResource{Attributes: map[string]interface{}{"filename": "code.zip"}}

	for _, tc := range []struct {
		name        string
		input       interface{}
		output      interface{}
		hasHash     bool
		wantErr     bool
		wantPatched bool
	}{
		{name: "unverified stale input", input: stale, output: candidate, wantErr: true},
		{name: "unverified stale output", input: candidate, output: stale, wantErr: true},
		{name: "verified stale input", input: stale, output: candidate, hasHash: true, wantPatched: true},
		{name: "verified stale output", input: candidate, output: stale, hasHash: true, wantPatched: true},
		{name: "verified matching pair", input: candidate, output: candidate, hasHash: true},
		{name: "unverified matching pair", input: candidate, output: candidate},
		{name: "unverified missing input", output: candidate, wantPatched: true},
		{name: "unverified missing output", input: candidate, wantPatched: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inputs := map[string]interface{}{"code": deepCopyJSONValue(tc.input)}
			outputs := map[string]interface{}{"code": deepCopyJSONValue(tc.output)}
			if tc.hasHash {
				outputs["codeSha256"] = base64.StdEncoding.EncodeToString(hash[:])
			}
			beforeInputs := deepCopyJSONValue(inputs)
			beforeOutputs := deepCopyJSONValue(outputs)
			result, err := patchResourceFields(fields, inputs, outputs, digest, nil, dir)
			if tc.wantErr {
				require.ErrorContains(t, err, "refusing an unverified archive replacement")
				assert.Equal(t, beforeInputs, inputs)
				assert.Equal(t, beforeOutputs, outputs)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantPatched, result.patched)
			assert.Equal(t, candidate, inputs["code"])
			assert.Equal(t, candidate, outputs["code"])
		})
	}
}

const archiveScenarioURN = "urn:pulumi:dev::p::aws:lambda/function:Function::fn"

func archiveScenarioState(t *testing.T, inputs, outputs map[string]interface{}) []byte {
	t.Helper()
	state, err := json.Marshal(map[string]interface{}{
		"version": 3,
		"deployment": map[string]interface{}{"resources": []interface{}{
			map[string]interface{}{"urn": archiveScenarioURN, "type": "aws:lambda/function:Function", "custom": true, "id": "fn", "inputs": inputs, "outputs": outputs},
		}},
	})
	require.NoError(t, err)
	return state
}

func archiveScenarioDigest(filename string) *ModuleMap {
	return &ModuleMap{RootResources: []ModuleResource{{
		Mode: "managed", TerraformAddress: "aws_lambda_function.fn", TranslatedURN: archiveScenarioURN,
		Attributes: map[string]interface{}{"filename": filename, "function_name": "fn"},
	}}}
}

func archiveScenarioFields() *FieldsFile {
	kind, format := 2, 3
	return &FieldsFile{Fields: map[string]FieldCategory{"aws:lambda/function:Function": {
		NotRead: map[string]FieldInfo{"code": {Asset: "FileArchive", AssetKind: &kind, ArchiveFormat: &format, HashField: "source_code_hash"}},
	}}}
}

func archiveScenarioPatchedResource(t *testing.T, state []byte) (map[string]interface{}, map[string]interface{}) {
	t.Helper()
	var deployment struct {
		Deployment struct {
			Resources []struct {
				Inputs  map[string]interface{}
				Outputs map[string]interface{}
			}
		}
	}
	require.NoError(t, json.Unmarshal(state, &deployment))
	require.Len(t, deployment.Deployment.Resources, 1)
	return deployment.Deployment.Resources[0].Inputs, deployment.Deployment.Resources[0].Outputs
}

func TestArchiveScenariosA1S1S6S7PatchStateShapes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		withDelta bool
		malformed bool
	}{
		{name: "missing-code-with-delta", withDelta: true},
		{name: "missing-code-without-delta"},
		{name: "malformed-code-with-delta", withDelta: true, malformed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			contents := writeArchiveScenarioZip(t, filepath.Join(dir, "code.zip"), []byte("deployed"))
			hash := sha256.Sum256(contents)
			inputs := map[string]interface{}{}
			outputs := map[string]interface{}{"codeSha256": base64.StdEncoding.EncodeToString(hash[:])}
			if tc.withDelta {
				outputs[rawStateDeltaKey] = map[string]interface{}{"obj": map[string]interface{}{"ps": map[string]interface{}{}}}
			}
			if tc.malformed {
				inputs["code"], outputs["code"] = "code.zip", "code.zip"
			}
			state := archiveScenarioState(t, inputs, outputs)
			digest := archiveScenarioDigest("code.zip")
			mappings := map[string]string{"aws_lambda_function.fn": "fn"}
			patched, result, err := PatchState(state, digest, archiveScenarioFields(), nil, mappings, nil, dir)
			require.NoError(t, err)
			assert.Equal(t, 1, result.Patched)
			newInputs, newOutputs := archiveScenarioPatchedResource(t, patched)
			want := map[string]interface{}{sigKey: archiveSig, "path": filepath.Join(dir, "code.zip"), "hash": hex.EncodeToString(hash[:])}
			assert.Equal(t, want, newInputs["code"])
			assert.Equal(t, want, newOutputs["code"])
			assert.Equal(t, outputs["codeSha256"], newOutputs["codeSha256"])
			assert.Zero(t, result.DeltaFailed)
			if tc.withDelta {
				assert.Equal(t, 1, result.DeltaValidated)
				delta := newOutputs[rawStateDeltaKey].(map[string]interface{})["obj"].(map[string]interface{})["ps"].(map[string]interface{})
				assert.Equal(t, map[string]interface{}{"asset": map[string]interface{}{"kind": float64(2), "archiveFormat": float64(3), "hashField": "source_code_hash"}}, delta["code"])
			} else {
				assert.Zero(t, result.DeltaValidated)
				assert.NotContains(t, newOutputs, rawStateDeltaKey, "do not invent a delta for the legacy path")
			}
			again, againResult, err := PatchState(patched, digest, archiveScenarioFields(), nil, mappings, nil, dir)
			require.NoError(t, err)
			assert.Zero(t, againResult.Patched, "S2: a second patch must be a no-op")
			assert.JSONEq(t, string(patched), string(again))
		})
	}
}

func TestArchiveScenariosA3A4RecoveryUsesDeployedZip(t *testing.T) {
	for _, name := range []string{"missing-local", "malformed-local"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			contents := writeArchiveScenarioZip(t, filepath.Join(t.TempDir(), "deployed.zip"), []byte{0, 255, 254, 128})
			hash := sha256.Sum256(contents)
			archiveLambdaServer(t, contents, base64.StdEncoding.EncodeToString(hash[:]), http.StatusOK)
			if name == "malformed-local" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "code.zip"), []byte("not a ZIP"), 0o600))
			}
			state := archiveScenarioState(t, map[string]interface{}{}, map[string]interface{}{"codeSha256": base64.StdEncoding.EncodeToString(hash[:])})
			patched, result, err := PatchState(state, archiveScenarioDigest("code.zip"), archiveScenarioFields(), nil, map[string]string{"aws_lambda_function.fn": "fn"}, nil, dir)
			require.NoError(t, err)
			assert.Equal(t, 1, result.Patched)
			inputs, outputs := archiveScenarioPatchedResource(t, patched)
			path := filepath.Join(dir, ".pulumi-import-artifacts", hex.EncodeToString(hash[:])+".zip")
			assert.Equal(t, path, inputs["code"].(map[string]interface{})["path"])
			assert.Equal(t, inputs["code"], outputs["code"])
			actual, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, contents, actual)
			if name == "malformed-local" {
				original, err := os.ReadFile(filepath.Join(dir, "code.zip"))
				require.NoError(t, err)
				assert.Equal(t, "not a ZIP", string(original), "recovery must not overwrite the source build file")
			}
		})
	}
}

func TestArchiveScenarioA6MissingHashIsReportedAsUnverified(t *testing.T) {
	dir := t.TempDir()
	writeArchiveScenarioZip(t, filepath.Join(dir, "code.zip"), []byte("local only"))
	log, err := os.CreateTemp(t.TempDir(), "stderr-")
	require.NoError(t, err)
	defer log.Close()
	previous := os.Stderr
	os.Stderr = log
	defer func() { os.Stderr = previous }()
	inputs, outputs := map[string]interface{}{}, map[string]interface{}{}
	_, err = patchResourceFields([]patchFieldDescriptor{{PulumiName: "code", TFName: "filename", AssetType: "FileArchive"}}, inputs, outputs,
		&ModuleResource{Attributes: map[string]interface{}{"filename": "code.zip"}}, nil, dir)
	require.NoError(t, err)
	os.Stderr = previous
	message, err := os.ReadFile(log.Name())
	require.NoError(t, err)
	assert.Contains(t, string(message), "has no deployed codeSha256; local archive reconstruction is unverified")
	assert.Equal(t, inputs["code"], outputs["code"])
	assert.NotContains(t, outputs, "codeSha256", "do not manufacture deployed evidence from a local file")
}

func TestArchiveScenariosA10S8ProgramEvidenceCannotHideMismatch(t *testing.T) {
	for _, tc := range []struct {
		name string
		code interface{}
		want string
	}{
		{"changed-program-bytes", map[string]interface{}{sigKey: archiveSig, "path": "program.zip"}, "does not match deployed codeSha256"},
		{"omitted-code", nil, "does not declare a local code archive"},
		{"embedded-text", map[string]interface{}{sigKey: archiveSig, "assets": map[string]interface{}{"index.js": map[string]interface{}{sigKey: assetSig, "text": "deployed"}}}, "non-file archive"},
		{"embedded-binary", map[string]interface{}{sigKey: archiveSig, "assets": map[string]interface{}{"handler": map[string]interface{}{sigKey: assetSig, "path": "handler.bin"}}}, "non-file archive"},
		{"nested-archive", map[string]interface{}{sigKey: archiveSig, "assets": map[string]interface{}{"nested": map[string]interface{}{sigKey: archiveSig, "path": "source.zip"}}}, "non-file archive"},
		{"unknown-code", "[unknown]", "not a known archive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			contents := writeArchiveScenarioZip(t, filepath.Join(dir, "source.zip"), []byte("deployed"))
			writeArchiveScenarioZip(t, filepath.Join(dir, "program.zip"), []byte("changed"))
			hash := sha256.Sum256(contents)
			state := archiveScenarioState(t, map[string]interface{}{}, map[string]interface{}{"codeSha256": base64.StdEncoding.EncodeToString(hash[:])})
			digest := archiveScenarioDigest("source.zip")
			preview := &PreviewDigest{Steps: []PreviewStep{{URN: archiveScenarioURN, NewState: map[string]interface{}{"type": "aws:lambda/function:Function", "inputs": map[string]interface{}{"code": tc.code}}}}}
			patched, _, err := PatchStateWithArchiveOptions(state, digest, archiveScenarioFields(), nil, map[string]string{"aws_lambda_function.fn": "fn"}, nil, dir, ArchiveOptions{Preview: preview, ProjectDir: dir})
			require.ErrorContains(t, err, tc.want)
			assert.Nil(t, patched, "a failed patch must not yield candidate state")
			assert.Equal(t, "source.zip", digest.RootResources[0].Attributes["filename"])
		})
	}
}

func TestArchiveScenarioA13SeparateProgramAndSourceDirectories(t *testing.T) {
	sourceDir, programDir := t.TempDir(), t.TempDir()
	writeArchiveScenarioZip(t, filepath.Join(sourceDir, "code.zip"), []byte("stale"))
	contents := writeArchiveScenarioZip(t, filepath.Join(programDir, "code.zip"), []byte("deployed"))
	hash := sha256.Sum256(contents)
	state := archiveScenarioState(t, map[string]interface{}{}, map[string]interface{}{"codeSha256": base64.StdEncoding.EncodeToString(hash[:])})
	preview := &PreviewDigest{Steps: []PreviewStep{{URN: archiveScenarioURN, NewState: map[string]interface{}{"type": "aws:lambda/function:Function", "inputs": map[string]interface{}{"code": map[string]interface{}{sigKey: archiveSig, "path": "code.zip"}}}}}}
	patched, _, err := PatchStateWithArchiveOptions(state, archiveScenarioDigest("code.zip"), archiveScenarioFields(), nil, map[string]string{"aws_lambda_function.fn": "fn"}, nil, sourceDir, ArchiveOptions{Preview: preview, ProjectDir: programDir})
	require.NoError(t, err)
	inputs, _ := archiveScenarioPatchedResource(t, patched)
	assert.Equal(t, filepath.Join(programDir, "code.zip"), inputs["code"].(map[string]interface{})["path"])
}

func TestArchiveScenarioA11PreservesUnicodeAndExecutableMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "code.zip")
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	header := &zip.FileHeader{Name: "bin/处理器", Method: zip.Deflate}
	header.SetMode(0o755)
	header.SetModTime(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	header.Comment = "deployment metadata"
	entry, err := writer.CreateHeader(header)
	require.NoError(t, err)
	payload := []byte{0, 255, 254, 128}
	_, err = entry.Write(payload)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.NoError(t, os.WriteFile(path, buffer.Bytes(), 0o600))
	archive, err := buildAssetSentinel(path, "FileArchive")
	require.NoError(t, err)
	actual, err := os.ReadFile(archive["path"].(string))
	require.NoError(t, err)
	assert.Equal(t, buffer.Bytes(), actual, "preserve the entire ZIP, including metadata")
	reader, err := zip.NewReader(bytes.NewReader(actual), int64(len(actual)))
	require.NoError(t, err)
	require.Len(t, reader.File, 1)
	assert.Equal(t, header.Name, reader.File[0].Name)
	assert.Equal(t, os.FileMode(0o755), reader.File[0].Mode().Perm())
	assert.Equal(t, header.Comment, reader.File[0].Comment)
	assert.True(t, header.Modified.Equal(reader.File[0].Modified))
}

func TestArchiveScenarioA12UnsafeNamesAreNeverExtracted(t *testing.T) {
	// Exercise the opaque ZIP path even when Go's optional name guard is disabled.
	// The tool must not write an entry outside its artifact directory.
	t.Setenv("GODEBUG", "zipinsecurepath=1")
	dir := t.TempDir()
	artifacts := filepath.Join(dir, "artifacts")
	require.NoError(t, os.Mkdir(artifacts, 0o700))
	path := filepath.Join(artifacts, "code.zip")
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	entry, err := writer.Create("../escaped.txt")
	require.NoError(t, err)
	_, err = entry.Write([]byte("must remain inside ZIP"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.NoError(t, os.WriteFile(path, buffer.Bytes(), 0o600))
	archive, err := buildAssetSentinel(path, "FileArchive")
	require.NoError(t, err)
	hash := sha256.Sum256(buffer.Bytes())
	assert.Equal(t, hex.EncodeToString(hash[:]), archive["hash"])
	assert.NoFileExists(t, filepath.Join(dir, "escaped.txt"))
	entries, err := os.ReadDir(artifacts)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

func TestArchiveScenarioA4DownloadRejectsMalformedZipWithMatchingChecksum(t *testing.T) {
	body := []byte("not a ZIP")
	hash := sha256.Sum256(body)
	archiveLambdaServer(t, body, base64.StdEncoding.EncodeToString(hash[:]), http.StatusOK)
	path := filepath.Join(t.TempDir(), "keep.zip")
	require.NoError(t, os.WriteFile(path, []byte("keep original artifact"), 0o600))
	err := DownloadLambdaCodeToFile(t.Context(), "fn", "", "us-west-2", path)
	require.Error(t, err, "checksum success alone must not accept an invalid archive")
	actual, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "keep original artifact", string(actual))
}

func TestArchiveScenarioA9DirectoryDoesNotProveZipIdentity(t *testing.T) {
	dir := t.TempDir()
	archiveDir := filepath.Join(dir, "function")
	require.NoError(t, os.MkdirAll(filepath.Join(archiveDir, "bin"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(archiveDir, "bin", "handler"), []byte("deployed"), 0o755))
	contents := writeArchiveScenarioZip(t, filepath.Join(dir, "function.zip"), []byte("deployed"))
	hash := sha256.Sum256(contents)
	directoryArchive, err := buildAssetSentinel(archiveDir, "FileArchive")
	require.NoError(t, err)
	require.NotEqual(t, hex.EncodeToString(hash[:]), directoryArchive["hash"], "fixture must distinguish equivalent files from exact ZIP identity")
	inputs := map[string]interface{}{}
	_, err = patchResourceFields([]patchFieldDescriptor{{PulumiName: "code", TFName: "filename", AssetType: "FileArchive"}}, inputs,
		map[string]interface{}{"codeSha256": base64.StdEncoding.EncodeToString(hash[:])},
		&ModuleResource{Attributes: map[string]interface{}{"filename": "function"}}, nil, dir)
	require.ErrorContains(t, err, "does not match deployed codeSha256")
	assert.Empty(t, inputs)
}

func TestArchiveScenarioA13MissingPathDoesNotGuessAnotherZip(t *testing.T) {
	dir := t.TempDir()
	writeArchiveScenarioZip(t, filepath.Join(dir, "different.zip"), []byte("deployed"))
	inputs := map[string]interface{}{}
	_, err := patchResourceFields([]patchFieldDescriptor{{PulumiName: "code", TFName: "filename", AssetType: "FileArchive"}}, inputs, map[string]interface{}{},
		&ModuleResource{Attributes: map[string]interface{}{"filename": "missing.zip"}}, nil, dir)
	require.ErrorContains(t, err, "cannot reconstruct code")
	assert.Empty(t, inputs)
}

func TestArchiveScenarioA2MalformedImportedHashIsRejected(t *testing.T) {
	dir := t.TempDir()
	writeArchiveScenarioZip(t, filepath.Join(dir, "code.zip"), []byte("deployed"))
	for _, hash := range []string{"not-base64", base64.StdEncoding.EncodeToString([]byte("short"))} {
		t.Run(hash, func(t *testing.T) {
			inputs := map[string]interface{}{}
			_, err := patchResourceFields([]patchFieldDescriptor{{PulumiName: "code", TFName: "filename", AssetType: "FileArchive"}}, inputs,
				map[string]interface{}{"codeSha256": hash}, &ModuleResource{Attributes: map[string]interface{}{"filename": "code.zip"}}, nil, dir)
			require.ErrorContains(t, err, "invalid deployed codeSha256")
			assert.Empty(t, inputs)
		})
	}
}
