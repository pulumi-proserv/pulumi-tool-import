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
// inverted. An error means the correlation is ambiguous (distinct source
// integers round to the same float64) and the caller must not use outputs at
// all: a wrong guess writes a wrong value into state.
func restoreLargeIntegers(outputs map[string]interface{}, attrsJSON []byte) (map[string]interface{}, error) {
	dec := json.NewDecoder(bytes.NewReader(attrsJSON))
	dec.UseNumber()
	var attrs interface{}
	if err := dec.Decode(&attrs); err != nil {
		return nil, fmt.Errorf("decoding attributes for precision repair: %w", err)
	}

	// lossy: float64 -> the set of source digit-strings that round to it and
	// do not survive the round-trip. exact: float64s some source hits
	// exactly — a rounded leaf equal to one of those cannot be told apart
	// from the exact source, so repairing it would be a guess.
	lossy := map[float64]map[string]bool{}
	exact := map[float64]bool{}
	var collect func(v interface{})
	collect = func(v interface{}) {
		switch val := v.(type) {
		case json.Number:
			digits := val.String()
			if strings.ContainsAny(digits, ".eE") {
				return
			}
			f, err := val.Float64()
			if err != nil {
				return
			}
			if strconv.FormatFloat(f, 'f', -1, 64) == digits {
				exact[f] = true
				return
			}
			if lossy[f] == nil {
				lossy[f] = map[string]bool{}
			}
			lossy[f][digits] = true
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
	if len(lossy) == 0 {
		return outputs, nil
	}

	var repair func(v interface{}) (interface{}, error)
	repair = func(v interface{}) (interface{}, error) {
		switch val := v.(type) {
		case float64:
			sources, hit := lossy[val]
			if !hit {
				return v, nil
			}
			if len(sources) > 1 || exact[val] {
				return nil, fmt.Errorf(
					"attributes contain distinct integers that all round to the float64 %s; "+
						"their exact values cannot be restored unambiguously",
					strconv.FormatFloat(val, 'f', -1, 64))
			}
			for digits := range sources {
				return json.Number(digits), nil
			}
			return v, nil
		case map[string]interface{}:
			for k, elem := range val {
				repaired, err := repair(elem)
				if err != nil {
					return nil, err
				}
				val[k] = repaired
			}
			return val, nil
		case []interface{}:
			for i, elem := range val {
				repaired, err := repair(elem)
				if err != nil {
					return nil, err
				}
				val[i] = repaired
			}
			return val, nil
		default:
			return v, nil
		}
	}
	for k, v := range outputs {
		repaired, err := repair(v)
		if err != nil {
			return nil, err
		}
		outputs[k] = repaired
	}
	return outputs, nil
}
