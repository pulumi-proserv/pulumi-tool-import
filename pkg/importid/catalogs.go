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

package importid

import (
	"fmt"
	"strings"

	"github.com/blang/semver/v4"
	aws6 "github.com/pulumi-proserv/pulumi-tool-import/importids/aws/v6"
	aws7 "github.com/pulumi-proserv/pulumi-tool-import/importids/aws/v7"
	"github.com/pulumi-proserv/pulumi-tool-import/importids/catalog"
	"github.com/pulumi-proserv/pulumi-tool-import/pkg/provideraddr"
)

type Formats = catalog.Formats
type FormatEntry = catalog.FormatEntry

func Expand(template string, attrs map[string]interface{}, stateID string) (string, error) {
	return catalog.Expand(template, attrs, stateID)
}

func LoadFormats(path string) (*Formats, error) {
	f, err := catalog.LoadFormats(path)
	if err != nil {
		return nil, err
	}
	if f.Provider != "hashicorp/aws" {
		return nil, fmt.Errorf("unsupported provider %q: only hashicorp/aws import-ID formats are supported", f.Provider)
	}
	return f, nil
}

// AWSVersion finds the resolved destination pin, allowing equivalent registry hosts.
func AWSVersion(providers map[string]string) (string, error) {
	var pin string
	for _, addr := range provideraddr.Equivalents("registry.terraform.io/hashicorp/aws") {
		if candidate := providers[addr]; candidate != "" {
			if pin != "" && candidate != pin {
				return "", fmt.Errorf("conflicting AWS provider pins %q and %q", pin, candidate)
			}
			pin = candidate
		}
	}
	name, version, found := strings.Cut(pin, "@")
	if !found || name != "aws" || version == "" {
		return "", fmt.Errorf("missing statically bridged Pulumi AWS version; re-run digest tf or set the import entry's version (supported majors: 6, 7)")
	}
	return version, nil
}

// ForAWSVersion selects by Pulumi major, never by the Terraform source major.
// An override replaces the table only within its declared Pulumi major.
func ForAWSVersion(version string, override *Formats) (*catalog.Catalog, error) {
	v, err := semver.ParseTolerant(version)
	if err != nil {
		return nil, fmt.Errorf("invalid Pulumi AWS version %q: %w", version, err)
	}
	var c *catalog.Catalog
	switch v.Major {
	case 6:
		c = aws6.Load()
	case 7:
		c = aws7.Load()
	default:
		return nil, fmt.Errorf("unsupported Pulumi AWS major %d (supported: 6, 7)", v.Major)
	}
	if override != nil {
		ov, err := semver.ParseTolerant(override.PulumiVersion)
		if err != nil || ov.Major != v.Major {
			return nil, fmt.Errorf("import-ID override must declare pulumiVersion in major %d; got %q", v.Major, override.PulumiVersion)
		}
		if override.Provider != c.Formats.Provider {
			return nil, fmt.Errorf("import-ID override provider %q does not match %q", override.Provider, c.Formats.Provider)
		}
		upstream, err := semver.ParseTolerant(override.Version)
		expected, expectedErr := semver.ParseTolerant(c.Formats.Version)
		if err != nil || expectedErr != nil || upstream.Major != expected.Major {
			return nil, fmt.Errorf("import-ID override upstream version %q is incompatible with Pulumi AWS major %d", override.Version, v.Major)
		}
		if err := override.Validate(); err != nil {
			return nil, err
		}
		c.Formats = override
	}
	return c, nil
}

func VersionWarning(version string, c *catalog.Catalog) string {
	requested, err := semver.ParseTolerant(version)
	generated, genErr := semver.ParseTolerant(c.Formats.PulumiVersion)
	if err != nil || genErr != nil || !requested.GT(generated) {
		return ""
	}
	return fmt.Sprintf("Pulumi AWS %s is newer than the catalog generated for %s (Terraform AWS %s); update the matching importids/aws/v%d source pin and run make update-import-id-formats", version, c.Formats.PulumiVersion, c.Formats.Version, requested.Major)
}
