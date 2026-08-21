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

	"github.com/pulumi-proserv/pulumi-tool-import/pkg/provideraddr"
	"github.com/pulumi-proserv/pulumi-tool-import/pkg/providermap"
	"github.com/pulumi-proserv/pulumi-tool-import/pkg/tfprovider"
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge"
	shim "github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfshim"
)

// InjectionProviderPair is the relationship between "digest tf"'s two
// provider loaders. Neither can replace the other — the live Terraform
// provider knows cty types and schema versions but no Pulumi names; the
// bridged schema knows the mapping but carries no instance state — so
// computing injection state needs both.
type InjectionProviderPair struct {
	// Live is the running Terraform provider the import-support probe holds.
	Live tfprovider.Provider
	// SchemaMap and SchemaInfos are the bridged schema for the resource type.
	SchemaMap   shim.SchemaMap
	SchemaInfos map[string]*tfbridge.SchemaInfo
	// MissingReason is empty exactly when both halves resolved; otherwise it
	// names the missing half, for the sidecar's InjectionStateReason.
	MissingReason string
}

// equivalentProviderAddrs is provideraddr.Equivalents, aliased locally.
func equivalentProviderAddrs(addr string) []string {
	return provideraddr.Equivalents(addr)
}

// resolveInjectionProviders resolves both halves of the pair — the live
// Terraform provider and the bridged schema for resourceType — or names the
// missing half in MissingReason. It is the one place the pair is correlated.
func resolveInjectionProviders(
	ctx context.Context,
	accessor ProviderAccessor,
	pulumiProviders map[providermap.TerraformProviderName]*ProviderWithMetadata,
	providerName string,
	resourceType string,
) InjectionProviderPair {
	var pair InjectionProviderPair

	addrs := equivalentProviderAddrs(providerName)
	for _, addr := range addrs {
		if prov, ok := accessor.Provider(ctx, addr); ok && prov != nil {
			pair.Live = prov
			break
		}
	}
	if pair.Live == nil {
		pair.MissingReason = fmt.Sprintf(
			"the import-support probe holds no open provider for %s", providerName)
		return pair
	}

	pwm := lookupBridgedProvider(pulumiProviders, providerName)
	if pwm == nil {
		pair.MissingReason = fmt.Sprintf(
			"no bridged Pulumi provider for %s, though the import-support probe loaded it: "+
				"the provider loaders disagree (see issue #26)", providerName)
		return pair
	}
	if shimResource := pwm.P.ResourcesMap().Get(resourceType); shimResource != nil {
		pair.SchemaMap = shimResource.Schema()
	}
	if ri := pwm.Resources[resourceType]; ri != nil {
		pair.SchemaInfos = ri.Fields
	}
	if pair.SchemaMap == nil {
		pair.MissingReason = fmt.Sprintf(
			"bridged provider %s has no schema for resource type %s: "+
				"the provider loaders disagree (see issue #26)", providerName, resourceType)
	}
	return pair
}

// lookupBridgedProvider finds the bridged provider for an address, trying the
// equivalent registry forms and guarding every level a partially-populated
// entry can leave nil.
func lookupBridgedProvider(
	pulumiProviders map[providermap.TerraformProviderName]*ProviderWithMetadata,
	providerName string,
) *ProviderWithMetadata {
	for _, addr := range equivalentProviderAddrs(providerName) {
		if p, ok := pulumiProviders[providermap.TerraformProviderName(addr)]; ok &&
			p != nil && p.Provider != nil && p.P != nil {
			return p
		}
	}
	return nil
}
