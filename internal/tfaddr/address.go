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

// Package tfaddr centralizes Terraform resource and module address parsing.
// Keep OpenTofu's typed instance keys until rendering a user-facing name;
// string keys such as "0" and "" are distinct from count indices and no key.
package tfaddr

import (
	"fmt"
	"strconv"

	"github.com/pulumi/opentofu/addrs"
)

func ParseResource(address string) (addrs.AbsResourceInstance, error) {
	instance, diags := addrs.ParseAbsResourceInstanceStr(address)
	if diags.HasErrors() {
		return addrs.AbsResourceInstance{}, diags.Err()
	}
	return instance, nil
}

func ParseModule(address string) (addrs.ModuleInstance, error) {
	if address == "" {
		return addrs.RootModuleInstance, nil
	}
	module, diags := addrs.ParseModuleInstanceStr(address)
	if diags.HasErrors() {
		return nil, diags.Err()
	}
	return module, nil
}

// ParseName parses a resource name with an optional instance key, such as
// public[0] or params["key.with.dots"], using the resource-address grammar.
func ParseName(name string) (addrs.ResourceInstance, error) {
	instance, err := ParseResource("placeholder." + name)
	if err != nil {
		return addrs.ResourceInstance{}, err
	}
	if len(instance.Module) != 0 || instance.Resource.Resource.Type != "placeholder" {
		return addrs.ResourceInstance{}, fmt.Errorf("invalid Terraform resource name %q", name)
	}
	return instance.Resource, nil
}

// ResourceType returns an empty string for an invalid resource address.
func ResourceType(address string) string {
	instance, err := ParseResource(address)
	if err != nil {
		return ""
	}
	return instance.Resource.Resource.Type
}

// Name renders a name and its key with OpenTofu's canonical quoting/escaping.
func Name(name string, key addrs.InstanceKey) string {
	if key == addrs.NoKey {
		return name
	}
	return name + key.String()
}

// KeyValue is the unquoted value for display, not for address reconstruction.
func KeyValue(key addrs.InstanceKey) string {
	switch key := key.(type) {
	case addrs.IntKey:
		return strconv.Itoa(int(key))
	case addrs.StringKey:
		return string(key)
	default:
		return ""
	}
}
