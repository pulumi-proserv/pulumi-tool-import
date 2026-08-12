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

import "strings"

// PulumiNameFromTerraformAddress derives a Pulumi resource name from a Terraform
// resource address, folding module path segments into the name and dropping a
// trailing "this" when a module already provides a meaningful name.
func PulumiNameFromTerraformAddress(address, resourceType string) string {
	parts := strings.Split(address, ".")

	var moduleParts []string
	var resourceParts []string
	for i := 0; i < len(parts); i++ {
		if parts[i] == resourceType {
			resourceParts = append(resourceParts, parts[i+1:]...)
			break
		}
		if parts[i] == "module" && i+1 < len(parts) {
			moduleParts = append(moduleParts, parts[i+1])
			i++
		}
	}

	// Drop "this" suffix when module context provides a meaningful name.
	if len(moduleParts) > 0 && len(resourceParts) == 1 && resourceParts[0] == "this" {
		return strings.Join(moduleParts, "_")
	}

	nameParts := append(moduleParts, resourceParts...)
	return strings.Join(nameParts, "_")
}
