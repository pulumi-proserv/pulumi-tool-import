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
	"strings"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg/providermap"
)

// CurrentDigestFormatVersion is stamped into every digest this build writes.
// Bump it on a change a consumer built before the change would half-read
// rather than reject.
const CurrentDigestFormatVersion = 1

// dynamicPin marks a provider the digest resolved through dynamic bridging
// rather than a statically bridged Pulumi provider.
const dynamicPin = "dynamic"

// LoadDigest reads a tf-digest.json with UseNumber (the digest's values end
// up in state) and refuses a format version newer than this build knows.
func LoadDigest(path string) (*ModuleMap, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading digest: %w", err)
	}
	var mm ModuleMap
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&mm); err != nil {
		return nil, fmt.Errorf("parsing digest %s: %w", path, err)
	}
	if mm.FormatVersion > CurrentDigestFormatVersion {
		return nil, fmt.Errorf(
			"digest %s has format version %d, but this build reads at most version %d; "+
				"re-run \"digest tf\" with this build, or upgrade the tool",
			path, mm.FormatVersion, CurrentDigestFormatVersion)
	}
	return &mm, nil
}

// applyProviderPin overrides rec's version with the pin the digest recorded
// ("name@version"), so injection loads the provider the digest's property
// names came from. An empty pin (older digest, versions unrecorded) leaves
// rec unchanged. A pin that disagrees about WHICH provider — identifier, or
// static vs dynamic — is mapping drift between digest and injection, and
// errors telling the operator to re-run the digest.
func applyProviderPin(
	rec providermap.RecommendedPulumiProvider, pin string,
) (providermap.RecommendedPulumiProvider, error) {
	if pin == "" {
		return rec, nil
	}
	if pin == dynamicPin || strings.HasPrefix(pin, dynamicPin+"@") {
		if rec.StaticallyBridgedProvider != nil {
			return rec, fmt.Errorf(
				"the digest resolved this provider by dynamic bridging, but this build recommends "+
					"the statically bridged %q: the tool's provider mapping changed since the digest "+
					"was written. Re-run \"digest tf\" with this build",
				rec.StaticallyBridgedProvider.Identifier)
		}
		return rec, nil
	}
	name, version, ok := strings.Cut(pin, "@")
	if !ok {
		return rec, fmt.Errorf("digest records malformed provider version %q (want name@version)", pin)
	}
	if rec.StaticallyBridgedProvider == nil {
		return rec, fmt.Errorf(
			"the digest resolved this provider to the statically bridged %s, but this build "+
				"recommends dynamic bridging: the tool's provider mapping changed since the digest "+
				"was written. Re-run \"digest tf\" with this build",
			pin)
	}
	if rec.StaticallyBridgedProvider.Identifier != name {
		return rec, fmt.Errorf(
			"the digest was computed against Pulumi provider %s, but this build maps the same "+
				"Terraform provider to %q: property names would come from a different schema than "+
				"the digest's. Re-run \"digest tf\" with this build",
			pin, rec.StaticallyBridgedProvider.Identifier)
	}
	pinned := *rec.StaticallyBridgedProvider
	pinned.Version = version
	return providermap.RecommendedPulumiProvider{StaticallyBridgedProvider: &pinned}, nil
}
