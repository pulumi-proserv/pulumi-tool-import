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

package importid

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
)

//go:embed aws-import-id-formats.json
var embeddedFormats []byte

// FormatEntry is one divergent Terraform type: either a template over state
// attribute names, or manual with the provider's own composition as evidence.
type FormatEntry struct {
	Template   string   `json:"template,omitempty"`
	Manual     bool     `json:"manual,omitempty"`
	Snippet    string   `json:"snippet,omitempty"`
	Sensitive  []string `json:"sensitive,omitempty"`
	Docs       string   `json:"docs,omitempty"`
	DocsSource string   `json:"docsSource,omitempty"`
	Evidence   string   `json:"evidence"`
}

// DocsOnly reports whether the entry is manual because the provider's
// documentation shows a composite example and no import test was found —
// a review candidate rather than a proven divergence.
func (e FormatEntry) DocsOnly() bool {
	return strings.HasPrefix(e.Evidence, "docs-only:")
}

// Formats is the import-ID composition table for one upstream provider
// version. Absence of a type means its state ID is its import ID.
type Formats struct {
	Provider string                 `json:"provider"`
	Version  string                 `json:"version"`
	Types    map[string]FormatEntry `json:"types"`
}

var (
	embeddedOnce   sync.Once
	embeddedParsed *Formats
)

// Embedded returns the table compiled into the binary.
func Embedded() *Formats {
	embeddedOnce.Do(func() {
		f, err := parseFormats(embeddedFormats)
		if err != nil {
			panic(fmt.Sprintf("embedded aws-import-id-formats.json: %v", err))
		}
		embeddedParsed = f
	})
	return embeddedParsed
}

// LoadFormats reads a table from disk, for testing a regenerated table
// before committing it.
func LoadFormats(path string) (*Formats, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading import-ID formats: %w", err)
	}
	return parseFormats(data)
}

func parseFormats(data []byte) (*Formats, error) {
	var f Formats
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parsing import-ID formats: %w", err)
	}
	if f.Types == nil {
		f.Types = map[string]FormatEntry{}
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

var placeholderRe = regexp.MustCompile(`\{([^{}]*)\}`)

// Validate enforces the table's invariants.
func (f *Formats) Validate() error {
	if f.Provider != "" && f.Provider != "hashicorp/aws" {
		return fmt.Errorf("unsupported provider %q: only hashicorp/aws import-ID formats are supported", f.Provider)
	}
	for typ, e := range f.Types {
		switch {
		case e.Template != "" && e.Manual:
			return fmt.Errorf("%s: template and manual are exclusive", typ)
		case e.Template == "" && !e.Manual:
			return fmt.Errorf("%s: neither template nor manual", typ)
		case e.Evidence == "":
			return fmt.Errorf("%s: missing evidence", typ)
		}
		for _, m := range placeholderRe.FindAllStringSubmatch(e.Template, -1) {
			if strings.TrimSpace(m[1]) == "" {
				return fmt.Errorf("%s: empty placeholder in %q", typ, e.Template)
			}
		}
	}
	return nil
}

// Expand fills a template from state attributes. {id} is the state ID.
func Expand(template string, attrs map[string]interface{}, stateID string) (string, error) {
	var firstErr error
	out := placeholderRe.ReplaceAllStringFunc(template, func(m string) string {
		name := m[1 : len(m)-1]
		if name == "id" {
			if stateID == "" {
				firstErr = firstOf(firstErr, fmt.Errorf(`placeholder "id" has no state id`))
			}
			return stateID
		}
		v, ok := attrs[name]
		if !ok || v == nil {
			firstErr = firstOf(firstErr, fmt.Errorf("attribute %q absent from state", name))
			return ""
		}
		s := fmt.Sprintf("%v", v)
		switch s {
		case "":
			firstErr = firstOf(firstErr, fmt.Errorf("attribute %q is empty", name))
		case "(sensitive)":
			firstErr = firstOf(firstErr, fmt.Errorf("attribute %q is sensitive; the digest redacts it", name))
		}
		return s
	})
	if firstErr != nil {
		return "", firstErr
	}
	return out, nil
}

func firstOf(have, next error) error {
	if have != nil {
		return have
	}
	return next
}
