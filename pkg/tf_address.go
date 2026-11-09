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
	"strings"

	"github.com/pulumi-proserv/pulumi-tool-import/internal/tfaddr"
)

// PulumiNameFromTerraformAddress derives a Pulumi resource name from a Terraform
// resource address, folding module path segments into the name and dropping a
// trailing "this" when a module already provides a meaningful name.
func PulumiNameFromTerraformAddress(address, resourceType string) string {
	instance, err := tfaddr.ParseResource(address)
	if err != nil || instance.Resource.Resource.Type != resourceType {
		return ""
	}
	var moduleParts []string
	for _, step := range instance.Module {
		moduleParts = append(moduleParts, tfaddr.Name(step.Name, step.InstanceKey))
	}
	resourceName := tfaddr.Name(instance.Resource.Resource.Name, instance.Resource.Key)

	// Drop "this" suffix when module context provides a meaningful name.
	if len(moduleParts) > 0 && resourceName == "this" {
		return strings.Join(moduleParts, "_")
	}

	nameParts := append(moduleParts, resourceName)
	return strings.Join(nameParts, "_")
}
