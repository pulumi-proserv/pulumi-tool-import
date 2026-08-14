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
	"encoding/json"
	"fmt"
	"strings"
)

// PreviewKey identifies a resource by Pulumi type token and resource name. It
// is how a sidecar entry is matched to the preview step that describes it.
type PreviewKey struct {
	Type string
	Name string
}

// PropertyDiff mirrors the PropertyDiff type in pulumi/pkg/v3/display/json.go,
// decoding only the Kind field this tool surfaces in diagnostics.
type PropertyDiff struct {
	Kind string `json:"kind"`
}

// PreviewStep is one step from "pulumi preview --json". NewState is kept as a
// raw map rather than apitype.ResourceV3 so that every field is carried through
// verbatim — including ones this tool does not interpret — and so that numbers
// survive as json.Number.
//
// DiffReasons and DetailedDiff are decoded so that a verification failure can
// say *what* differed, not just that something did — pulumi/pkg/v3's own
// PreviewStep carries both alongside Op and URN, but the CLI otherwise
// discards them once printed.
type PreviewStep struct {
	Op       string                 `json:"op"`
	URN      string                 `json:"urn"`
	NewState map[string]interface{} `json:"newState"`
	// DiffReasons lists the property keys causing a diff (update steps only).
	DiffReasons []string `json:"diffReasons,omitempty"`
	// DetailedDiff maps property path to the kind of difference found there.
	DetailedDiff map[string]PropertyDiff `json:"detailedDiff,omitempty"`
}

// PreviewDigest is the "pulumi preview --json" document. It mirrors the
// previewDigest type in pulumi/pkg/v3/display/json.go, decoding only the fields
// this tool consumes.
type PreviewDigest struct {
	Steps         []PreviewStep  `json:"steps"`
	ChangeSummary map[string]int `json:"changeSummary"`
}

// ParsePreviewJSON decodes "pulumi preview --json" output.
//
// UseNumber is required: preview steps carry resource inputs, and without it a
// large integer such as an AWS account ID becomes a float64 that re-serializes
// as scientific notation, which Pulumi's state parser rejects.
func ParsePreviewJSON(data []byte) (*PreviewDigest, error) {
	var digest PreviewDigest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&digest); err != nil {
		return nil, fmt.Errorf("parsing preview JSON: %w", err)
	}
	return &digest, nil
}

// CreatesByTypeName indexes every create step by Pulumi type and resource name.
// Resources the program would create are the ones injection can supply state
// for; every other operation is ignored.
func (d *PreviewDigest) CreatesByTypeName() (map[PreviewKey]map[string]interface{}, error) {
	result := make(map[PreviewKey]map[string]interface{})
	for _, step := range d.Steps {
		if step.Op != "create" || step.NewState == nil {
			continue
		}
		urn, _ := step.NewState["urn"].(string)
		if urn == "" {
			urn = step.URN
		}
		typ, name, err := splitURN(urn)
		if err != nil {
			return nil, err
		}
		key := PreviewKey{Type: typ, Name: name}
		if _, dup := result[key]; dup {
			return nil, fmt.Errorf("preview contains two create steps for %s %q", typ, name)
		}
		result[key] = step.NewState
	}
	return result, nil
}

// OpsByURN maps each step's URN to its operation, for checking that injected
// resources report "same" on the verifying preview.
func (d *PreviewDigest) OpsByURN() map[string]string {
	ops := make(map[string]string, len(d.Steps))
	for _, step := range d.Steps {
		ops[step.URN] = step.Op
	}
	return ops
}

// DiffReasonsByURN maps each step's URN to the property keys causing its
// diff. Steps with no diff reasons (creates, deletes, same, or updates the
// provider reported no per-property reasons for) are omitted rather than
// mapped to an empty slice, so callers can distinguish "no reasons reported"
// from "reasons reported but empty" with a single ok check.
func (d *PreviewDigest) DiffReasonsByURN() map[string][]string {
	reasons := make(map[string][]string, len(d.Steps))
	for _, step := range d.Steps {
		if len(step.DiffReasons) > 0 {
			reasons[step.URN] = step.DiffReasons
		}
	}
	return reasons
}

// splitURN extracts the Pulumi type token and resource name from a URN of the
// form urn:pulumi:<stack>::<project>::<qualifiedType>::<name>. The qualified
// type may name a chain of parents separated by "$"; the resource's own type is
// the last element.
func splitURN(urn string) (string, string, error) {
	parts := strings.Split(urn, "::")
	if len(parts) < 4 {
		return "", "", fmt.Errorf("malformed URN %q", urn)
	}
	name := parts[len(parts)-1]
	qualifiedType := parts[len(parts)-2]
	typ := qualifiedType
	if idx := strings.LastIndex(qualifiedType, "$"); idx >= 0 {
		typ = qualifiedType[idx+1:]
	}
	return typ, name, nil
}
