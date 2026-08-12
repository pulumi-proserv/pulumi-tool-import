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

package cfn

import (
	"fmt"
	"strings"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg"
	"github.com/pulumi-proserv/pulumi-tool-import/pkg/importid"
)

func suffix(name string) string {
	if i := strings.LastIndex(name, "-"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// FillFromDigest fills each import entry's ID from the matching digest resource,
// composing pure types via the shared spec core and using pre-resolved IDs for
// lookup types.
func FillFromDigest(digest *StackDigest, importFile *pkg.ImportFile, mappings map[string]string, provider string) *pkg.FillResult {
	byLogical := make(map[string]*CfnResource, len(digest.Resources))
	for i := range digest.Resources {
		byLogical[digest.Resources[i].LogicalID] = &digest.Resources[i]
	}
	res := &pkg.FillResult{}
	for i := range importFile.Resources {
		entry := &importFile.Resources[i]
		if entry.Component {
			res.Skipped++
			continue
		}
		if entry.ID != "" && entry.ID != "<PLACEHOLDER>" {
			continue // already filled, don't clobber a pre-populated ID
		}
		logical := suffix(entry.Name)
		if m, ok := mappings[entry.Name]; ok {
			logical = m
		}
		r, ok := byLogical[logical]
		if !ok || r.Skipped {
			res.Unmatched++
			res.Warnings = append(res.Warnings, "no digest match for "+entry.Name)
			continue
		}
		// The SecretVersion is not a CFN resource; it matches the owning secret's
		// logical ID and takes the live-enriched arn|versionId import ID.
		if entry.Type == "aws:secretsmanager/secretVersion:SecretVersion" {
			if r.SecretVersionImportID == "" {
				res.Warnings = append(res.Warnings, "no live secret version for "+entry.Name+" (run digest cfn with AWS access)")
				res.Unmatched++
				continue
			}
			entry.ID = r.SecretVersionImportID
			res.Filled++
			continue
		}
		if provider == "native" && r.NativeImportID != "" {
			// aws-native entry types (aws-native:apigateway:*) aren't in the
			// classic-keyed spec table — use the pre-resolved native composite ID.
			entry.ID = r.NativeImportID
		} else if id, handled, err := importid.Compose(entry.Type, provider, CfnGetter(r.Attributes)); handled && err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("compose failed for %s (%s): %v", entry.Name, entry.Type, err))
			res.Unmatched++
			continue
		} else if handled {
			entry.ID = id
		} else if r.ImportID != "" {
			entry.ID = r.ImportID
		} else {
			entry.ID = r.PhysicalID
		}
		if entry.ID == "" {
			res.Warnings = append(res.Warnings, fmt.Sprintf("empty import ID for %s", entry.Name))
			res.Unmatched++
			continue
		}
		if strings.Contains(entry.ID, "<unresolved-intrinsic:") {
			res.Warnings = append(res.Warnings, fmt.Sprintf("unresolved intrinsic in import ID for %s: %s", entry.Name, entry.ID))
			res.Unmatched++
			entry.ID = ""
			continue
		}
		res.Filled++
	}
	return res
}
