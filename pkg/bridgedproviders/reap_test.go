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

package bridgedproviders

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

const cacheDir = "/home/u/.pulumi/dynamic_tf_plugins"

const psOutput = `
  101     1 /home/u/.pulumi/dynamic_tf_plugins/registry.opentofu.org/hashicorp/time/0.12.1/linux_amd64/terraform-provider-time
  102  4000 /home/u/.pulumi/dynamic_tf_plugins/registry.opentofu.org/hashicorp/time/0.12.1/linux_amd64/terraform-provider-time
  103     1 /home/u/.pulumi/dynamic_tf_plugins2/registry.opentofu.org/hashicorp/time/0.12.1/linux_amd64/terraform-provider-time
  104     1 /home/u/.pulumi/plugins/resource-aws-v7.27.0/pulumi-resource-aws 127.0.0.1:57548
  105     1 /home/u/.pulumi/dynamic_tf_plugins/registry.terraform.io/go-gandi/gandi/2.3.0/darwin_arm64/terraform-provider-gandi_v2.3.0 -debug
 bogus     1 /home/u/.pulumi/dynamic_tf_plugins/x/terraform-provider-x
`

func TestParseProviderProcesses_KeepsOnlyProcessesUnderTheCache(t *testing.T) {
	got := parseProviderProcesses(psOutput, cacheDir)
	assert.Equal(t, []providerProcess{
		{pid: 101, ppid: 1},
		{pid: 102, ppid: 4000},
		{pid: 105, ppid: 1},
	}, got, "a sibling directory sharing the cache dir as a prefix, an unrelated "+
		"plugin, and an unparseable pid must all be excluded")
}

func TestOrphans_SelectsProcessesWhoseParentIsGone(t *testing.T) {
	procs := parseProviderProcesses(psOutput, cacheDir)
	assert.Equal(t, []int{101, 105}, orphans(procs),
		"a provider whose parent is still alive belongs to a live caller and must be left alone")
}
