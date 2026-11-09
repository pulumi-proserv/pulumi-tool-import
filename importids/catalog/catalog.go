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

// Package catalog defines the version-independent import-ID catalog contract.
package catalog

import (
	"encoding/json"
	"fmt"
)

type Composer func(attrs map[string]interface{}, stateID string) (string, bool)

// Catalog couples a generated table to the composers from the same module.
type Catalog struct {
	Formats   *Formats
	Composers map[string]Composer
}

// Source is the reviewed generation pin, not a moving latest-version lookup.
// Helpers maps modeled acctest helper names to hashes of their printed Go ASTs.
type Source struct {
	Provider         string            `json:"provider"`
	PulumiVersion    string            `json:"pulumiVersion"`
	UpstreamVersion  string            `json:"upstreamVersion"`
	UpstreamRevision string            `json:"upstreamRevision"`
	Helpers          map[string]string `json:"helpers"`
}

// LoadEmbedded checks generation provenance before pairing a table and composers.
func LoadEmbedded(data, sourceData []byte, composers map[string]Composer) *Catalog {
	formats, err := ParseFormats(data)
	if err != nil {
		panic(err)
	}
	var source Source
	if err := json.Unmarshal(sourceData, &source); err != nil {
		panic(err)
	}
	if formats.Provider != source.Provider || formats.Version != source.UpstreamVersion ||
		formats.PulumiVersion != source.PulumiVersion || formats.UpstreamRevision != source.UpstreamRevision {
		panic(fmt.Sprintf("catalog does not match generation source for %s %s; regenerate it", source.Provider, source.PulumiVersion))
	}
	return &Catalog{Formats: formats, Composers: composers}
}
