// Copyright 2016-2026, Pulumi Corporation.
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

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildFormatsGolden(t *testing.T) {
	var warnings []string
	f, sum, err := buildFormats(fixtureRoot(t), "v0.0.0-fixture", func(s string) { warnings = append(warnings, s) })
	require.NoError(t, err)

	// Templates: event target, elasticsearch domain, dynamodb table.
	// ManualFromTests: lambda layer perm, ec2 cross thing, iam static thing, ec2 child thing.
	assert.Equal(t, Summary{Templates: 3, ManualFromTests: 4, ManualFromDocs: 2, SensitiveHits: 1}, sum)
	assert.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "aws_cloudwatch_event_target")
	assert.Contains(t, warnings[0], "target_id")
	assert.NotContains(t, f.Types, "aws_s3_bucket")

	out := filepath.Join(t.TempDir(), "out.json")
	require.NoError(t, writeFormats(out, f))
	got, err := os.ReadFile(out)
	require.NoError(t, err)

	goldenPath := "testdata/golden.json"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		require.NoError(t, os.WriteFile(goldenPath, got, 0o644))
	}
	want, err := os.ReadFile(goldenPath)
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got))
}
