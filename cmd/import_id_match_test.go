// Copyright 2016-2026, Pulumi Corporation.
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
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveTFSelectsCatalogFromDestinationVersion(t *testing.T) {
	for _, tc := range []struct{ version, want string }{
		{"6.83.4", "events"}, {"7.48.0", "events"}, {"8.0.0", "arn:aws:kinesis:us-east-1:123:stream/events"},
	} {
		t.Run(tc.version, func(t *testing.T) {
			dir := t.TempDir()
			const stateID = "arn:aws:kinesis:us-east-1:123:stream/events"
			digest := &pkg.ModuleMap{RootResources: []pkg.ModuleResource{{
				Mode: "managed", TerraformAddress: "aws_kinesis_stream.events", ImportID: stateID,
				Attributes: map[string]interface{}{"name": "events"},
			}}}
			imports := &pkg.ImportFile{Resources: []pkg.ImportEntry{{Type: "aws:kinesis/stream:Stream", Name: "events", ID: stateID, Version: tc.version}}}
			for name, value := range map[string]interface{}{"digest.json": digest, "imports.json": imports} {
				data, err := json.Marshal(value)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), data, 0o600))
			}
			out := filepath.Join(dir, "out.json")
			cmd := buildImportIDMatchCommand("tf", false)
			cmd.SetArgs([]string{"--digest", filepath.Join(dir, "digest.json"), "--import-file", filepath.Join(dir, "imports.json"), "--out", out})
			require.NoError(t, cmd.Execute())
			data, err := os.ReadFile(out)
			require.NoError(t, err)
			var result pkg.ImportFile
			require.NoError(t, json.Unmarshal(data, &result))
			require.Len(t, result.Resources, 1)
			assert.Equal(t, tc.want, result.Resources[0].ID)
		})
	}
}
