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

// CurrentSidecarFormatVersion is stamped into every sidecar this build
// writes. Bump it on a change an older consumer would half-read rather than
// reject (version 1: the tagged nested placeholder).
const CurrentSidecarFormatVersion = 1

type NonImportableFile struct {
	Comment string `json:"_comment,omitempty"`
	// FormatVersion is the sidecar format this tool wrote. 0 (absent)
	// predates the field; LoadNonImportableFile refuses a version newer than
	// this build knows.
	FormatVersion int                     `json:"formatVersion,omitempty"`
	Resources     []NonImportableResource `json:"resources"`
}

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
	if f.FormatVersion > CurrentSidecarFormatVersion {
		return nil, fmt.Errorf(
			"sidecar %s has format version %d, but this build reads at most version %d; "+
				"re-run \"resolve tf\" with this build, or upgrade the tool",
			path, f.FormatVersion, CurrentSidecarFormatVersion)
	}
	return &f, nil
}

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

func PulumiToTFNames(fields map[string]*SchemaFieldInfo) map[string]string {
	result := make(map[string]string, len(fields))
	for tfName, fi := range fields {
		if fi != nil && fi.PulumiName != "" {
			result[fi.PulumiName] = tfName
		}
	}
	return result
}
