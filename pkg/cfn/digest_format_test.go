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

package cfn

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStackDigestFormatVersion_RoundTripsAndRejectsNewer(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	path := filepath.Join(dir, "cfn-digest.json")
	require.NoError(t, WriteStackDigest(&StackDigest{StackName: "s"}, path))
	d, err := LoadStackDigest(path)
	require.NoError(t, err)
	assert.Equal(t, CurrentDigestFormatVersion, d.FormatVersion)

	future := filepath.Join(dir, "future.json")
	require.NoError(t, os.WriteFile(future, []byte(`{"digestFormatVersion":99,"stackName":"s"}`), 0o644))
	_, err = LoadStackDigest(future)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "99")

	legacy := filepath.Join(dir, "legacy.json")
	require.NoError(t, os.WriteFile(legacy, []byte(`{"stackName":"s"}`), 0o644))
	d, err = LoadStackDigest(legacy)
	require.NoError(t, err)
	assert.Equal(t, 0, d.FormatVersion)
}

func TestLoadStackDigest_PreservesLargeIntegers(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "cfn-digest.json")
	data := []byte(`{"stackName":"s","resources":[{"logicalId":"r","attributes":{"n":1234567890123456789}}]}`)
	require.NoError(t, os.WriteFile(path, data, 0o644))

	d, err := LoadStackDigest(path)
	require.NoError(t, err)
	require.Len(t, d.Resources, 1)
	assert.Equal(t, "1234567890123456789",
		d.Resources[0].Attributes["n"].(interface{ String() string }).String())
}
