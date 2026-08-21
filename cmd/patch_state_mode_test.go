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

func TestPatchStateMode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		flags     patchStateFlags
		wantStack bool
		wantErr   string
	}{
		{name: "file mode", flags: patchStateFlags{StatePath: "s.json", StateFlagSet: true, OutPath: "o.json"}},
		{name: "stack mode", flags: patchStateFlags{ProjectDir: ".", Stack: "dev"}, wantStack: true},
		{
			name:      "--out does not force file mode",
			flags:     patchStateFlags{ProjectDir: ".", Stack: "dev", OutPath: "o.json"},
			wantStack: true,
		},
		{
			name: "state and out with stack flags stays file mode",
			flags: patchStateFlags{
				StatePath: "s.json", StateFlagSet: true, OutPath: "o.json",
				ProjectDir: ".", Stack: "dev",
			},
		},
		{
			name: "explicitly empty --state is an error, never stack mode",
			flags: patchStateFlags{
				StatePath: "", StateFlagSet: true, OutPath: "o.json",
				ProjectDir: ".", Stack: "dev",
			},
			wantErr: "--state is empty",
		},
		{
			name:    "--state without --out",
			flags:   patchStateFlags{StatePath: "s.json", StateFlagSet: true},
			wantErr: "file mode needs both",
		},
		{
			name:    "--out alone, no stack flags",
			flags:   patchStateFlags{OutPath: "o.json"},
			wantErr: "stack mode needs",
		},
		{
			name:    "no flags at all",
			flags:   patchStateFlags{},
			wantErr: "stack mode needs",
		},
		{
			name:    "preview-json is file mode only",
			flags:   patchStateFlags{ProjectDir: ".", Stack: "dev", PreviewJSON: "p.json"},
			wantErr: "--preview-json",
		},
		{
			name: "injection in file mode needs a preview",
			flags: patchStateFlags{
				StatePath: "s.json", StateFlagSet: true, OutPath: "o.json",
				NonImportable: "n.json",
			},
			wantErr: "--non-importable requires --preview-json",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stackMode, err := patchStateMode(tc.flags)
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
