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

// Package provideraddr is the single home for the fact that
// registry.terraform.io and registry.opentofu.org name the same providers.
// The repo also encodes it as a state rewrite (pkg/tofu/loader.go) and
// duplicated rows (pkg/providermap); new correlation points must use
// Equivalents rather than adding another encoding.
package provideraddr

import "strings"

const (
	terraformHost = "registry.terraform.io/"
	opentofuHost  = "registry.opentofu.org/"
)

// Equivalents returns every address form that names the same provider as
// addr, the requested form first: the two registry hosts are interchangeable,
// and a host-less "namespace/type" matches either host.
func Equivalents(addr string) []string {
	switch {
	case strings.HasPrefix(addr, terraformHost):
		rest := strings.TrimPrefix(addr, terraformHost)
		return []string{addr, opentofuHost + rest, rest}
	case strings.HasPrefix(addr, opentofuHost):
		rest := strings.TrimPrefix(addr, opentofuHost)
		return []string{addr, terraformHost + rest, rest}
	case strings.Count(addr, "/") == 1:
		// Host-less "namespace/type", as older tfjson provider_name emits.
		return []string{addr, terraformHost + addr, opentofuHost + addr}
	default:
		// A third-party registry host: no equivalent form.
		return []string{addr}
	}
}
