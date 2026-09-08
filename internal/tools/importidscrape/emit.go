// Copyright 2016-2026, Pulumi Corporation.
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

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg/importid"
)

type Summary struct {
	Templates, ManualFromTests, ManualFromDocs, SensitiveHits int
}

// buildFormats runs the four stages against a provider checkout.
func buildFormats(providerRoot, version string, warn func(string)) (*importid.Formats, Summary, error) {
	steps, err := collectImportSteps(providerRoot)
	if err != nil {
		return nil, Summary{}, err
	}
	docs, err := collectDocs(providerRoot)
	if err != nil {
		return nil, Summary{}, err
	}
	consts, err := loadNameConsts(providerRoot)
	if err != nil {
		return nil, Summary{}, err
	}

	f := &importid.Formats{Provider: "hashicorp/aws", Version: version, Types: map[string]importid.FormatEntry{}}
	var sum Summary

	// A type with several import steps: any non-passthrough step wins, and a
	// template beats a manual (a passthrough-looking step elsewhere is a
	// different test's shortcut, not a contradiction).
	for _, step := range steps {
		c := classify(step, consts)
		if c.Template == "" && !c.Manual {
			continue
		}
		existing, seen := f.Types[step.TFType]
		if seen && existing.Template != "" {
			continue
		}
		if seen && c.Manual {
			continue
		}
		e := importid.FormatEntry{
			Evidence: fmt.Sprintf("terraform-provider-aws/%s:%d %s", step.File, evidenceLine(c, step), c.Symbol),
		}
		if c.Template != "" {
			e.Template = c.Template
			if s := sensitiveAttrs(providerRoot, step.TFType, c.Template); len(s) > 0 {
				e.Sensitive = s
				sum.SensitiveHits++
				warn(fmt.Sprintf("%s: template %q reads Sensitive attribute(s) %s", step.TFType, c.Template, strings.Join(s, ", ")))
			}
		} else {
			e.Manual, e.Snippet = true, c.Snippet
		}
		f.Types[step.TFType] = e
	}

	for typ, d := range docs {
		if e, ok := f.Types[typ]; ok {
			e.Docs, e.DocsSource = "terraform import "+typ+".example "+d.Example, d.Source
			f.Types[typ] = e
			continue
		}
		if d.Divergent {
			f.Types[typ] = importid.FormatEntry{
				Manual:     true,
				Docs:       "terraform import " + typ + ".example " + d.Example,
				DocsSource: d.Source,
				Evidence:   fmt.Sprintf("docs-only: multi-segment or attribute-based example in %s, no import test found", d.Source),
			}
		}
	}

	for typ, e := range f.Types {
		switch {
		case e.Template != "":
			sum.Templates++
		case strings.HasPrefix(e.Evidence, "docs-only:"):
			sum.ManualFromDocs++
		default:
			sum.ManualFromTests++
		}
		_ = typ
	}
	if err := f.Validate(); err != nil {
		return nil, Summary{}, err
	}
	return f, sum, nil
}

func evidenceLine(c Classification, step ImportStep) int {
	if c.Line > 0 {
		return c.Line
	}
	return step.Line
}

// writeFormats emits sorted, 2-space-indented JSON with a trailing newline.
// encoding/json sorts map keys, which is the whole determinism story.
func writeFormats(path string, f *importid.Formats) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(f); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// sortedTypes is for the summary printout only.
func sortedTypes(f *importid.Formats) []string {
	out := make([]string, 0, len(f.Types))
	for k := range f.Types {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
