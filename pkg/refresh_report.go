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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
)

// The refresh report is the one check that consults the deployed resource
// (#41). Injection's values come from the Terraform state file, and the
// verifying preview compares the program against the injected state — neither
// ever reads live. "pulumi refresh --preview-only --json" calls the provider's
// Read and reports both oldState and newState per resource, without writing
// anything.
//
// It is a report, never a gate. A diff has three possible causes — stale
// Terraform state, a wrong program, or the provider's Read normalising values
// — and the tool cannot tell them apart, so an automatic verdict would be a
// guess. And "no diff" is not confirmation: for the types this feature exists
// for, Read may return exactly what it was given (see
// docs/non-importable-resources.md, "Verify with preview, not refresh"), so
// silence means "learned nothing", and the report says so rather than letting
// it read as success.

// RefreshStep is one resource in a "pulumi refresh --preview-only --json" run.
type RefreshStep struct {
	Op       string                 `json:"op"`
	URN      string                 `json:"urn"`
	OldState map[string]interface{} `json:"oldState"`
	NewState map[string]interface{} `json:"newState"`
}

// RefreshDigest is the parsed output of "pulumi refresh --preview-only --json".
type RefreshDigest struct {
	Steps []RefreshStep `json:"steps"`
}

// ParseRefreshJSON parses "pulumi refresh --preview-only --json" output.
func ParseRefreshJSON(data []byte) (*RefreshDigest, error) {
	var digest RefreshDigest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&digest); err != nil {
		return nil, fmt.Errorf("parsing refresh preview JSON: %w", err)
	}
	return &digest, nil
}

// refreshPreviewJSONArgs builds the CLI args for the refresh report run.
// --preview-only is load-bearing: a real refresh can DELETE an injected
// resource from state when Read reports it gone, resurrecting the destructive
// create this feature exists to prevent. This path must never write.
func refreshPreviewJSONArgs(stackName string) []string {
	return []string{"refresh", "--preview-only", "--json", "--stack", stackName}
}

// RefreshPreviewJSON runs "pulumi refresh --preview-only --json" and parses
// the result. Like PreviewJSON, it shells out: the Automation API's
// PreviewRefresh tails an event stream that does not carry per-resource
// old/new state.
func (s *StackSession) RefreshPreviewJSON(ctx context.Context) (*RefreshDigest, error) {
	stdout, stderr, code, err := s.stack.Workspace().PulumiCommand().Run(
		ctx, s.projectDir, nil, nil, nil, nil, refreshPreviewJSONArgs(s.stackName)...)
	if err != nil || code != 0 {
		return nil, fmt.Errorf("pulumi refresh --preview-only failed (exit %d): %w\n%s", code, err, stderr)
	}
	return ParseRefreshJSON([]byte(stdout))
}

const refreshValueMax = 80

// BuildRefreshReport renders, per injected URN, what the provider's Read
// returned versus what was injected. Empty result means every injected
// resource reported "no change" — which the caller must still present as
// "learned nothing new", never as confirmation.
func BuildRefreshReport(digest *RefreshDigest, injectedURNs []string) []string {
	byURN := make(map[string]RefreshStep, len(digest.Steps))
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
			continue
		case "same":
			// Fall through to the property diff: an op of "same" with byte-equal
			// states is the no-change case; anything else is still worth naming.
		}
		diffs := diffOutputs(outputsOf(step.OldState), outputsOf(step.NewState))
		if len(diffs) == 0 {
			lines = append(lines, fmt.Sprintf(
				"%s: no change reported. This is agreement only if this type's Read consults the "+
					"API; a Read that returns its input reports the same, so nothing was learned", urn))
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: live disagrees with the injected state:", urn))
		lines = append(lines, diffs...)
	}
	return lines
}

func outputsOf(state map[string]interface{}) map[string]interface{} {
	if state == nil {
		return nil
	}
	outs, _ := state["outputs"].(map[string]interface{})
	return outs
}

// diffOutputs names each top-level property whose value differs, with both
// values (truncated — the CLI already masks secrets as [secret], and a report
// line must stay a line).
func diffOutputs(old, live map[string]interface{}) []string {
	keys := map[string]bool{}
	for k := range old {
		keys[k] = true
	}
	for k := range live {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		if k == rawStateDeltaKey || k == metaKey {
			continue
		}
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)

	var diffs []string
	for _, k := range sorted {
		ov, oOK := old[k]
		lv, lOK := live[k]
		if oOK && lOK && reflect.DeepEqual(ov, lv) {
			continue
		}
		diffs = append(diffs, fmt.Sprintf("  %s: state=%s live=%s",
			k, renderRefreshValue(ov, oOK), renderRefreshValue(lv, lOK)))
	}
	return diffs
}

func renderRefreshValue(v interface{}, present bool) string {
	if !present {
		return "(absent)"
	}
	s := fmt.Sprintf("%v", v)
	if len(s) > refreshValueMax {
		s = s[:refreshValueMax] + "…"
	}
	return s
}
