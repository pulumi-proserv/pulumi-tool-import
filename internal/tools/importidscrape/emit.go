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
	"strings"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg/importid"
)

type Summary struct {
	Templates, ManualFromTests, ManualFromDocs, SensitiveHits int
}

// buildFormats runs the four stages against a provider checkout.
func buildFormats(providerRoot, version string, warn func(string)) (*importid.Formats, Summary, error) {
	if err := checkAcctestHelpers(providerRoot); err != nil {
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

	f := &importid.Formats{Provider: "hashicorp/aws", Version: version, Types: map[string]importid.FormatEntry{}}
	var sum Summary

	// provenPassthrough holds the types some import test proved to import by
	// their state ID. A divergent-looking docs example for such a type is a
	// documentation shorthand, not evidence, so the docs pass below must not
	// invent an entry for it: that entry would put a "compose this by hand"
	// note on a resource the provider's own tests import by its state ID.
	provenPassthrough := map[string]bool{}

	// A type with several import steps: any non-passthrough step wins, and a
	// template beats a manual (a passthrough-looking step elsewhere is a
	// different test's shortcut, not a contradiction).
	for _, step := range steps {
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
		if seen && existing.Template != "" {
			// Two of a type's import steps proving different templates is a
			// real contradiction — one of them is a shape the whitelist reads
			// wrongly, or the type's import ID depends on configuration. The
			// first in sorted order still wins; the warning is so a human
			// looks.
			if c.Template != "" && c.Template != existing.Template {
				warn(fmt.Sprintf("%s: import steps prove different templates %q and %q; keeping %q",
					step.TFType, existing.Template, c.Template, existing.Template))
			}
			continue
		}
		if seen && c.Manual {
			continue
		}
		e := importid.FormatEntry{
			Evidence: fmt.Sprintf("terraform-provider-aws/%s:%d %s", evidenceFile(c, step), evidenceLine(c, step), c.Symbol),
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
		if d.Divergent && !provenPassthrough[typ] {
			f.Types[typ] = importid.FormatEntry{
				Manual:     true,
				Docs:       "terraform import " + typ + ".example " + d.Example,
				DocsSource: d.Source,
				Evidence:   fmt.Sprintf("docs-only: multi-segment or attribute-based example in %s, no import test found", d.Source),
			}
		}
	}

	for _, e := range f.Types {
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
