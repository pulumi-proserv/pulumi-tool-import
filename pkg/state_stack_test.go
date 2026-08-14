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

package pkg

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckInjectedOps_AllSame(t *testing.T) {
	t.Parallel()
	preview, err := ParsePreviewJSON([]byte(`{"steps": [
		{"op": "same", "urn": "urn:pulumi:dev::proj::aws:ec2/x:X::a"},
		{"op": "same", "urn": "urn:pulumi:dev::proj::aws:ec2/y:Y::b"}
	]}`))
	require.NoError(t, err)

	problems := CheckInjectedOps(preview, []string{
		"urn:pulumi:dev::proj::aws:ec2/x:X::a",
		"urn:pulumi:dev::proj::aws:ec2/y:Y::b",
	})
	assert.Empty(t, problems)
}

func TestCheckInjectedOps_ReplaceIsAProblem(t *testing.T) {
	t.Parallel()
	preview, err := ParsePreviewJSON([]byte(`{"steps": [
		{"op": "replace", "urn": "urn:pulumi:dev::proj::aws:ec2/x:X::a"}
	]}`))
	require.NoError(t, err)

	problems := CheckInjectedOps(preview, []string{"urn:pulumi:dev::proj::aws:ec2/x:X::a"})
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0], "replace")
	assert.Contains(t, problems[0], "X::a")
}

func TestCheckInjectedOps_MissingFromPreviewIsAProblem(t *testing.T) {
	t.Parallel()
	// A URN absent from the preview means the resource is not in the program's
	// graph at all — injection put something in state that nothing declares.
	preview, err := ParsePreviewJSON([]byte(`{"steps": []}`))
	require.NoError(t, err)

	problems := CheckInjectedOps(preview, []string{"urn:pulumi:dev::proj::aws:ec2/x:X::a"})
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0], "no step")
}

func TestCheckPreviewClean_AllSame(t *testing.T) {
	t.Parallel()
	preview, err := ParsePreviewJSON([]byte(`{"steps": [
		{"op": "same", "urn": "urn:pulumi:dev::proj::aws:ec2/x:X::a"},
		{"op": "same", "urn": "urn:pulumi:dev::proj::aws:ec2/y:Y::b"}
	]}`))
	require.NoError(t, err)

	assert.Empty(t, CheckPreviewClean(preview))
}

func TestCheckPreviewClean_NoSteps(t *testing.T) {
	t.Parallel()
	preview, err := ParsePreviewJSON([]byte(`{"steps": []}`))
	require.NoError(t, err)

	assert.Empty(t, CheckPreviewClean(preview))
}

func TestCheckPreviewClean_NonSameStepIsAProblem(t *testing.T) {
	t.Parallel()
	preview, err := ParsePreviewJSON([]byte(`{"steps": [
		{"op": "update", "urn": "urn:pulumi:dev::proj::aws:ec2/x:X::a"}
	]}`))
	require.NoError(t, err)

	problems := CheckPreviewClean(preview)
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0], "update")
	assert.Contains(t, problems[0], "X::a")
}
