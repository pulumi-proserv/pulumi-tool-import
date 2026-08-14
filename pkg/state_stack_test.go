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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPreviewJSONArgs_IncludesShowSames is the regression test for the
// whole-branch finding that "pulumi preview --json" silently omits "same"
// steps without --show-sames (shouldShow in pulumi/pkg/v3's
// backend/display/display.go defaults opts.ShowSameResources to false, and
// nothing forces it under --json). Before the fix, previewJSONArgs did not
// include the flag — this test fails against that version and passes now.
func TestPreviewJSONArgs_IncludesShowSames(t *testing.T) {
	t.Parallel()
	args := previewJSONArgs("my-stack")
	assert.Contains(t, args, "--show-sames")
	assert.Contains(t, args, "--json")
	assert.Contains(t, args, "my-stack")
}

// These fixtures hand-write {"op":"same",...} steps to exercise
// CheckInjectedOps/CheckPreviewClean/CheckInjectionVerification in isolation.
// That is a deliberate simplification, not a claim about what real "pulumi
// preview --json" output looks like: without --show-sames, the real CLI
// omits "same" steps from the JSON entirely (see the comment on
// StackSession.PreviewJSON in state_stack.go, and shouldShow in
// pulumi/pkg/v3's backend/display/display.go). PreviewJSON always passes
// --show-sames, so these fixtures are a faithful stand-in for its actual
// output — they would NOT be faithful to what "pulumi preview --json" prints
// on its own.
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

// The CheckInjectionVerification fixtures below also hand-write "same" steps
// for the same reason noted above TestCheckInjectedOps_AllSame: they test the
// comparison logic in isolation, and are only a faithful stand-in for real
// "pulumi preview --json" output because PreviewJSON always passes
// --show-sames (see previewJSONArgs and TestPreviewJSONArgs_IncludesShowSames).
func TestCheckInjectionVerification_BaselineDirtyPostIdentical(t *testing.T) {
	t.Parallel()
	baseline, err := ParsePreviewJSON([]byte(`{"steps": [
		{"op": "update", "urn": "urn:pulumi:dev::proj::aws:ec2/x:X::a"}
	]}`))
	require.NoError(t, err)
	verify, err := ParsePreviewJSON([]byte(`{"steps": [
		{"op": "update", "urn": "urn:pulumi:dev::proj::aws:ec2/x:X::a"}
	]}`))
	require.NoError(t, err)

	problems := CheckInjectionVerification(baseline, verify, nil)
	assert.Empty(t, problems)
}

func TestCheckInjectionVerification_BaselineDirtyPostCleaner(t *testing.T) {
	t.Parallel()
	baseline, err := ParsePreviewJSON([]byte(`{"steps": [
		{"op": "update", "urn": "urn:pulumi:dev::proj::aws:ec2/x:X::a"},
		{"op": "update", "urn": "urn:pulumi:dev::proj::aws:ec2/y:Y::b"}
	]}`))
	require.NoError(t, err)
	verify, err := ParsePreviewJSON([]byte(`{"steps": [
		{"op": "same", "urn": "urn:pulumi:dev::proj::aws:ec2/x:X::a"},
		{"op": "update", "urn": "urn:pulumi:dev::proj::aws:ec2/y:Y::b"}
	]}`))
	require.NoError(t, err)

	problems := CheckInjectionVerification(baseline, verify, nil)
	assert.Empty(t, problems)
}

func TestCheckInjectionVerification_BaselineCleanPostDirtyIsAProblem(t *testing.T) {
	t.Parallel()
	baseline, err := ParsePreviewJSON([]byte(`{"steps": [
		{"op": "same", "urn": "urn:pulumi:dev::proj::aws:ec2/x:X::a"}
	]}`))
	require.NoError(t, err)
	verify, err := ParsePreviewJSON([]byte(`{"steps": [
		{"op": "update", "urn": "urn:pulumi:dev::proj::aws:ec2/x:X::a"}
	]}`))
	require.NoError(t, err)

	problems := CheckInjectionVerification(baseline, verify, nil)
	require.NotEmpty(t, problems)
	joined := strings.Join(problems, "\n")
	assert.Contains(t, joined, "X::a")
}

func TestCheckInjectionVerification_NewlyDirtyURNIsNamedInMessage(t *testing.T) {
	t.Parallel()
	// Net count of non-"same" steps stays the same (one resource improves,
	// another regresses), but the specific regression must still be caught
	// and named — a stable total is not enough if it hides a real regression.
	baseline, err := ParsePreviewJSON([]byte(`{"steps": [
		{"op": "same", "urn": "urn:pulumi:dev::proj::aws:ec2/x:X::a"},
		{"op": "update", "urn": "urn:pulumi:dev::proj::aws:ec2/y:Y::b"}
	]}`))
	require.NoError(t, err)
	verify, err := ParsePreviewJSON([]byte(`{"steps": [
		{"op": "update", "urn": "urn:pulumi:dev::proj::aws:ec2/x:X::a"},
		{"op": "same", "urn": "urn:pulumi:dev::proj::aws:ec2/y:Y::b"}
	]}`))
	require.NoError(t, err)

	problems := CheckInjectionVerification(baseline, verify, nil)
	require.NotEmpty(t, problems)
	joined := strings.Join(problems, "\n")
	assert.Contains(t, joined, "urn:pulumi:dev::proj::aws:ec2/x:X::a")
}

func TestCheckInjectionVerification_InjectedURNMustBeSame(t *testing.T) {
	t.Parallel()
	baseline, err := ParsePreviewJSON([]byte(`{"steps": []}`))
	require.NoError(t, err)
	verify, err := ParsePreviewJSON([]byte(`{"steps": [
		{"op": "replace", "urn": "urn:pulumi:dev::proj::aws:ec2/x:X::a"}
	]}`))
	require.NoError(t, err)

	problems := CheckInjectionVerification(baseline, verify, []string{"urn:pulumi:dev::proj::aws:ec2/x:X::a"})
	require.NotEmpty(t, problems)
	joined := strings.Join(problems, "\n")
	assert.Contains(t, joined, "replace")
}
