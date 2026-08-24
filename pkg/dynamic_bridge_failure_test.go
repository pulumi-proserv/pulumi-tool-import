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
	"fmt"
	"testing"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg/providermap"
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge/info"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A provider with no static bridge mapping resolves through dynamic bridging.
const dynamicOnlyProvider = "registry.terraform.io/example/nonexistent"

func withFailingDynamicBridge(t *testing.T) {
	t.Helper()
	prev := dynamicBridge
	dynamicBridge = func(context.Context, string, string) (*info.Provider, error) {
		return nil, fmt.Errorf("registry unreachable")
	}
	t.Cleanup(func() { dynamicBridge = prev })
}

func TestDynamicBridgeFailure_PinnedProviderIsAnError(t *testing.T) {
	withFailingDynamicBridge(t)

	_, err := PulumiProvidersForTerraformProvidersPinned(
		[]providermap.TerraformProviderName{dynamicOnlyProvider},
		map[string]string{dynamicOnlyProvider: "dynamic@1.0.0"})
	require.Error(t, err,
		"silently dropping a provider the digest pinned defeats the pin's purpose")
	assert.Contains(t, err.Error(), dynamicOnlyProvider)
	assert.Contains(t, err.Error(), "registry unreachable")
}

func TestDynamicBridgeFailure_UnpinnedProviderIsSkippedWithoutError(t *testing.T) {
	withFailingDynamicBridge(t)

	providers, err := PulumiProvidersForTerraformProviders(
		[]providermap.TerraformProviderName{dynamicOnlyProvider}, nil)
	require.NoError(t, err)
	assert.NotContains(t, providers, providermap.TerraformProviderName(dynamicOnlyProvider))
}
