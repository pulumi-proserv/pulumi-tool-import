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

func sweepValue(ity cty.Type) ([]byte, error) {
	if !ity.IsObjectType() {
		return nil, fmt.Errorf("not an object type")
	}
	vals := map[string]cty.Value{}
	for name, at := range ity.AttributeTypes() {
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
		return cty.NullVal(t)
	}
}

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
