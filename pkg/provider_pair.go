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
	"strings"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg/providermap"
	"github.com/pulumi-proserv/pulumi-tool-import/pkg/tfprovider"
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge"
	shim "github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfshim"
)

// InjectionProviderPair is the declared relationship between "digest tf"'s two
// provider loaders (issue #26). Neither can replace the other — the live
// Terraform provider knows cty types and schema versions but no Pulumi names;
// the bridged schema knows the mapping but its mock carries no instance state —
// so computing injection state needs both, and this is the one place that
// resolves the pair and names which half is missing.
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

// equivalentProviderAddrs returns addr plus its other-registry form: terraform
// writes registry.terraform.io where tofu writes registry.opentofu.org for the
// same provider, and the two loaders key their maps from different sources
// (the lock file vs state addresses), so a mixed terraform/tofu history can
// name one provider both ways in one run.
func equivalentProviderAddrs(addr string) []string {
	const tfHost, tofuHost = "registry.terraform.io/", "registry.opentofu.org/"
	switch {
	case strings.HasPrefix(addr, tfHost):
		return []string{addr, tofuHost + strings.TrimPrefix(addr, tfHost)}
	case strings.HasPrefix(addr, tofuHost):
		return []string{addr, tfHost + strings.TrimPrefix(addr, tofuHost)}
	default:
		return []string{addr}
	}
}

func resolveInjectionProviders(
	ctx context.Context,
	accessor ProviderAccessor,
	pulumiProviders map[providermap.TerraformProviderName]*ProviderWithMetadata,
	providerName string,
	resourceType string,
) InjectionProviderPair {
	var pair InjectionProviderPair

	for _, addr := range equivalentProviderAddrs(providerName) {
		if prov, ok := accessor.Provider(ctx, addr); ok {
			pair.Live = prov
			break
		}
	}
	if pair.Live == nil {
		pair.MissingReason = fmt.Sprintf(
			"the import-support probe holds no open provider for %s", providerName)
		return pair
	}

	var pwm *ProviderWithMetadata
	for _, addr := range equivalentProviderAddrs(providerName) {
		if p, ok := pulumiProviders[providermap.TerraformProviderName(addr)]; ok && p != nil {
			pwm = p
			break
		}
	}
	if pwm == nil {
		pair.MissingReason = fmt.Sprintf(
			"no bridged Pulumi schema for provider %s, though the import-support probe loaded it: "+
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
			"no bridged Pulumi schema for %s, though the import-support probe loaded %s: "+
				"the provider loaders disagree (see issue #26)", resourceType, providerName)
	}
	return pair
}
