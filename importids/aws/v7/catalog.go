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

// Package aws contains import-ID behavior for Pulumi AWS v7 (Terraform AWS v6).
package aws

import (
	_ "embed"

	"github.com/pulumi-proserv/pulumi-tool-import/importids/catalog"
)

//go:embed formats.json
var formats []byte

//go:embed source.json
var source []byte

func Load() *catalog.Catalog {
	c := catalog.LoadEmbedded(formats, source, composers())
	// Current-provider tests cannot reveal historical name-based state IDs.
	// Keep this migration rule separate from the generated import-test evidence.
	c.Formats.Types["aws_cloudtrail"] = catalog.FormatEntry{
		Manual:   true,
		Docs:     "CloudTrail trail ARN (from arn for historical name-based state IDs)",
		Evidence: "https://github.com/hashicorp/terraform-provider-aws/blob/d0cb9698b41b21c587043949fde0bac0459e335a/internal/provider/sdkv2/importer/arn.go#L16-L37",
	}
	// Preserve the generated evidence while adding validation and conflict
	// handling that plain template expansion cannot express.
	for _, principal := range []string{"user", "group", "role"} {
		typ := "aws_iam_" + principal + "_policy_attachment"
		entry := c.Formats.Types[typ]
		entry.Template = ""
		entry.Manual = true
		c.Formats.Types[typ] = entry
	}
	return c
}
