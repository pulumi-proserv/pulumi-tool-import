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
	"os"
)

// NonImportableFile is the sidecar "resolve tf" writes beside the import file,
// recording the resources it left out because their Terraform type declares no
// importer.
type NonImportableFile struct {
	Comment   string                  `json:"_comment,omitempty"`
	Resources []NonImportableResource `json:"resources"`
}

// LoadNonImportableFile reads a sidecar written by "resolve tf".
//
// UseNumber keeps large integer attributes exact; they are written into state
// unchanged, where a float64 would re-serialize as scientific notation.
func LoadNonImportableFile(path string) (*NonImportableFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading non-importable file: %w", err)
	}
	var f NonImportableFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &f, nil
}

// MapTFAttributesToPulumi renames Terraform attributes to their Pulumi property
// names using the provider schema. An attribute the schema does not describe is
// camelCased rather than dropped: losing state values silently is worse than
// carrying one under a best-guess name.
func MapTFAttributesToPulumi(
	attrs map[string]interface{},
	fields map[string]*SchemaFieldInfo,
) map[string]interface{} {
	result := make(map[string]interface{}, len(attrs))
	for tfName, value := range attrs {
		name := snakeToCamel(tfName)
		if fi, ok := fields[tfName]; ok && fi.PulumiName != "" {
			name = fi.PulumiName
		}
		result[name] = value
	}
	return result
}

// PulumiToTFNames inverts the schema's name mapping, so a Pulumi property name
// can be traced back to the Terraform attribute it came from.
func PulumiToTFNames(fields map[string]*SchemaFieldInfo) map[string]string {
	result := make(map[string]string, len(fields))
	for tfName, fi := range fields {
		if fi != nil && fi.PulumiName != "" {
			result[fi.PulumiName] = tfName
		}
	}
	return result
}
