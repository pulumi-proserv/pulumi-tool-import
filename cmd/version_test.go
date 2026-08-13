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
	"bytes"
	"testing"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg/version"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The release build stamps pkg/version.Version through ldflags; without a
// command to print it the value is compiled in but unreachable, so there is no
// way to tell which build is installed.
func TestVersionCommandPrintsTheStampedVersion(t *testing.T) {
	cmd := newVersionCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)

	require.NoError(t, cmd.Execute())

	assert.Equal(t, version.Version+"\n", out.String())
}

func TestVersionCommandIsRegistered(t *testing.T) {
	t.Parallel()
	found, _, err := rootCmd.Find([]string{"version"})
	require.NoError(t, err)
	assert.Equal(t, "version", found.Name())
}
