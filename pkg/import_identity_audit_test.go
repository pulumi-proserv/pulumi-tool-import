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

package pkg

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFocusedIdentityAuditVectors(t *testing.T) {
	t.Parallel()
	const directory = "../importids/audits/aws-v7.51.1-focused/"
	var report struct {
		Coverage struct {
			SelectedResources    []string
			ProcessedResources   []string
			UnprocessedResources []string
			Complete             bool
		}
		Resources []struct {
			TerraformType string
			PulumiTypes   []string
			Evidence      []struct{ ID string }
		}
	}
	data, err := os.ReadFile(directory + "identity-report.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &report))
	require.True(t, report.Coverage.Complete)
	require.Empty(t, report.Coverage.UnprocessedResources)
	require.ElementsMatch(t, report.Coverage.SelectedResources, report.Coverage.ProcessedResources)
	tokens := map[string]string{}
	evidence := map[string]map[string]bool{}
	for _, resource := range report.Resources {
		require.NotContains(t, tokens, resource.TerraformType)
		require.NotEmpty(t, resource.PulumiTypes)
		tokens[resource.TerraformType] = resource.PulumiTypes[0]
		evidence[resource.TerraformType] = map[string]bool{}
		for _, item := range resource.Evidence {
			evidence[resource.TerraformType][item.ID] = true
		}
	}
	require.Len(t, tokens, len(report.Coverage.SelectedResources))
	var vectors struct {
		DestinationVersion string
		Cases              []struct {
			Name          string
			TerraformType string
			ID            string
			Attributes    map[string]interface{}
			Outcome       string
			ExpectedID    *string
			Evidence      []string
		}
	}
	data, err = os.ReadFile(directory + "identity-vectors.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &vectors))
	covered := map[string]bool{}
	for _, vector := range vectors.Cases {
		covered[vector.TerraformType] = true
		t.Run(vector.Name, func(t *testing.T) {
			require.Contains(t, tokens, vector.TerraformType)
			require.NotEmpty(t, vector.Evidence)
			for _, id := range vector.Evidence {
				require.True(t, evidence[vector.TerraformType][id], "unknown evidence %s", id)
			}
			digest := &ModuleMap{RootResources: []ModuleResource{{Mode: "managed", TerraformAddress: vector.TerraformType + ".example", ImportID: vector.ID, Attributes: vector.Attributes}}}
			file := &ImportFile{Resources: []ImportEntry{{Type: tokens[vector.TerraformType], Name: "example", ID: vector.ID, Version: vectors.DestinationVersion}}}
			result := TranslateImportIDsForProviders(file, digest, nil)
			switch vector.Outcome {
			case "resolved":
				require.NotNil(t, vector.ExpectedID)
				assert.Equal(t, *vector.ExpectedID, file.Resources[0].ID)
				assert.Zero(t, result.Unverified)
				assert.Empty(t, result.Notes)
				// A normalized source ID must remain stable on a subsequent pass.
				digest.RootResources[0].ImportID = file.Resources[0].ID
				again := TranslateImportIDsForProviders(file, digest, nil)
				assert.Zero(t, again.Translated)
				assert.Zero(t, again.Unverified)
				assert.Equal(t, *vector.ExpectedID, file.Resources[0].ID)
			case "unresolved":
				assert.Nil(t, vector.ExpectedID)
				assert.Equal(t, vector.ID, file.Resources[0].ID)
				assert.Equal(t, 1, result.Unverified)
				require.NotEmpty(t, result.Notes)
				assert.Contains(t, result.Notes[len(result.Notes)-1], "cannot compose import ID")
			default:
				t.Fatalf("unexpected outcome %q", vector.Outcome)
			}
		})
	}
	for _, typ := range report.Coverage.SelectedResources {
		assert.True(t, covered[typ], "no regression vectors for %s", typ)
	}
}
