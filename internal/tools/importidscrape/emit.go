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
	"slices"
	"strings"

	"github.com/pulumi-proserv/pulumi-tool-import/importids/catalog"
)

// Summary tallies buildFormats' output by how each entry's evidence was
// classified; see the loop at the end of buildFormats.
type Summary struct {
	Templates, ManualFromTests, ManualFromDocs, SensitiveHits int
}

// buildFormats runs the four stages against a provider checkout.
func buildFormats(providerRoot, version string, warn func(string), sources ...catalog.Source) (*catalog.Formats, Summary, error) {
	var source *catalog.Source
	if len(sources) > 0 {
		source = &sources[0]
		if err := checkPinnedHelpers(providerRoot, source.Helpers); err != nil {
			return nil, Summary{}, err
		}
	} else if err := checkAcctestHelpers(providerRoot); err != nil {
		return nil, Summary{}, err
	}
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
	separator, err := loadResourceIDSeparator(providerRoot)
	if err != nil {
		return nil, Summary{}, err
	}

	f := &catalog.Formats{Provider: "hashicorp/aws", Version: version, Types: map[string]catalog.FormatEntry{}}
	if source != nil {
		f.PulumiVersion, f.UpstreamRevision = source.PulumiVersion, source.UpstreamRevision
	}
	var sum Summary

	// provenPassthrough holds the types some import test proved to import by
	// their state ID. A divergent-looking docs example for such a type is a
	// documentation shorthand, not evidence, so the docs pass below must not
	// invent an entry for it: that entry would put a "compose this by hand"
	// note on a resource the provider's own tests import by its state ID.
	provenPassthrough := map[string]bool{}

	// Prefer a proven template to an unclassified step, but retain distinct
	// templates as manual alternatives rather than choosing by source order.
	for _, step := range steps {
		step.ResourceIDSeparator = &separator
		if source != nil {
			step.Helpers = source.Helpers
		}
		c := classify(step, consts)
		// A step with no ImportStateIdFunc, and a step proving exactly "{id}",
		// both say the import ID is the state ID. That is not a divergence, so
		// it neither creates an entry nor overrides one. ("{id}" inside a
		// composite such as "{rest_api_id}/{id}" is a real template and is
		// unaffected.)
		if (c.Template == "" && !c.Manual) || c.Template == "{id}" {
			provenPassthrough[step.TFType] = true
			continue
		}
		existing, seen := f.Types[step.TFType]
		if seen && (c.Manual || existing.Template == c.Template || hasTemplate(existing.Alternatives, c.Template)) {
			continue
		}
		e := catalog.FormatEntry{
			Evidence: fmt.Sprintf("terraform-provider-aws/%s:%d %s", evidenceFile(c, step), evidenceLine(c, step), c.Symbol),
		}
		if c.Template != "" {
			e.Template = c.Template
			if s := sensitiveAttrs(providerRoot, step.TFType, c.Template, consts); len(s) > 0 {
				e.Sensitive = s
			}
		} else {
			e.Manual, e.Snippet = true, c.Snippet
		}
		f.Types[step.TFType] = mergeFormat(step.TFType, existing, e)
	}

	for typ, d := range docs {
		if e, ok := f.Types[typ]; ok {
			e.Docs, e.DocsSource = "terraform import "+typ+".example "+d.Example, d.Source
			f.Types[typ] = e
			continue
		}
		if d.Divergent && !provenPassthrough[typ] {
			f.Types[typ] = catalog.FormatEntry{
				Manual:     true,
				Docs:       "terraform import " + typ + ".example " + d.Example,
				DocsSource: d.Source,
				Evidence:   fmt.Sprintf("docs-only: multi-segment or attribute-based example in %s, no import test found", d.Source),
			}
		}
	}

	for _, typ := range sortedKeys(f.Types) {
		e := f.Types[typ]
		if len(e.Sensitive) > 0 {
			sum.SensitiveHits++
			warn(fmt.Sprintf("%s: import format reads Sensitive attribute(s) %s", typ, strings.Join(e.Sensitive, ", ")))
		}
		if len(e.Alternatives) > 0 {
			warn(fmt.Sprintf("%s: %d distinct import templates retained as manual alternatives; use an explicit composer", typ, len(e.Alternatives)))
		}
		switch {
		case e.Template != "":
			sum.Templates++
		case strings.HasPrefix(e.Evidence, "docs-only:"):
			sum.ManualFromDocs++
		default:
			sum.ManualFromTests++
		}
	}
	if err := f.Validate(); err != nil {
		return nil, Summary{}, err
	}
	return f, sum, nil
}

func hasTemplate(alternatives []catalog.TemplateEvidence, template string) bool {
	for _, alternative := range alternatives {
		if alternative.Template == template {
			return true
		}
	}
	return false
}

func mergeFormat(typ string, existing, candidate catalog.FormatEntry) catalog.FormatEntry {
	if existing.Evidence == "" {
		return candidate
	}
	if candidate.Template == "" {
		return existing
	}
	if len(existing.Alternatives) == 0 {
		if existing.Template == "" {
			return candidate
		}
		if existing.Template == candidate.Template {
			return existing
		}
		existing.Alternatives = []catalog.TemplateEvidence{{Template: existing.Template, Evidence: existing.Evidence}}
		existing.Template, existing.Snippet = "", ""
		existing.Manual = true
		existing.Evidence = fmt.Sprintf("multiple import templates for %s; see alternatives", typ)
	}
	if !hasTemplate(existing.Alternatives, candidate.Template) {
		existing.Alternatives = append(existing.Alternatives, catalog.TemplateEvidence{Template: candidate.Template, Evidence: candidate.Evidence})
	}
	slices.SortFunc(existing.Alternatives, func(a, b catalog.TemplateEvidence) int {
		return strings.Compare(a.Template, b.Template)
	})
	existing.Sensitive = append(existing.Sensitive, candidate.Sensitive...)
	slices.Sort(existing.Sensitive)
	existing.Sensitive = slices.Compact(existing.Sensitive)
	return existing
}

// evidenceFile pairs with evidenceLine: the line is the resolved helper's, so
// the file must be the helper's too, which is often a sibling of the step's.
func evidenceFile(c Classification, step ImportStep) string {
	if c.File != "" {
		return c.File
	}
	return step.File
}

func evidenceLine(c Classification, step ImportStep) int {
	if c.Line > 0 {
		return c.Line
	}
	return step.Line
}

// writeFormats emits sorted, 2-space-indented JSON with a trailing newline.
// encoding/json sorts map keys, which is the whole determinism story.
func writeFormats(path string, f *catalog.Formats) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(f); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
