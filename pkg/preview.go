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
	"path/filepath"
	"strings"
)

type PreviewKey struct {
	Type string
	Name string
}

type PreviewStep struct {
	Op       string                 `json:"op"`
	URN      string                 `json:"urn"`
	NewState map[string]interface{} `json:"newState"`
	// OldState is populated by "pulumi refresh --preview-only --json" steps;
	// plain preview steps carry only NewState.
	OldState    map[string]interface{} `json:"oldState,omitempty"`
	DiffReasons []string               `json:"diffReasons,omitempty"`
}

type PreviewDigest struct {
	Steps         []PreviewStep  `json:"steps"`
	ChangeSummary map[string]int `json:"changeSummary"`
}

// LambdaArchivePaths identifies file archives the program actually declares.
// Embedded archives cannot be silently substituted for byte-identical ZIPs.
func (d *PreviewDigest) LambdaArchivePaths(projectDir string) (map[string]string, error) {
	paths := map[string]string{}
	for _, step := range d.Steps {
		state := step.NewState
		if state["type"] != "aws:lambda/function:Function" {
			continue
		}
		inputs, _ := state["inputs"].(map[string]interface{})
		code := inputs["code"]
		if code == nil {
			continue
		}
		archive, ok := code.(map[string]interface{})
		if !ok || archive[sigKey] != archiveSig {
			return nil, fmt.Errorf("lambda %s code is not a known archive; cannot reconcile its artifact", step.URN)
		}
		path, ok := archive["path"].(string)
		if !ok || path == "" {
			return nil, fmt.Errorf("lambda %s uses a non-file archive; byte-preserving ZIP reconciliation requires a FileArchive referencing the deployed ZIP", step.URN)
		}
		if !filepath.IsAbs(path) {
			if projectDir == "" {
				return nil, fmt.Errorf("--project-dir is required to resolve program archive %q", path)
			}
			path = filepath.Join(projectDir, path)
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		urn := step.URN
		if urn == "" {
			urn, _ = state["urn"].(string)
		}
		if urn == "" {
			return nil, fmt.Errorf("lambda archive preview has no resource URN")
		}
		paths[urn] = absolute
	}
	return paths, nil
}

func ParsePreviewJSON(data []byte) (*PreviewDigest, error) {
	var digest PreviewDigest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&digest); err != nil {
		return nil, fmt.Errorf("parsing preview JSON: %w", err)
	}
	return &digest, nil
}

type PreviewCreates struct {
	byKey map[PreviewKey]map[string]interface{}
	urns  map[PreviewKey][]string
}

func (c *PreviewCreates) Lookup(key PreviewKey) (map[string]interface{}, error) {
	if urns := c.urns[key]; len(urns) > 1 {
		return nil, fmt.Errorf(
			"the preview contains %d create steps for %s %q and the sidecar records only the "+
				"resource's own type, so they cannot be told apart: %s. Give the resources "+
				"distinct Pulumi names, or map them so their names differ",
			len(urns), key.Type, key.Name, strings.Join(urns, ", "))
	}
	state, ok := c.byKey[key]
	if !ok {
		return nil, nil
	}
	return state, nil
}

func (d *PreviewDigest) CreatesByTypeName() (*PreviewCreates, error) {
	result := &PreviewCreates{
		byKey: make(map[PreviewKey]map[string]interface{}),
		urns:  make(map[PreviewKey][]string),
	}
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
		result.urns[key] = append(result.urns[key], urn)
		if _, dup := result.byKey[key]; !dup {
			result.byKey[key] = step.NewState
		}
	}
	return result, nil
}

func (d *PreviewDigest) OpsByURN() map[string]string {
	ops := make(map[string]string, len(d.Steps))
	for _, step := range d.Steps {
		ops[step.URN] = step.Op
	}
	return ops
}

func (d *PreviewDigest) DiffReasonsByURN() map[string][]string {
	reasons := make(map[string][]string, len(d.Steps))
	for _, step := range d.Steps {
		if len(step.DiffReasons) > 0 {
			reasons[step.URN] = step.DiffReasons
		}
	}
	return reasons
}

func splitURN(urn string) (string, string, error) {
	parts := strings.SplitN(urn, "::", 4)
	if len(parts) < 4 {
		return "", "", fmt.Errorf("malformed URN %q", urn)
	}
	name := parts[3]
	qualifiedType := parts[2]
	typ := qualifiedType
	if idx := strings.LastIndex(qualifiedType, "$"); idx >= 0 {
		typ = qualifiedType[idx+1:]
	}
	return typ, name, nil
}
