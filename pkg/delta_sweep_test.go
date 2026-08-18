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

//go:build deltasweep

package pkg

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"sort"
	"testing"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg/importsupport"
	"github.com/pulumi-proserv/pulumi-tool-import/pkg/providermap"
	"github.com/pulumi-proserv/pulumi-tool-import/pkg/tfprovider"
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

// The delta sweep runs every NON-IMPORTABLE resource type in a provider through
// the same pipeline production uses, and requires the recovered raw state to
// equal what went in.
//
// Behind a build tag because it probes hundreds of resource types and takes
// minutes. It needs the provider binary but NO cloud credentials:
//
//	go test -tags deltasweep ./pkg/ -run TestDeltaSweep -v -timeout 30m
//
// Non-importable types are the population that matters: they are the only ones
// this tool computes deltas for. Everything else gets its delta from the bridge
// during "pulumi import".
//
// Two rules this sweep must follow, both learned by getting them wrong in
// smaller tests where the damage was visible:
//
//  1. Decode with UseNumber on BOTH sides. A plain decode turns large integers
//     into float64 on both, so the comparison silently agrees about a value
//     neither side represents correctly.
//  2. Report WHICH attribute differs, never a boolean. An earlier prototype
//     reported "5 mismatches of 10 types"; careful per-attribute comparison of
//     one of them showed 0 differing attributes of 23. The finding was an
//     artifact of the comparator and was withdrawn. A sweep that cries wolf is
//     worse than no sweep.
//
// It also accounts for what it SKIPPED. A sweep that silently drops the types
// it could not build a value for reads as coverage it does not have.

var sweepLimit = flag.Int("delta-sweep-limit", 0,
	"maximum non-importable resource types to sweep (0 = no limit)")

type sweepOutcome struct {
	noBridgedSchema []string
	valueBuildFail  []string
	computeFail     []string
	recoverFail     []string
	mismatch        []string
	ok              []string
}

func TestDeltaSweep(t *testing.T) {
	ctx := context.Background()

	const addr = nestedBlockTestAWSProviderAddr
	const version = nestedBlockTestAWSProviderVersion

	prov, err := tfprovider.LoadProvider(ctx, addr, version)
	if err != nil {
		t.Fatalf("provider unavailable: %v", err)
	}
	defer prov.Close(ctx)

	pulumiProviders, err := PulumiProvidersForTerraformProviders(
		[]providermap.TerraformProviderName{addr}, map[string]string{addr: version})
	if err != nil {
		t.Fatalf("could not bridge provider schema: %v", err)
	}
	pwm := pulumiProviders[providermap.TerraformProviderName(addr)]

	schemas := prov.GetProviderSchema(ctx)
	types := make([]string, 0, len(schemas.ResourceTypes))
	for typ := range schemas.ResourceTypes {
		types = append(types, typ)
	}
	sort.Strings(types)

	prober := importsupport.NewProber(map[string]string{addr: version})
	defer prober.Close(ctx)

	var nonImportable []string
	for _, typ := range types {
		if prober.Check(ctx, addr, typ) == importsupport.Unsupported {
			nonImportable = append(nonImportable, typ)
		}
	}
	t.Logf("%d of %d resource types are non-importable", len(nonImportable), len(types))
	if *sweepLimit > 0 && len(nonImportable) > *sweepLimit {
		t.Logf("limiting to %d types (--delta-sweep-limit)", *sweepLimit)
		nonImportable = nonImportable[:*sweepLimit]
	}

	var out sweepOutcome
	for _, typ := range nonImportable {
		sweepOne(ctx, t, prov, pwm, typ, schemas.ResourceTypes[typ].Block.ImpliedType(), &out)
	}

	// Report everything, including what was not exercised.
	t.Logf("swept %d non-importable types: %d ok, %d mismatch, %d recover-fail, "+
		"%d compute-fail, %d unbuildable value, %d no bridged schema",
		len(nonImportable), len(out.ok), len(out.mismatch), len(out.recoverFail),
		len(out.computeFail), len(out.valueBuildFail), len(out.noBridgedSchema))

	for _, label := range []struct {
		name string
		list []string
	}{
		{"NO BRIDGED SCHEMA (not exercised)", out.noBridgedSchema},
		{"VALUE COULD NOT BE BUILT (not exercised)", out.valueBuildFail},
		{"DELTA NOT COMPUTED", out.computeFail},
		{"RECOVER FAILED", out.recoverFail},
		{"MISMATCH", out.mismatch},
	} {
		if len(label.list) == 0 {
			continue
		}
		shown := label.list
		if len(shown) > 25 {
			shown = shown[:25]
		}
		t.Logf("%s (%d): %v%s", label.name, len(label.list), shown,
			map[bool]string{true: " ...", false: ""}[len(label.list) > 25])
	}

	// Only a genuine reconstruction difference fails the sweep. A type whose
	// value could not be built, or which has no bridged schema, was not
	// exercised — counted and named above, but not treated as a defect.
	if len(out.mismatch) > 0 || len(out.recoverFail) > 0 {
		t.Errorf("%d type(s) did not reconstruct their raw state exactly and %d failed to recover",
			len(out.mismatch), len(out.recoverFail))
	}
}

func sweepOne(
	ctx context.Context, t *testing.T, prov tfprovider.Provider,
	pwm *ProviderWithMetadata, typ string, ity cty.Type, out *sweepOutcome,
) {
	t.Helper()

	shimRes := pwm.P.ResourcesMap().Get(typ)
	if shimRes == nil {
		out.noBridgedSchema = append(out.noBridgedSchema, typ)
		return
	}
	var infos map[string]*tfbridge.SchemaInfo
	if ri := pwm.Resources[typ]; ri != nil {
		infos = ri.Fields
	}

	attrs, err := sweepValue(ity)
	if err != nil {
		out.valueBuildFail = append(out.valueBuildFail, typ)
		return
	}

	outputs, delta, reason, _, err := ComputeInjectionState(ctx, prov, typ, attrs, shimRes.Schema(), infos)
	if err != nil || reason != "" {
		out.computeFail = append(out.computeFail, typ)
		return
	}

	outputsJSON, err := json.Marshal(outputs)
	if err != nil {
		out.computeFail = append(out.computeFail, typ)
		return
	}
	deltaJSON, err := json.Marshal(delta)
	if err != nil {
		out.computeFail = append(out.computeFail, typ)
		return
	}

	// The production converters, deliberately. propertyValueFromState is the
	// only one with a json.Number case; NewPropertyMapFromMap turns every
	// UseNumber-decoded number into a String property, which made this sweep
	// report three false mismatches on numeric attributes before it was fixed.
	rsd, err := tfbridge.UnmarshalRawStateDelta(deltaPropertyValue(decodeExact(t, deltaJSON)))
	if err != nil {
		out.recoverFail = append(out.recoverFail, typ)
		return
	}
	recovered, err := rsd.Recover(propertyValueFromState(decodeExact(t, outputsJSON)))
	if err != nil {
		out.recoverFail = append(out.recoverFail, fmt.Sprintf("%s (%v)", typ, err))
		return
	}

	if diffs := diffAttributes(decodeExact(t, attrs), decodeExact(t, recovered)); len(diffs) > 0 {
		out.mismatch = append(out.mismatch, fmt.Sprintf("%s%v", typ, diffs))
		return
	}
	out.ok = append(out.ok, typ)
}

// sweepValue builds a populated value for a resource type from its implied
// type. Populated, not all-null: an all-null value exercises the walk and
// almost nothing else, so it would report success without testing naming,
// pluralization or element handling.
func sweepValue(ity cty.Type) ([]byte, error) {
	if !ity.IsObjectType() {
		return nil, fmt.Errorf("not an object type")
	}
	vals := map[string]cty.Value{}
	for name, at := range ity.AttributeTypes() {
		// "timeouts" is left null deliberately. ComputeInjectionState strips it
		// from both the type and the Pulumi outputs — mirroring what the sdk-v2
		// shim does in production, where no bridged resource's props ever
		// carries it — so a populated timeouts block would make every type with
		// one report a difference the pipeline creates ON PURPOSE. That
		// behaviour has its own dedicated coverage in
		// TestComputeInjectionState_TimeoutsDeltaRecovers, including the
		// populated case; it does not belong in a mismatch count.
		if name == "timeouts" {
			vals[name] = cty.NullVal(at)
			continue
		}
		vals[name] = sampleValue(at)
	}
	if _, hasID := vals["id"]; hasID {
		vals["id"] = cty.StringVal("sweep-id")
	}
	return ctyjson.Marshal(cty.ObjectVal(vals), ity)
}

// sampleValue returns a representative non-null value for a cty type, falling
// back to null for shapes it cannot populate meaningfully.
func sampleValue(t cty.Type) cty.Value {
	switch {
	case t == cty.String:
		return cty.StringVal("sample")
	case t == cty.Number:
		return cty.NumberIntVal(7)
	case t == cty.Bool:
		return cty.False
	case t.IsListType():
		return cty.ListVal([]cty.Value{sampleValue(t.ElementType())})
	case t.IsSetType():
		return cty.SetVal([]cty.Value{sampleValue(t.ElementType())})
	case t.IsMapType():
		return cty.MapVal(map[string]cty.Value{"k": sampleValue(t.ElementType())})
	case t.IsObjectType():
		vals := map[string]cty.Value{}
		for n, at := range t.AttributeTypes() {
			vals[n] = sampleValue(at)
		}
		if len(vals) == 0 {
			return cty.NullVal(t)
		}
		return cty.ObjectVal(vals)
	default:
		// DynamicPseudoType, tuples and anything else: null rather than a
		// guess. Counted as swept, but honestly not exercised deeply.
		return cty.NullVal(t)
	}
}

// diffAttributes names the attributes that differ, rather than returning a
// boolean. Recovered raw state is schema-complete, so an attribute the original
// omitted is only a difference if it came back non-null.
func diffAttributes(want, got map[string]interface{}) []string {
	var diffs []string
	for k, w := range want {
		if k == "timeouts" {
			continue // stripped by design; see sweepValue
		}
		g, present := got[k]
		if !present {
			diffs = append(diffs, k+":missing")
			continue
		}
		wj, _ := json.Marshal(w)
		gj, _ := json.Marshal(g)
		if string(wj) != string(gj) {
			diffs = append(diffs, k)
		}
	}
	for k, g := range got {
		if _, inWant := want[k]; inWant {
			continue
		}
		if g != nil {
			diffs = append(diffs, k+":invented")
		}
	}
	sort.Strings(diffs)
	return diffs
}
