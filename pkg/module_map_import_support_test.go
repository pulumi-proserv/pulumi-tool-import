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
	"github.com/pulumi-proserv/pulumi-tool-import/pkg/providermap"
	"github.com/pulumi-proserv/pulumi-tool-import/pkg/tfprovider"
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

// A digest built without the check must be distinguishable from one where the
// check ran and found nothing — otherwise "no non-importable resources" and
// "we never looked" are the same document.
func TestBuildModuleMapRecordsThatTheCheckRan(t *testing.T) {
	t.Parallel()
	mm := buildIndexedModulesMap(t, &stubChecker{verdicts: map[string]importsupport.Support{
		"random_pet": importsupport.Supported,
	}})
	assert.True(t, mm.ImportSupportChecked)
}

func TestBuildModuleMapRecordsThatTheCheckWasSkipped(t *testing.T) {
	t.Parallel()
	mm := buildIndexedModulesMap(t, nil)
	assert.False(t, mm.ImportSupportChecked)
}

// checkerWithProvider adds ProviderAccessor to stubChecker, so tests can
// exercise matchResources' non-importable state computation without needing
// a real importsupport.Prober.
type checkerWithProvider struct {
	*stubChecker
	prov tfprovider.Provider
}

func (c *checkerWithProvider) Provider(context.Context, string) (tfprovider.Provider, bool) {
	return c.prov, true
}

// A provider that answers Check is not enough on its own: matchResources
// also needs the Pulumi-bridged mock schema (pulumiProviders) to compute a
// nested-block delta, and that map can legitimately have no entry for a
// provider the checker loaded just fine (they are two different loaders with
// different failure modes). Without a schema there is nothing safe to
// compute — the fix is to skip, not to hand tfbridge a nil SchemaMap and let
// it panic. This exercises exactly that path end-to-end through
// BuildModuleMap/matchResources/populateInjectionState, with pulumiProviders
// left nil to force it.
func TestBuildModuleMapSkipsInjectionStateWithoutBridgedSchema(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	prov, err := tfprovider.LoadProvider(ctx, "registry.opentofu.org/hashicorp/random", "3.9.0")
	if err != nil {
		t.Skipf("random provider unavailable: %v", err)
	}
	defer prov.Close(ctx)

	tfDir, err := filepath.Abs(filepath.Join("testdata", "tf_indexed_modules"))
	require.NoError(t, err)
	config, err := LoadConfig(tfDir)
	require.NoError(t, err)
	rawState, err := LoadRawState(filepath.Join(tfDir, "terraform.tfstate"))
	require.NoError(t, err)

	checker := &checkerWithProvider{
		stubChecker: &stubChecker{verdicts: map[string]importsupport.Support{
			"random_pet": importsupport.Unsupported,
		}},
		prov: prov,
	}

	// pulumiProviders is nil: no bridged schema is available for
	// populateInjectionState to use, so it must degrade to leaving the new
	// fields empty rather than passing a nil SchemaMap into
	// ComputeInjectionState (which panics for nested-block types) or, worse,
	// crashing the whole digest.
	require.NotPanics(t, func() {
		mm, err := BuildModuleMap(ctx, config, nil, rawState, nil, "test-stack", "test-project", checker)
		require.NoError(t, err)

		require.Contains(t, mm.Modules, "pet[0]")
		require.Len(t, mm.Modules["pet[0]"].Resources, 1)
		res := mm.Modules["pet[0]"].Resources[0]
		assert.True(t, res.NonImportable)
		assert.Nil(t, res.PulumiOutputs)
		assert.Nil(t, res.RawStateDelta)
		assert.Zero(t, res.SchemaVersion)
	})
}

// With a bridged schema available, matchResources populates the new fields
// for a resource it flags non-importable, end to end through BuildModuleMap.
func TestBuildModuleMapPopulatesInjectionStateWithBridgedSchema(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	prov, err := tfprovider.LoadProvider(ctx, "registry.opentofu.org/hashicorp/random", "3.9.0")
	if err != nil {
		t.Skipf("random provider unavailable: %v", err)
	}
	defer prov.Close(ctx)

	pulumiProviders, err := PulumiProvidersForTerraformProviders(
		[]providermap.TerraformProviderName{"registry.opentofu.org/hashicorp/random"},
		map[string]string{"registry.opentofu.org/hashicorp/random": "3.9.0"},
	)
	if err != nil {
		t.Skipf("could not bridge random provider schema: %v", err)
	}

	tfDir, err := filepath.Abs(filepath.Join("testdata", "tf_indexed_modules"))
	require.NoError(t, err)
	config, err := LoadConfig(tfDir)
	require.NoError(t, err)
	rawState, err := LoadRawState(filepath.Join(tfDir, "terraform.tfstate"))
	require.NoError(t, err)

	checker := &checkerWithProvider{
		stubChecker: &stubChecker{verdicts: map[string]importsupport.Support{
			"random_pet": importsupport.Unsupported,
		}},
		prov: prov,
	}

	mm, err := BuildModuleMap(ctx, config, nil, rawState, pulumiProviders, "test-stack", "test-project", checker)
	require.NoError(t, err)

	require.Contains(t, mm.Modules, "pet[0]")
	require.Len(t, mm.Modules["pet[0]"].Resources, 1)
	res := mm.Modules["pet[0]"].Resources[0]
	assert.True(t, res.NonImportable)
	require.NotNil(t, res.PulumiOutputs)
	assert.Equal(t, "test-0-just-phoenix", res.PulumiOutputs["id"])
	assert.Equal(t, "-", res.PulumiOutputs["separator"])
}

func buildIndexedModulesMap(t *testing.T, checker ImportSupportChecker) *ModuleMap {
	t.Helper()
	tfDir, err := filepath.Abs(filepath.Join("testdata", "tf_indexed_modules"))
	require.NoError(t, err)
	config, err := LoadConfig(tfDir)
	require.NoError(t, err)
	rawState, err := LoadRawState(filepath.Join(tfDir, "terraform.tfstate"))
	require.NoError(t, err)

	mm, err := BuildModuleMap(context.Background(), config, nil, rawState, nil, "test-stack", "test-project", checker)
	require.NoError(t, err)
	return mm
}
