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

package batchimport

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/optimport"
)

// ImportFile is the on-disk Pulumi import file. Resources decode directly into
// optimport.ImportResource — the type ImportResources consumes — so no field is
// lost in translation.
type ImportFile struct {
	NameTable map[string]string           `json:"nameTable,omitempty"`
	Resources []*optimport.ImportResource `json:"resources"`
}

// LoadImportFile reads and decodes a prepared import file.
func LoadImportFile(path string) (*ImportFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading import file: %w", err)
	}
	var f ImportFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parsing import file: %w", err)
	}
	return &f, nil
}

// keyOf builds the ResourceKey identifying an import entry.
func keyOf(r *optimport.ImportResource) ResourceKey {
	return ResourceKey{Type: r.Type, Name: r.Name}
}
