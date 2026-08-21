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

// Mode selection is what decides whether a run gets verification, so it is
// pinned directly. File mode is chosen by --state; stack mode is everything
// else, and may also write --out — the verified artifact — so an operator
// never has to choose between verification and having the file.
func TestPatchStateMode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name                                  string
		statePath, outPath, projectDir, stack string
		previewJSON, nonImportable            string
		wantStack                             bool
		wantErr                               string
	}{
		{name: "file mode", statePath: "s.json", outPath: "o.json", wantStack: false},
		{name: "stack mode", projectDir: ".", stack: "dev", wantStack: true},
		{
			name:       "stack mode with --out writes the verified artifact",
			projectDir: ".", stack: "dev", outPath: "o.json", wantStack: true,
		},
		{
			name: "--state without --out", statePath: "s.json",
			wantErr: "file mode needs both",
		},
		{
			name: "stack mode without a stack", outPath: "",
			wantErr: "stack mode needs",
		},
		{
			name: "preview-json is file mode only", projectDir: ".", stack: "dev",
			previewJSON: "p.json", wantErr: "--preview-json",
		},
		{
			name: "injection in file mode needs a preview", statePath: "s.json", outPath: "o.json",
			nonImportable: "n.json", wantErr: "--non-importable requires --preview-json",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stackMode, err := patchStateMode(
				tc.statePath, tc.outPath, tc.projectDir, tc.stack, tc.previewJSON, tc.nonImportable)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantStack, stackMode)
		})
	}
}
