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

func ComputeInjectionState(
	ctx context.Context,
	prov tfprovider.Provider,
	tfType string,
	attrsJSON []byte,
	schemaMap shim.SchemaMap,
	schemaInfos map[string]*tfbridge.SchemaInfo,
) (outputs map[string]interface{}, delta map[string]interface{}, deltaUnavailableReason string, version int64, err error) { //nolint:lll
	schemas := prov.GetProviderSchema(ctx)
	sch, ok := schemas.ResourceTypes[tfType]
	if !ok {
		return nil, nil, "", 0, fmt.Errorf("provider has no schema for %s", tfType)
	}

	ty := sch.Block.ImpliedType()
	value, err := ctyjson.Unmarshal(attrsJSON, ty)
	if err != nil {
		return nil, nil, "", 0, fmt.Errorf("decoding %s attributes: %w", tfType, err)
	}

	strippedType := stripTimeouts(ty)

	props, err := pulumiOutputsFromCty(ctx, value, schemaMap, schemaInfos)
	if err != nil {
		return nil, nil, "", 0, fmt.Errorf("converting %s attributes to Pulumi outputs: %w", tfType, err)
	}
	delete(props, "timeouts")
	outputs = props.Mappable()

	if resource.NewObjectProperty(props).ContainsUnknowns() {
		return outputs, nil, fmt.Sprintf(
			"raw state delta for %s skipped: outputs contain unknown values, which the bridge's "+
				"delta computation cannot process", tfType), sch.Version, nil
	}

	rawDelta, deltaErr := tfbridge.RawStateComputeDelta(ctx, schemaMap, schemaInfos,
		props,
		valueshim.FromCtyType(strippedType),
		valueshim.FromCtyValue(value))
	if deltaErr != nil {
		return outputs, nil, fmt.Sprintf("computing raw state delta for %s: %v", tfType, deltaErr), sch.Version, nil
	}

	deltaJSON, err := json.Marshal(rawDelta)
	if err != nil {
		return outputs, nil, fmt.Sprintf("marshalling raw state delta for %s: %v", tfType, err),
			sch.Version, nil
	}
	marshalled, err := decodeAttrs(deltaJSON)
	if err != nil {
		return outputs, nil, fmt.Sprintf("decoding raw state delta for %s: %v", tfType, err),
			sch.Version, nil
	}
	if marshalled == nil {
		return outputs, nil, fmt.Sprintf("raw state delta for %s computed empty despite no error", tfType),
			sch.Version, nil
	}

	return outputs, marshalled, "", sch.Version, nil
}

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

type noSetChecker struct{}

func (noSetChecker) IsSet(context.Context, interface{}) ([]interface{}, bool) {
	return nil, false
}

func stripTimeouts(t cty.Type) cty.Type {
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
