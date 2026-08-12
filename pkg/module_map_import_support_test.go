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
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg/importsupport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubChecker answers from a fixed table so digest tests do not need a live
// Terraform provider.
type stubChecker struct {
	verdicts map[string]importsupport.Support
	asked    []string
}

func (s *stubChecker) Check(_ context.Context, providerAddr, tfType string) importsupport.Support {
	s.asked = append(s.asked, providerAddr+"|"+tfType)
	if v, ok := s.verdicts[tfType]; ok {
		return v
	}
	return importsupport.Unknown
}

func TestBuildModuleMapFlagsNonImportableResources(t *testing.T) {
	t.Parallel()
	tfDir, err := filepath.Abs(filepath.Join("testdata", "tf_indexed_modules"))
	require.NoError(t, err)

	config, err := LoadConfig(tfDir)
	require.NoError(t, err)
	rawState, err := LoadRawState(filepath.Join(tfDir, "terraform.tfstate"))
	require.NoError(t, err)

	checker := &stubChecker{verdicts: map[string]importsupport.Support{
		"random_pet": importsupport.Unsupported,
	}}

	mm, err := BuildModuleMap(context.Background(), config, nil, rawState, nil, "test-stack", "test-project", checker)
	require.NoError(t, err)

	require.Contains(t, mm.Modules, "pet[0]")
	require.Len(t, mm.Modules["pet[0]"].Resources, 1)
	assert.True(t, mm.Modules["pet[0]"].Resources[0].NonImportable)
}

func TestBuildModuleMapLeavesImportableResourcesUnflagged(t *testing.T) {
	t.Parallel()
	tfDir, err := filepath.Abs(filepath.Join("testdata", "tf_indexed_modules"))
	require.NoError(t, err)

	config, err := LoadConfig(tfDir)
	require.NoError(t, err)
	rawState, err := LoadRawState(filepath.Join(tfDir, "terraform.tfstate"))
	require.NoError(t, err)

	checker := &stubChecker{verdicts: map[string]importsupport.Support{
		"random_pet": importsupport.Supported,
	}}

	mm, err := BuildModuleMap(context.Background(), config, nil, rawState, nil, "test-stack", "test-project", checker)
	require.NoError(t, err)

	resource := mm.Modules["pet[0]"].Resources[0]
	assert.False(t, resource.NonImportable)

	// The flag stays out of the digest unless it is set.
	encoded, err := json.Marshal(resource)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "nonImportable")
}

func TestBuildModuleMapWithoutCheckerFlagsNothing(t *testing.T) {
	t.Parallel()
	tfDir, err := filepath.Abs(filepath.Join("testdata", "tf_indexed_modules"))
	require.NoError(t, err)

	config, err := LoadConfig(tfDir)
	require.NoError(t, err)
	rawState, err := LoadRawState(filepath.Join(tfDir, "terraform.tfstate"))
	require.NoError(t, err)

	mm, err := BuildModuleMap(context.Background(), config, nil, rawState, nil, "test-stack", "test-project", nil)
	require.NoError(t, err)

	assert.False(t, mm.Modules["pet[0]"].Resources[0].NonImportable)
}
