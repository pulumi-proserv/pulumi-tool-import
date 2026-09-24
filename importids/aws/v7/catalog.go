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
	return catalog.LoadEmbedded(formats, source, composers())
}
