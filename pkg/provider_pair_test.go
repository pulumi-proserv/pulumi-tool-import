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
	"testing"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg/providermap"
	"github.com/pulumi-proserv/pulumi-tool-import/pkg/tfprovider"
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge/info"
	shim "github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfshim"
	shimschema "github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfshim/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAccessor holds live providers keyed by exact address, like the prober.
type fakeAccessor struct {
	providers map[string]tfprovider.Provider
}

func (f *fakeAccessor) Provider(_ context.Context, addr string) (tfprovider.Provider, bool) {
	p, ok := f.providers[addr]
	return p, ok
}

// stubProvider is a stand-in the pair resolver holds but never invokes; its
// embedded interface is nil, so an accidental call panics the test.
type stubProvider struct{ tfprovider.Provider }

// bridgedProvidersFor builds the bridged half of the pair only; the live half
// comes from the accessor.
func bridgedProvidersFor(addr string) map[providermap.TerraformProviderName]*ProviderWithMetadata {
	shimProv := &shimschema.Provider{
		ResourcesMap: shimschema.ResourceMap{
			"aws_x": (&shimschema.Resource{
				Schema: shimschema.SchemaMap{
					"id": (&shimschema.Schema{Type: shim.TypeString, Computed: true}).Shim(),
				},
			}).Shim(),
		},
	}
	return map[providermap.TerraformProviderName]*ProviderWithMetadata{
		providermap.TerraformProviderName(addr): {
			Provider: &info.Provider{
				Name:      "aws",
				P:         shimProv.Shim(),
				Resources: map[string]*info.Resource{"aws_x": {}},
			},
			TerraformAddress: addr,
		},
	}
}

func TestResolveInjectionProviders_BothHalvesResolve(t *testing.T) {
	t.Parallel()

	acc := &fakeAccessor{providers: map[string]tfprovider.Provider{
		"registry.opentofu.org/hashicorp/aws": stubProvider{},
	}}
	pair := resolveInjectionProviders(t.Context(), acc,
		bridgedProvidersFor("registry.opentofu.org/hashicorp/aws"),
		"registry.opentofu.org/hashicorp/aws", "aws_x")

	assert.Empty(t, pair.MissingReason)
	assert.NotNil(t, pair.Live)
	assert.NotNil(t, pair.SchemaMap)
}

func TestResolveInjectionProviders_RegistryHostsAreEquivalent(t *testing.T) {
	t.Parallel()

	// Live provider keyed by the terraform.io form, lookup by the opentofu form.
	acc := &fakeAccessor{providers: map[string]tfprovider.Provider{
		"registry.terraform.io/hashicorp/aws": stubProvider{},
	}}
	pair := resolveInjectionProviders(t.Context(), acc,
		bridgedProvidersFor("registry.opentofu.org/hashicorp/aws"),
		"registry.opentofu.org/hashicorp/aws", "aws_x")
	assert.Empty(t, pair.MissingReason)
	assert.NotNil(t, pair.Live)

	// And the bridged half keyed by the other form.
	acc = &fakeAccessor{providers: map[string]tfprovider.Provider{
		"registry.opentofu.org/hashicorp/aws": stubProvider{},
	}}
	pair = resolveInjectionProviders(t.Context(), acc,
		bridgedProvidersFor("registry.terraform.io/hashicorp/aws"),
		"registry.opentofu.org/hashicorp/aws", "aws_x")
	assert.Empty(t, pair.MissingReason)
	require.NotNil(t, pair.SchemaMap)

	// The other direction: queried with the terraform.io form against
	// opentofu-keyed maps.
	acc = &fakeAccessor{providers: map[string]tfprovider.Provider{
		"registry.opentofu.org/hashicorp/aws": stubProvider{},
	}}
	pair = resolveInjectionProviders(t.Context(), acc,
		bridgedProvidersFor("registry.opentofu.org/hashicorp/aws"),
		"registry.terraform.io/hashicorp/aws", "aws_x")
	assert.Empty(t, pair.MissingReason)
	assert.NotNil(t, pair.Live)
	require.NotNil(t, pair.SchemaMap)
}

func TestResolveInjectionProviders_NamesTheMissingHalf(t *testing.T) {
	t.Parallel()

	// Live present, bridged absent.
	acc := &fakeAccessor{providers: map[string]tfprovider.Provider{
		"registry.opentofu.org/hashicorp/aws": stubProvider{},
	}}
	pair := resolveInjectionProviders(t.Context(), acc, nil,
		"registry.opentofu.org/hashicorp/aws", "aws_x")
	assert.Contains(t, pair.MissingReason, "no bridged Pulumi provider",
		"whole-provider absence names the provider, not a resource type")

	// Bridged present, live absent.
	pair = resolveInjectionProviders(t.Context(), &fakeAccessor{},
		bridgedProvidersFor("registry.opentofu.org/hashicorp/aws"),
		"registry.opentofu.org/hashicorp/aws", "aws_x")
	assert.Contains(t, pair.MissingReason, "no open provider")

	// A bridged provider that does not know this resource type is the
	// loader-disagreement case, and says so.
	pair = resolveInjectionProviders(t.Context(), acc,
		bridgedProvidersFor("registry.opentofu.org/hashicorp/aws"),
		"registry.opentofu.org/hashicorp/aws", "aws_unknown_type")
	assert.Contains(t, pair.MissingReason, "has no schema for resource type aws_unknown_type",
		"resource-type absence is distinguishable from provider absence")
}

// The equivalence rules are pinned in pkg/provideraddr; this pins only that
// the local alias delegates there.
func TestEquivalentProviderAddrs_Delegates(t *testing.T) {
	t.Parallel()

	assert.Equal(t,
		[]string{"registry.terraform.io/hashicorp/aws", "registry.opentofu.org/hashicorp/aws", "hashicorp/aws"},
		equivalentProviderAddrs("registry.terraform.io/hashicorp/aws"))
}
