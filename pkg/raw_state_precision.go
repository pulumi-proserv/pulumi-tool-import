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
	"strconv"
	"strings"
)

// restoreLargeIntegers repairs float64 rounding in a converted outputs tree
// (issue #29): the bridge conversion passes numbers through
// resource.PropertyValue, whose number kind is float64, so an integer above
// 2^53 in attrsJSON reaches outputs rounded. The repair correlates by VALUE,
// not by name — a rounded leaf is replaced with the one source digit-string
// that rounds to it — so the bridge's renames and reshaping never have to be
// inverted. The input is not modified; a new tree is returned.
//
// A leaf whose value is ambiguous (distinct sources land on the same
// float64) is left rounded rather than guessed, and its value is returned in
// ambiguities so the caller can warn; the raw state delta, computed from the
// exact cty value, still carries the right digits downstream. An error is
// only a failure to decode attrsJSON.
func restoreLargeIntegers(
	outputs map[string]interface{}, attrsJSON []byte,
) (repaired map[string]interface{}, ambiguities []string, err error) {
	dec := json.NewDecoder(bytes.NewReader(attrsJSON))
	dec.UseNumber()
	var attrs interface{}
	if err := dec.Decode(&attrs); err != nil {
		return nil, nil, fmt.Errorf("decoding attributes for precision repair: %w", err)
	}

	repairs, ambiguous := indexSourceIntegers(attrs)
	if len(repairs) == 0 && len(ambiguous) == 0 {
		return outputs, nil, nil
	}

	hit := map[float64]bool{}
	repaired = make(map[string]interface{}, len(outputs))
	for k, v := range outputs {
		repaired[k] = repairLeaf(v, repairs, ambiguous, hit)
	}
	for f := range hit {
		ambiguities = append(ambiguities, strconv.FormatFloat(f, 'f', -1, 64))
	}
	return repaired, ambiguities, nil
}

// indexSourceIntegers walks the decoded attributes and returns, per rounded
// float64: the single lossy digit-string that rounds to it (repairs), or a
// marker that no unique repair exists (ambiguous). A source is "lossy" when
// its integer literal does not survive a float64 round-trip; every other
// numeric form — floats, exponent notation, round-trippable integers — is
// "exact at f", and a rounded leaf equal to an exact source cannot be told
// apart from that source, so f becomes ambiguous whenever a repair exists
// for it too.
func indexSourceIntegers(attrs interface{}) (repairs map[float64]string, ambiguous map[float64]bool) {
	repairs = map[float64]string{}
	ambiguous = map[float64]bool{}
	exact := map[float64]bool{}

	var collect func(v interface{})
	collect = func(v interface{}) {
		switch val := v.(type) {
		case json.Number:
			digits := val.String()
			f, err := val.Float64()
			if err != nil {
				return
			}
			if strings.ContainsAny(digits, ".eE") ||
				strconv.FormatFloat(f, 'f', -1, 64) == digits {
				exact[f] = true
				return
			}
			if prior, hit := repairs[f]; hit && prior != digits {
				ambiguous[f] = true
			}
			repairs[f] = digits
		case map[string]interface{}:
			for _, elem := range val {
				collect(elem)
			}
		case []interface{}:
			for _, elem := range val {
				collect(elem)
			}
		}
	}
	collect(attrs)

	for f := range repairs {
		if exact[f] || ambiguous[f] {
			delete(repairs, f)
			ambiguous[f] = true
		}
	}
	return repairs, ambiguous
}

// repairLeaf rebuilds v with every float64 leaf that matches a repairable
// rounded value replaced by its exact digits. A leaf matching an ambiguous
// value is left rounded — rewriting it would be a guess between sources —
// and the value is recorded in hit for the caller's warning.
func repairLeaf(
	v interface{}, repairs map[float64]string, ambiguous map[float64]bool, hit map[float64]bool,
) interface{} {
	switch val := v.(type) {
	case float64:
		if ambiguous[val] {
			hit[val] = true
			return v
		}
		if digits, ok := repairs[val]; ok {
			return json.Number(digits)
		}
		return v
	case map[string]interface{}:
		out := make(map[string]interface{}, len(val))
		for k, elem := range val {
			out[k] = repairLeaf(elem, repairs, ambiguous, hit)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(val))
		for i, elem := range val {
			out[i] = repairLeaf(elem, repairs, ambiguous, hit)
		}
		return out
	default:
		return v
	}
}
