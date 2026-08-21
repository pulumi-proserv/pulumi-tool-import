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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These prove the command wires the flags — including cobra's Changed("state"),
// which the unit table cannot exercise — into patchStateMode.

func TestPatchStateTf_CommandRoutesFlagsIntoModeSelection(t *testing.T) {
	t.Parallel()
	cmd := newPatchStateTfCmd()
	cmd.SetArgs([]string{
		"--state", "state.json",
		"--digest", "digest.json",
		"--fields", "fields.json",
		"--config-dir", ".",
	})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "file mode needs both --state and --out")
}

func TestPatchStateTf_ExplicitlyEmptyStateIsRejected(t *testing.T) {
	t.Parallel()
	cmd := newPatchStateTfCmd()
	cmd.SetArgs([]string{
		"--state", "",
		"--out", "o.json",
		"--project-dir", ".",
		"--stack", "dev",
		"--digest", "digest.json",
		"--fields", "fields.json",
		"--config-dir", ".",
	})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--state is empty")
}
