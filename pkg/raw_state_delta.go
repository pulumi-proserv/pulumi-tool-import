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
//
// The returned deltaUnavailableReason is non-empty exactly when delta is nil
// but err is nil: RawStateComputeDelta was reached and ran, but could not
// produce a delta for this value. That is not fatal — the caller still gets
// outputs and a schema version, and injects the resource without a delta,
// falling back to the bridge's pre-delta reconstruction — but the reason is
// worth keeping rather than discarding, since "no delta" and "no delta and we
// don't know why" look identical to everything downstream unless the reason
// is threaded through.
//
// err, by contrast, means nothing here could be computed at all (no schema
// for tfType, attrsJSON does not decode, or the attributes could not convert
// to Pulumi outputs) — the caller gets no outputs, no delta, and no version.
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

	// RawStateComputeDelta (tfbridge/rawstate.go) special-cases the "timeouts"
	// attribute in two places:
	//
	//   - Its own value/type walk unconditionally treats a top-level
	//     "timeouts" path as an empty, already-handled Object node, so the
	//     type it is handed for the walk must not declare "timeouts" either
	//     (mirroring what the bridge's own valueshim.FromHctyResourceType
	//     does when building a resource's SchemaType() for every real
	//     caller). strippedType does that.
	//
	//   - Separately, it inspects the Pulumi outputs (props) it is handed:
	//     if props has a "timeouts" key at all, it treats timeouts as part
	//     of the resource's real data model and preserves it verbatim in the
	//     raw state; otherwise it strips "timeouts" from the raw state value
	//     itself (v.Remove("timeouts")) before encoding.
	//
	// The second branch (props carrying "timeouts") turns out to be
	// unreachable for the sdk-v2 flavour this tool imports through, and not
	// just for our own inputs: the bridge's own production instance-state
	// path (sdk-v2's (*v2InstanceState2).Object, tfshim/sdk-v2/provider2.go)
	// unconditionally deletes "timeouts" from the map it hands to
	// MakeTerraformResult, with the comment "grpc servers add a timeouts key
	// to compensate for infinite diffs; this is not needed in the Pulumi
	// projection." So no real caller's props ever carries "timeouts",
	// populated or not -- confirmed empirically here too: keeping "timeouts"
	// in props when it is populated (rather than always excluding it) makes
	// RawStateComputeDelta take the "part of the data model" branch, which
	// calls v.Marshal(schemaType) -- but schemaType is (and, per
	// SchemaType() above, always is for every real caller) the type with
	// "timeouts" already stripped, so cty's own JSON marshaller silently
	// drops the attribute from the encoded raw state while props/pv still
	// carry it, and the turnaround self-check then fails with "recovered raw
	// state does not byte-for-byte match the original raw state". So props
	// excludes "timeouts" unconditionally, matching upstream, and timeouts
	// data -- populated or not -- is never preserved into the injected
	// resource; that already matches how every other bridged resource
	// behaves, not a regression this tool introduces.
	strippedType := stripTimeouts(ty)

	props, err := pulumiOutputsFromCty(ctx, value, schemaMap, schemaInfos)
	if err != nil {
		return nil, nil, "", 0, fmt.Errorf("converting %s attributes to Pulumi outputs: %w", tfType, err)
	}
	delete(props, "timeouts")
	outputs = props.Mappable()

	// Mirrors the bridge's own first act in RawStateInjectDelta: bail before
	// computing a delta when the property map contains unknowns, because "the
	// code for deltas cannot process unknowns" (its words).
	//
	// UNREACHABLE TODAY, and deliberately kept anyway. pulumiOutputsFromCty
	// above starts with ctyjson.Marshal, which rejects an unknown value
	// outright ("value is not known" — measured), so an unknown cannot survive
	// to this point and this branch cannot currently fire. The guard exists
	// because that protection is incidental: it belongs to a conversion step
	// that could reasonably be rewritten, and nothing about it announces that
	// a delta invariant depends on it.
	//
	// Worth the three lines because "unknowns cannot reach here" was exactly
	// the claim that proved false for injected INPUTS — a resource referencing
	// another injected resource carries the engine's unknown sentinel, which
	// the code asserted was unreachable "by construction". Different path, same
	// class of assumption. Here the degradation is a resource without a delta,
	// rather than a delta computed from a placeholder.
	if resource.NewObjectProperty(props).ContainsUnknowns() {
		return outputs, nil, fmt.Sprintf(
			"raw state delta for %s skipped: outputs contain unknown values, which the bridge's "+
				"delta computation cannot process", tfType), sch.Version, nil
	}

	// The cty value itself is passed through unstripped: props has no
	// "timeouts" key (above), so RawStateComputeDelta removes "timeouts" from
	// the value on our behalf (v.Remove("timeouts")) before encoding raw
	// state, the same way it does for every real bridge caller -- stripping
	// it here too would be redundant. Confirmed empirically: a separate
	// value-side strip is not needed once the props-side strip above is
	// unconditional.
	rawDelta, deltaErr := tfbridge.RawStateComputeDelta(ctx, schemaMap, schemaInfos,
		props,
		valueshim.FromCtyType(strippedType),
		valueshim.FromCtyValue(value))
	if deltaErr != nil {
		// A delta that cannot be computed is not fatal: the resource is injected
		// without one and falls back to the bridge's pre-delta reconstruction.
		return outputs, nil, fmt.Sprintf("computing raw state delta for %s: %v", tfType, deltaErr), sch.Version, nil
	}

	// Marshalled by encoding/json directly, NOT via Marshal().Mappable().
	//
	// The bridge secrets every Replace node — rawstate.go returns
	// resource.MakeSecret(...) for any map carrying a "replace" key — and
	// PropertyValue.MapRepl, which Mappable calls, returns v.SecretValue() for
	// a secret: a *resource.Secret STRUCT, not a JSON shape. So Mappable
	// produced {"Element":{"V":{"replace":{"V":...}}}} instead of
	// {"replace":...}, with the SDK's internal field names baked into the
	// sidecar. Reading that back through UnmarshalRawStateDelta returned
	// err=nil and an EMPTY delta at that path, so the resource silently
	// recovered the natural encoding rather than the provider's exact bytes —
	// a wrong raw state written with no error anywhere, surfacing only as a
	// later phantom diff. The bridge's deliberate secret marking was lost too,
	// so the raw payload landed in state unencrypted.
	//
	// RawStateDelta's own JSON form is the canonical one: Marshal() itself
	// starts with json.Marshal(d) and only then converts to a PropertyValue.
	// Going straight to JSON keeps that shape and skips the lossy conversion.
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
		// A null delta. Unreachable for a whole-resource delta: the top-level
		// PropertyValue handed to RawStateComputeDelta is always an Object (a
		// resource's outputs are always a property map), and
		// rawStateRecoverNatural — the only path that produces an empty delta —
		// refuses Object values outright. Kept as a defensive fallback rather
		// than a panic, in case a future bridge version changes that.
		return outputs, nil, fmt.Sprintf("raw state delta for %s computed empty despite no error", tfType),
			sch.Version, nil
	}

	return outputs, marshalled, "", sch.Version, nil
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

// stripTimeouts removes the timeouts attribute, the way the bridge's own
// FromHctyResourceType does for the hcty flavour (valueshim.FromHctyResourceType).
// There is no zclconf equivalent, so it is replicated here.
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
