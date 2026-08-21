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
	"os"
	"path/filepath"
	"testing"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg/providermap"
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge/info"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildModuleMap_RecordsResolvedProviderVersions(t *testing.T) {
	t.Parallel()

	providers := map[providermap.TerraformProviderName]*ProviderWithMetadata{
		"registry.opentofu.org/hashicorp/aws": {
			Provider:         &info.Provider{Name: "aws"},
			TerraformAddress: "registry.opentofu.org/hashicorp/aws",
			ResolvedPulumi:   "aws@v7.12.0",
		},
	}

	mm, err := BuildModuleMap(t.Context(), nil, nil, nil, providers, "dev", "proj", nil)
	require.NoError(t, err)
	assert.Equal(t, "aws@v7.12.0", mm.Providers["registry.opentofu.org/hashicorp/aws"],
		"the digest must record which provider version its property names came from")
}

func TestApplyPin_OverridesTheStaticVersion(t *testing.T) {
	t.Parallel()

	rec := providermap.RecommendedPulumiProvider{
		StaticallyBridgedProvider: &providermap.BridgedPulumiProvider{
			Identifier: "aws", Version: "v99.0.0",
		},
	}
	pinned, err := applyProviderPin(rec, "aws@v7.12.0")
	require.NoError(t, err)
	assert.Equal(t, "v7.12.0", pinned.StaticallyBridgedProvider.Version,
		"injection must load the version the digest was computed against, not today's recommendation")
	assert.Equal(t, "v99.0.0", rec.StaticallyBridgedProvider.Version,
		"the input must not be mutated")
}

func TestApplyPin_RejectsAnIdentifierMismatch(t *testing.T) {
	t.Parallel()

	rec := providermap.RecommendedPulumiProvider{
		StaticallyBridgedProvider: &providermap.BridgedPulumiProvider{
			Identifier: "aws", Version: "v7.12.0",
		},
	}
	_, err := applyProviderPin(rec, "aws-native@v3.0.0")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "aws-native")
	assert.Contains(t, err.Error(), "aws")
}

func TestApplyPin_EmptyIsUnrecordedButDynamicIsDrift(t *testing.T) {
	t.Parallel()

	rec := providermap.RecommendedPulumiProvider{
		StaticallyBridgedProvider: &providermap.BridgedPulumiProvider{
			Identifier: "aws", Version: "v7.12.0",
		},
	}
	got, err := applyProviderPin(rec, "")
	require.NoError(t, err)
	assert.Equal(t, "v7.12.0", got.StaticallyBridgedProvider.Version)

	_, err = applyProviderPin(rec, "dynamic")
	require.Error(t, err)

	// And "dynamic" against a dynamic recommendation is a matched pair.
	dyn := providermap.RecommendedPulumiProvider{UseDynamicBridging: true}
	got, err = applyProviderPin(dyn, "dynamic")
	require.NoError(t, err)
	assert.True(t, got.UseDynamicBridging)
}

func TestApplyPin_DynamicRecommendationIgnoresStaticPin(t *testing.T) {
	t.Parallel()

	rec := providermap.RecommendedPulumiProvider{UseDynamicBridging: true}
	got, err := applyProviderPin(rec, "aws@v7.12.0")
	require.Error(t, err,
		"a static pin against a build that now recommends dynamic bridging is the same mapping drift "+
			"as an identifier mismatch")
	_ = got
}

func TestDigestFormatVersion_RoundTripsAndRejectsNewer(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "tf-digest.json")
	require.NoError(t, WriteModuleMap(&ModuleMap{}, path))

	mm, err := LoadDigest(path)
	require.NoError(t, err)
	assert.Equal(t, CurrentDigestFormatVersion, mm.FormatVersion)

	// A digest from a future tool version must be refused, naming both versions.
	future := []byte(`{"digestFormatVersion": 99, "modules": {}}`)
	futurePath := filepath.Join(dir, "future.json")
	require.NoError(t, os.WriteFile(futurePath, future, 0o644))
	_, err = LoadDigest(futurePath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "99")

	// A digest from before the field existed reads as version 0 and is accepted.
	legacy := []byte(`{"modules": {}}`)
	legacyPath := filepath.Join(dir, "legacy.json")
	require.NoError(t, os.WriteFile(legacyPath, legacy, 0o644))
	mm, err = LoadDigest(legacyPath)
	require.NoError(t, err)
	assert.Equal(t, 0, mm.FormatVersion)
}

func TestLoadDigest_PreservesLargeIntegers(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "tf-digest.json")
	data := []byte(`{"modules":{},"rootResources":[{"mode":"managed","translatedUrn":"u","terraformAddress":"a","importId":"1234567890123456789","attributes":{"n":1234567890123456789}}]}`)
	require.NoError(t, os.WriteFile(path, data, 0o644))

	mm, err := LoadDigest(path)
	require.NoError(t, err)
	require.Len(t, mm.RootResources, 1)
	assert.Equal(t, "1234567890123456789", mm.RootResources[0].Attributes["n"].(interface{ String() string }).String())
}
