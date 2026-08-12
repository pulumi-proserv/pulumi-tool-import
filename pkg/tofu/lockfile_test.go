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

package tofu

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLockedProviderVersionsReadsLockFile(t *testing.T) {
	t.Parallel()
	versions, err := LockedProviderVersions("testdata/tf-project-with-lockfile")
	require.NoError(t, err)
	assert.Equal(t, "3.7.2", versions["registry.terraform.io/hashicorp/random"])
}

func TestLockedProviderVersionsWithoutLockFile(t *testing.T) {
	t.Parallel()
	versions, err := LockedProviderVersions("testdata/tf-project")
	require.NoError(t, err)
	assert.Empty(t, versions)
}
