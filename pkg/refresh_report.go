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
	"fmt"
	"reflect"
	"sort"
)

// maxRenderedValueRunes bounds one rendered property value in a report line;
// the CLI has already masked state-marked secrets as [secret].
const maxRenderedValueRunes = 80

// maxDiffLinesPerResource bounds per-resource property diffs, matching
// formatDiffReasons' cap: a widely-normalising Read must not bury the GONE
// lines.
const maxDiffLinesPerResource = 8

// BuildRefreshReport renders, per injected URN, what the provider's Read said
// about it — the one check in the pipeline that consults the deployed
// resource. It is a report, never a gate: a diff has three causes the tool
// cannot distinguish (stale Terraform state, a wrong program, Read
// normalisation), so the operator adjudicates; and "no diff" is not
// confirmation, since for many non-importable types Read returns exactly what
// it was given. Every injected URN gets at least one line — silence is never
// allowed to read as success — so the result is empty only when injectedURNs
// is empty.
//
// The digest's op vocabulary, verified against the pinned pulumi/pkg source:
// a refresh-preview step is recorded from the pre-event with op "refresh" and
// newState a pre-Read COPY of oldState; the CLI rewrites the step only when
// the result is an update with a detailed diff, or a delete. So a property
// diff is computable only on "update" steps — an op "refresh" step carries no
// live values, and the report says "no diff reported" rather than pretending
// it compared anything.
func BuildRefreshReport(digest *PreviewDigest, injectedURNs []string) []string {
	byURN := make(map[string]PreviewStep, len(digest.Steps))
	for _, step := range digest.Steps {
		byURN[step.URN] = step
	}

	var lines []string
	for _, urn := range injectedURNs {
		step, ok := byURN[urn]
		if !ok {
			lines = append(lines, fmt.Sprintf(
				"%s: not reported by the refresh preview at all — nothing was learned about it", urn))
			continue
		}
		switch step.Op {
		case "delete":
			lines = append(lines, fmt.Sprintf(
				"%s: GONE — the provider's Read says the injected ID resolves to nothing that exists. "+
					"The next \"pulumi up\" would create it; if the ID names the wrong live object, "+
					"fix the ID before running anything", urn))
		case "update":
			outputs, hasOutputs := outputsOf(step.NewState)
			old, _ := outputsOf(step.OldState)
			if !hasOutputs {
				lines = append(lines, fmt.Sprintf(
					"%s: the provider reported a diff, but the step carried no outputs to compare — "+
						"inspect with \"pulumi refresh --preview-only --diff\"", urn))
				continue
			}
			diffs := diffOutputs(old, outputs, idOf(step.OldState), idOf(step.NewState))
			if len(diffs) == 0 {
				lines = append(lines, fmt.Sprintf(
					"%s: the provider reported a diff the outputs comparison cannot see (inputs or "+
						"metadata) — inspect with \"pulumi refresh --preview-only --diff\"", urn))
				continue
			}
			lines = append(lines, fmt.Sprintf("%s: live disagrees with the injected state:", urn))
			lines = append(lines, diffs...)
		default:
			// "refresh" (and any op this vocabulary gains): the provider's
			// Read reported no diff, and the step's newState is a pre-Read
			// copy of oldState, so there are no live values to compare here.
			lines = append(lines, fmt.Sprintf(
				"%s: no diff reported. This confirms only that the ID resolves; for a type whose "+
					"Read returns its input, nothing else was learned", urn))
		}
	}
	return lines
}

func outputsOf(state map[string]interface{}) (map[string]interface{}, bool) {
	if state == nil {
		return nil, false
	}
	outs, ok := state["outputs"].(map[string]interface{})
	return outs, ok
}

func idOf(state map[string]interface{}) string {
	if state == nil {
		return ""
	}
	id, _ := state["id"].(string)
	return id
}

// diffOutputs names each top-level property whose value differs between the
// injected outputs and the provider's Read, plus the ID — the one value the
// docs identify as genuinely checked against the cloud for every type. Lines
// come back fully formed (indentation included), capped at
// maxDiffLinesPerResource. An array differing only in element order is
// annotated as such rather than dumped: Read commonly reorders multi-valued
// properties, and that noise must not train the operator to skim.
func diffOutputs(injected, live map[string]interface{}, injectedID, liveID string) []string {
	var diffs []string
	if injectedID != liveID && liveID != "" {
		diffs = append(diffs, fmt.Sprintf("    id: injected=%s live=%s",
			renderRefreshValue(injectedID, true), renderRefreshValue(liveID, true)))
	}

	keys := map[string]bool{}
	for k := range injected {
		keys[k] = true
	}
	for k := range live {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		if isReservedOutputKey(k) {
			continue
		}
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)

	total := 0
	for _, k := range sorted {
		iv, iOK := injected[k]
		lv, lOK := live[k]
		if iOK && lOK && reflect.DeepEqual(iv, lv) {
			continue
		}
		total++
		if len(diffs) >= maxDiffLinesPerResource {
			continue
		}
		if iOK && lOK && equalIgnoringOrder(iv, lv) {
			diffs = append(diffs, fmt.Sprintf("    %s: differs only in element order", k))
			continue
		}
		diffs = append(diffs, fmt.Sprintf("    %s: injected=%s live=%s",
			k, renderRefreshValue(iv, iOK), renderRefreshValue(lv, lOK)))
	}
	if extra := total - maxDiffLinesPerResource; extra > 0 {
		diffs = append(diffs, fmt.Sprintf("    … and %d more propert%s", extra,
			map[bool]string{true: "y", false: "ies"}[extra == 1]))
	}
	return diffs
}

// equalIgnoringOrder reports whether two values are lists of scalars with the
// same elements in a different order.
func equalIgnoringOrder(a, b interface{}) bool {
	as, aOK := a.([]interface{})
	bs, bOK := b.([]interface{})
	if !aOK || !bOK || len(as) != len(bs) {
		return false
	}
	render := func(vs []interface{}) []string {
		out := make([]string, 0, len(vs))
		for _, v := range vs {
			switch v.(type) {
			case map[string]interface{}, []interface{}:
				return nil // only scalar lists are order-compared
			}
			out = append(out, fmt.Sprintf("%v", v))
		}
		sort.Strings(out)
		return out
	}
	ra, rb := render(as), render(bs)
	return ra != nil && rb != nil && reflect.DeepEqual(ra, rb)
}

func renderRefreshValue(v interface{}, present bool) string {
	if !present {
		return "(absent)"
	}
	s := fmt.Sprintf("%v", v)
	if r := []rune(s); len(r) > maxRenderedValueRunes {
		s = string(r[:maxRenderedValueRunes]) + "…"
	}
	return s
}
