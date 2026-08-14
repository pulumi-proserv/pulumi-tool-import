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
	"fmt"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg/tfprovider"
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge"
	shim "github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfshim"
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/valueshim"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

// ComputeInjectionState converts a resource's recorded Terraform attributes
// into the Pulumi outputs, raw state delta, and schema version that injecting
// it into state requires.
//
// This runs during "digest tf" because it needs a live Terraform provider: the
// cty type comes from the provider's schema, and there is no way to get it from
// the schema mock the rest of the tool uses. "digest tf" already starts
// providers for the import-support probe, which is the same check that marks a
// resource non-importable, so nothing extra is launched.
//
// attrsJSON must be the redacted attributes. The delta can embed raw JSON, so
// computing it from real secret values would put them in the sidecar.
func ComputeInjectionState(
	ctx context.Context,
	prov tfprovider.Provider,
	tfType string,
	attrsJSON []byte,
	schemaMap shim.SchemaMap,
	schemaInfos map[string]*tfbridge.SchemaInfo,
) (map[string]interface{}, map[string]interface{}, int64, error) {
	schemas := prov.GetProviderSchema(ctx)
	sch, ok := schemas.ResourceTypes[tfType]
	if !ok {
		return nil, nil, 0, fmt.Errorf("provider has no schema for %s", tfType)
	}

	ty := sch.Block.ImpliedType()
	value, err := ctyjson.Unmarshal(attrsJSON, ty)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("decoding %s attributes: %w", tfType, err)
	}

	props, err := pulumiOutputsFromCty(ctx, value, schemaMap, schemaInfos)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("converting %s attributes to Pulumi outputs: %w", tfType, err)
	}
	outputs := props.Mappable()

	delta, err := tfbridge.RawStateComputeDelta(ctx, schemaMap, schemaInfos,
		props,
		valueshim.FromCtyType(resourceType(ty)),
		valueshim.FromCtyValue(value))
	if err != nil {
		// A delta that cannot be computed is not fatal: the resource is injected
		// without one and falls back to the bridge's pre-delta reconstruction.
		return outputs, nil, sch.Version, nil
	}

	marshalled, ok := delta.Marshal().Mappable().(map[string]interface{})
	if !ok {
		return outputs, nil, sch.Version, nil
	}

	return outputs, marshalled, sch.Version, nil
}

// pulumiOutputsFromCty converts a cty value into the Pulumi property map the
// bridge would have produced reading this resource, using the real
// schema-aware conversion (tfbridge.MakeTerraformOutputs) rather than a flat
// name mapping. RawStateComputeDelta recovers the raw state by replaying the
// same schema-driven naming and MaxItems=1 pluralization at every nested
// path, so the outputs handed to it must be named and shaped exactly as that
// conversion would produce them, not just at the top level.
func pulumiOutputsFromCty(
	ctx context.Context,
	value cty.Value,
	schemaMap shim.SchemaMap,
	schemaInfos map[string]*tfbridge.SchemaInfo,
) (resource.PropertyMap, error) {
	data, err := ctyjson.Marshal(value, value.Type())
	if err != nil {
		return nil, fmt.Errorf("marshalling cty value to JSON: %w", err)
	}

	var attrs map[string]interface{}
	if err := json.Unmarshal(data, &attrs); err != nil {
		return nil, fmt.Errorf("unmarshalling attributes: %w", err)
	}

	return tfbridge.MakeTerraformOutputs(ctx, noSetChecker{}, attrs, schemaMap, schemaInfos, nil, false), nil
}

// noSetChecker tells tfbridge.MakeTerraformOutputs that no value is a
// Terraform SDK "Set" wrapper type, which is correct here: the values being
// converted are plain Go values decoded from JSON, not the SDK's typed set
// representation.
type noSetChecker struct{}

func (noSetChecker) IsSet(context.Context, interface{}) ([]interface{}, bool) {
	return nil, false
}

// resourceType strips the timeouts attribute, the way the bridge's own
// FromHctyResourceType does for the hcty flavour (valueshim.FromHctyResourceType).
// There is no zclconf equivalent, so it is replicated here.
func resourceType(t cty.Type) cty.Type {
	if !t.IsObjectType() {
		return t
	}
	attrs := make(map[string]cty.Type, len(t.AttributeTypes()))
	for k, v := range t.AttributeTypes() {
		attrs[k] = v
	}
	delete(attrs, "timeouts")
	return cty.Object(attrs)
}
