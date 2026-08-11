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

package batchimport

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/optimport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadImportFile_PreservesAllFields(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "imports.json")
	content := `{
  "nameTable": {"vpc": "urn:pulumi:dev::proj::example:index:Vpc::vpc"},
  "resources": [
    {"type": "example:index:Vpc", "name": "vpc", "component": true},
    {"type": "aws:ec2/vpc:Vpc", "name": "vpc-main", "id": "vpc-abc123",
     "parent": "vpc", "logicalName": "mainVpc", "properties": ["cidrBlock"],
     "version": "7.0.0", "pluginDownloadUrl": "https://example.invalid", "remote": true}
  ]
}`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	f, err := LoadImportFile(path)
	require.NoError(t, err)

	require.Len(t, f.Resources, 2)
	assert.Equal(t, map[string]string{
		"vpc": "urn:pulumi:dev::proj::example:index:Vpc::vpc",
	}, f.NameTable)

	assert.True(t, f.Resources[0].Component)

	r := f.Resources[1]
	assert.Equal(t, "vpc-abc123", r.ID)
	assert.Equal(t, "vpc", r.Parent)
	assert.Equal(t, "mainVpc", r.LogicalName)
	assert.Equal(t, []string{"cidrBlock"}, r.Properties)
	assert.Equal(t, "7.0.0", r.Version)
	assert.Equal(t, "https://example.invalid", r.PluginDownloadURL)
	assert.True(t, r.Remote)
}

func TestLoadImportFile_Errors(t *testing.T) {
	t.Parallel()

	_, err := LoadImportFile(filepath.Join(t.TempDir(), "missing.json"))
	require.ErrorContains(t, err, "reading import file")

	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(bad, []byte("{not json"), 0o600))
	_, err = LoadImportFile(bad)
	require.ErrorContains(t, err, "parsing import file")
}

func TestKeyOf(t *testing.T) {
	t.Parallel()

	k := keyOf(&optimport.ImportResource{Type: "aws:s3/bucket:Bucket", Name: "b"})
	assert.Equal(t, ResourceKey{Type: "aws:s3/bucket:Bucket", Name: "b"}, k)
}
