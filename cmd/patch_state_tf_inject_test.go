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

func TestPatchStateTf_NonImportableRequiresPreview(t *testing.T) {
	t.Parallel()
	cmd := newPatchStateTfCmd()
	cmd.SetArgs([]string{
		"--state", "state.json",
		"--digest", "digest.json",
		"--fields", "fields.json",
		"--config-dir", ".",
		"--out", "out.json",
		"--non-importable", "imports-ready.non-importable.json",
	})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--preview-json")
	assert.Contains(t, err.Error(), "pulumi preview --json")
}
