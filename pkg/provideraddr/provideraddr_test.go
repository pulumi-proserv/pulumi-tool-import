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

package provideraddr

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEquivalents(t *testing.T) {
	t.Parallel()

	assert.Equal(t,
		[]string{"registry.terraform.io/hashicorp/aws", "registry.opentofu.org/hashicorp/aws", "hashicorp/aws"},
		Equivalents("registry.terraform.io/hashicorp/aws"))
	assert.Equal(t,
		[]string{"registry.opentofu.org/hashicorp/aws", "registry.terraform.io/hashicorp/aws", "hashicorp/aws"},
		Equivalents("registry.opentofu.org/hashicorp/aws"))
	// Host-less, as older tfjson provider_name emits.
	assert.Equal(t,
		[]string{"hashicorp/aws", "registry.terraform.io/hashicorp/aws", "registry.opentofu.org/hashicorp/aws"},
		Equivalents("hashicorp/aws"))
	// A third-party registry has no equivalent form.
	assert.Equal(t, []string{"example.com/acme/thing"}, Equivalents("example.com/acme/thing"))
}
