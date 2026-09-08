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

package cmd

import (
	"testing"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg"
	"github.com/pulumi-proserv/pulumi-tool-import/pkg/importid"
	"github.com/stretchr/testify/assert"
)

func TestFormatsVersionWarning(t *testing.T) {
	formats := &importid.Formats{Provider: "hashicorp/aws", Version: "v6.38.0"}

	// Digest pinned to a Pulumi version whose upstream is older or equal: quiet.
	older := &pkg.ModuleMap{Providers: map[string]string{"registry.terraform.io/hashicorp/aws": "v7.24.0"}}
	assert.Equal(t, "", formatsVersionWarning(older, formats))

	// No aws provider at all: quiet.
	assert.Equal(t, "", formatsVersionWarning(&pkg.ModuleMap{}, formats))

	// Table older than the digest's upstream: warn, naming both and the make target.
	stale := &importid.Formats{Provider: "hashicorp/aws", Version: "v6.0.0"}
	got := formatsVersionWarning(older, stale)
	assert.Contains(t, got, "v6.0.0")
	assert.Contains(t, got, "v6.38.0")
	assert.Contains(t, got, "make update-import-id-formats")
}
