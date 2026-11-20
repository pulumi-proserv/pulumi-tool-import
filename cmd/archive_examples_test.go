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

package cmd

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg"
	"github.com/pulumi-proserv/pulumi-tool-import/pkg/cfn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func archiveCommandZIP(t *testing.T, content string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	entry, err := writer.Create("index.js")
	require.NoError(t, err)
	_, err = entry.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	return buffer.Bytes()
}

func archiveCommandJSON(t *testing.T, dir, name string, value interface{}) string {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

func archiveCommandState(t *testing.T, dir, codeHash string) string {
	t.Helper()
	const providerURN = "urn:pulumi:dev::p::pulumi:providers:aws::default"
	return archiveCommandJSON(t, dir, "state.json", map[string]interface{}{
		"version": 3, "deployment": map[string]interface{}{"resources": []interface{}{
			map[string]interface{}{"urn": providerURN, "type": "pulumi:providers:aws", "custom": true, "id": "provider-id", "inputs": map[string]interface{}{"region": "us-west-2"}, "outputs": map[string]interface{}{"region": "us-west-2"}},
			map[string]interface{}{"urn": "urn:pulumi:dev::p::aws:lambda/function:Function::fn", "type": "aws:lambda/function:Function", "custom": true, "id": "fn", "provider": providerURN + "::provider-id", "inputs": map[string]interface{}{}, "outputs": map[string]interface{}{"codeSha256": codeHash}},
		}},
	})
}

func archiveCommandFields(t *testing.T, dir string) string {
	t.Helper()
	return archiveCommandJSON(t, dir, "fields.json", &pkg.FieldsFile{Fields: map[string]pkg.FieldCategory{
		"aws:lambda/function:Function": {NotRead: map[string]pkg.FieldInfo{"code": {Asset: "FileArchive"}}},
	}})
}

func archiveCommandStderr(t *testing.T, call func() error) (string, error) {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "stderr-")
	require.NoError(t, err)
	defer file.Close()
	previous := os.Stderr
	os.Stderr = file
	defer func() { os.Stderr = previous }()
	callErr := call()
	os.Stderr = previous
	data, err := os.ReadFile(file.Name())
	require.NoError(t, err)
	return string(data), callErr
}

func TestArchiveScenarioV1FileModeWritesOnlySuccessfulCandidate(t *testing.T) {
	for _, stale := range []bool{false, true} {
		name := "matching-zip"
		if stale {
			name = "stale-zip"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			deployed := archiveCommandZIP(t, "deployed")
			hash := sha256.Sum256(deployed)
			statePath := archiveCommandState(t, dir, base64.StdEncoding.EncodeToString(hash[:]))
			before, err := os.ReadFile(statePath)
			require.NoError(t, err)
			contents := deployed
			if stale {
				contents = archiveCommandZIP(t, "stale")
			}
			require.NoError(t, os.WriteFile(filepath.Join(dir, "code.zip"), contents, 0o600))
			digestPath := archiveCommandJSON(t, dir, "digest.json", &pkg.ModuleMap{RootResources: []pkg.ModuleResource{{Mode: "managed", TerraformAddress: "aws_lambda_function.fn", TranslatedURN: "urn:pulumi:dev::p::aws:lambda/function:Function::fn", ImportID: "fn", Attributes: map[string]interface{}{"filename": "code.zip"}}}})
			out := filepath.Join(dir, "patched.json")
			require.NoError(t, os.WriteFile(out, []byte("existing output"), 0o600))
			command := newPatchStateTfCmd()
			command.SetArgs([]string{"--state", statePath, "--out", out, "--digest", digestPath, "--fields", archiveCommandFields(t, dir), "--config-dir", dir})
			log, err := archiveCommandStderr(t, command.Execute)
			if stale {
				require.ErrorContains(t, err, "does not match deployed codeSha256")
				actual, err := os.ReadFile(out)
				require.NoError(t, err)
				assert.Equal(t, "existing output", string(actual))
			} else {
				require.NoError(t, err)
				assert.Contains(t, log, "NOT VERIFIED")
				actual, err := os.ReadFile(out)
				require.NoError(t, err)
				assert.True(t, json.Valid(actual))
				assert.Contains(t, string(actual), hex.EncodeToString(hash[:]))
			}
			after, err := os.ReadFile(statePath)
			require.NoError(t, err)
			assert.Equal(t, before, after, "file mode must never overwrite its input snapshot")
		})
	}
}

func TestArchiveScenariosP2P3CFNDownloadIgnoresTemplatePackaging(t *testing.T) {
	const handler = "exports.handler = async () => ({ok: true});"
	for _, tc := range []struct {
		name string
		code map[string]interface{}
	}{
		{"s3-code", map[string]interface{}{"S3Bucket": "source-bucket", "S3Key": "function.zip"}},
		{"inline-code", map[string]interface{}{"ZipFile": handler}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			deployed := archiveCommandZIP(t, handler)
			hash := sha256.Sum256(deployed)
			var getFunctionCalls, downloadCalls atomic.Int32
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method)
				switch r.URL.Path {
				case "/2015-03-31/functions/fn":
					getFunctionCalls.Add(1)
					assert.Contains(t, r.Header.Get("Authorization"), "/us-west-2/lambda/")
					w.Header().Set("Content-Type", "application/json")
					assert.NoError(t, json.NewEncoder(w).Encode(map[string]interface{}{"Code": map[string]string{"Location": server.URL + "/archive"}, "Configuration": map[string]string{"CodeSha256": base64.StdEncoding.EncodeToString(hash[:])}}))
				case "/archive":
					downloadCalls.Add(1)
					_, err := w.Write(deployed)
					assert.NoError(t, err)
				default:
					t.Errorf("unexpected AWS request: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(server.Close)
			t.Setenv("AWS_ENDPOINT_URL_LAMBDA", server.URL)
			t.Setenv("AWS_ACCESS_KEY_ID", "test")
			t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
			t.Setenv("AWS_SESSION_TOKEN", "")
			t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
			t.Setenv("AWS_REGION", "us-east-1") // digest region must win
			state := archiveCommandState(t, dir, base64.StdEncoding.EncodeToString(hash[:]))
			digest := &cfn.StackDigest{Region: "us-west-2", Resources: []cfn.CfnResource{
				{LogicalID: "fn", CfnType: "AWS::Lambda::Function", PulumiType: "aws:lambda/function:Function", PhysicalID: "fn", Attributes: map[string]interface{}{"Code": tc.code}},
				{LogicalID: "not-imported", CfnType: "AWS::Lambda::Function", PulumiType: "aws:lambda/function:Function", PhysicalID: "not-imported"},
			}}
			digestPath := archiveCommandJSON(t, dir, "digest.json", digest)
			artifacts := filepath.Join(dir, "artifacts")
			out := filepath.Join(dir, "patched.json")
			command := newPatchStateCfnCmd()
			command.SetArgs([]string{"--state", state, "--out", out, "--digest", digestPath, "--fields", archiveCommandFields(t, dir), "--artifacts-dir", artifacts})
			log, err := archiveCommandStderr(t, command.Execute)
			require.NoError(t, err)
			assert.Contains(t, log, "NOT VERIFIED")
			assert.Equal(t, int32(1), getFunctionCalls.Load(), "only an imported Lambda should be fetched")
			assert.Equal(t, int32(1), downloadCalls.Load())
			actual, err := os.ReadFile(filepath.Join(artifacts, "fn.zip"))
			require.NoError(t, err)
			assert.Equal(t, deployed, actual)
			patched, err := os.ReadFile(out)
			require.NoError(t, err)
			assert.Contains(t, string(patched), hex.EncodeToString(hash[:]))
			var decoded struct {
				Deployment struct {
					Resources []struct {
						Inputs  map[string]interface{}
						Outputs map[string]interface{}
					}
				}
			}
			require.NoError(t, json.Unmarshal(patched, &decoded))
			require.Len(t, decoded.Deployment.Resources, 2)
			resource := decoded.Deployment.Resources[1]
			code := resource.Inputs["code"].(map[string]interface{})
			assert.Equal(t, filepath.Join(artifacts, "fn.zip"), code["path"])
			assert.Equal(t, code, resource.Outputs["code"])
			assert.NotContains(t, code, "assets")
			assert.NotContains(t, string(patched), "ZipFile", "state must reference the deployed ZIP, not reconstruct template inline packaging")
		})
	}
}
