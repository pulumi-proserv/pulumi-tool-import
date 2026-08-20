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

func TestPreviewJSONArgs_IncludesShowSames(t *testing.T) {
	t.Parallel()
	args := previewJSONArgs("my-stack")
	assert.Contains(t, args, "--show-sames")
	assert.Contains(t, args, "--json")
	assert.Contains(t, args, "my-stack")
}

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

func TestCheckInjectionVerification_NewlyDirtyURNNamesDiffReasons(t *testing.T) {
	t.Parallel()
	baseline, err := ParsePreviewJSON([]byte(`{"steps": [
		{"op": "same", "urn": "urn:pulumi:dev::proj::aws:lambda/function:Function::target"}
	]}`))
	require.NoError(t, err)
	verify, err := ParsePreviewJSON([]byte(`{"steps": [
		{"op": "update", "urn": "urn:pulumi:dev::proj::aws:lambda/function:Function::target",
		 "diffReasons": ["code", "sourceCodeHash"]}
	]}`))
	require.NoError(t, err)

	problems := CheckInjectionVerification(baseline, verify, nil)
	require.NotEmpty(t, problems)
	joined := strings.Join(problems, "\n")
	assert.Contains(t, joined, "code")
	assert.Contains(t, joined, "sourceCodeHash")
}

func TestCheckInjectionVerification_NewlyDirtyWithoutReasonsSaysSo(t *testing.T) {
	t.Parallel()
	baseline, err := ParsePreviewJSON([]byte(`{"steps": [
		{"op": "same", "urn": "urn:pulumi:dev::proj::aws:ec2/x:X::a"}
	]}`))
	require.NoError(t, err)
	verify, err := ParsePreviewJSON([]byte(`{"steps": [
		{"op": "replace", "urn": "urn:pulumi:dev::proj::aws:ec2/x:X::a"}
	]}`))
	require.NoError(t, err)

	problems := CheckInjectionVerification(baseline, verify, nil)
	require.NotEmpty(t, problems)
	joined := strings.Join(problems, "\n")
	assert.Contains(t, joined, "no property-level diff reported")
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

func TestCheckInjectedOps_UpdateNamesDiffReasons(t *testing.T) {
	t.Parallel()
	preview, err := ParsePreviewJSON([]byte(`{"steps": [
		{"op": "update", "urn": "urn:pulumi:dev::proj::aws:ec2/vpnConnectionRoute:VpnConnectionRoute::route",
		 "diffReasons": ["routeTableId", "vpnGatewayId"]}
	]}`))
	require.NoError(t, err)

	problems := CheckInjectedOps(preview, []string{
		"urn:pulumi:dev::proj::aws:ec2/vpnConnectionRoute:VpnConnectionRoute::route",
	})
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0], `preview reports "update", expected "same"`)
	assert.Contains(t, problems[0], "(differs on: routeTableId, vpnGatewayId)")
}

func TestCheckInjectedOps_UpdateWithNoDiffReasonsSaysSo(t *testing.T) {
	t.Parallel()
	preview, err := ParsePreviewJSON([]byte(`{"steps": [
		{"op": "update", "urn": "urn:pulumi:dev::proj::aws:ec2/x:X::a"}
	]}`))
	require.NoError(t, err)

	problems := CheckInjectedOps(preview, []string{"urn:pulumi:dev::proj::aws:ec2/x:X::a"})
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0], "no property-level diff reported")
	assert.NotContains(t, problems[0], "differs on: )")
	assert.NotContains(t, problems[0], "()")
}

func TestCheckInjectedOps_ManyDiffReasonsAreTruncated(t *testing.T) {
	t.Parallel()
	preview, err := ParsePreviewJSON([]byte(`{"steps": [
		{"op": "update", "urn": "urn:pulumi:dev::proj::aws:ec2/x:X::a",
		 "diffReasons": ["a", "b", "c", "d", "e", "f", "g", "h", "i", "j"]}
	]}`))
	require.NoError(t, err)

	problems := CheckInjectedOps(preview, []string{"urn:pulumi:dev::proj::aws:ec2/x:X::a"})
	require.Len(t, problems, 1)
	assert.Len(t, strings.Split(problems[0], "\n"), 1, "must stay one line per resource")
	assert.Contains(t, problems[0], "and 2 more")
}

func TestCheckInjectionVerification_SurfacesDiffReasons(t *testing.T) {
	t.Parallel()
	baseline, err := ParsePreviewJSON([]byte(`{"steps": []}`))
	require.NoError(t, err)
	verify, err := ParsePreviewJSON([]byte(`{"steps": [
		{"op": "update", "urn": "urn:pulumi:dev::proj::aws:ec2/x:X::a", "diffReasons": ["routeTableId"]}
	]}`))
	require.NoError(t, err)

	problems := CheckInjectionVerification(baseline, verify, []string{"urn:pulumi:dev::proj::aws:ec2/x:X::a"})
	require.NotEmpty(t, problems)
	joined := strings.Join(problems, "\n")
	assert.Contains(t, joined, "differs on: routeTableId")
}

func TestCheckInjectionVerification_EscalationIsAProblem(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, before, after string
	}{
		{"update to replace", "update", "replace"},
		{"update to delete", "update", "delete"},
		{"update to create-replacement", "update", "create-replacement"},
		{"create to delete", "create", "delete"},
		{"unrecognised op", "update", "some-future-op"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			baseline, err := ParsePreviewJSON([]byte(`{"steps": [
				{"op": "` + tc.before + `", "urn": "urn:pulumi:dev::proj::aws:ec2/x:X::a"}
			]}`))
			require.NoError(t, err)
			verify, err := ParsePreviewJSON([]byte(`{"steps": [
				{"op": "` + tc.after + `", "urn": "urn:pulumi:dev::proj::aws:ec2/x:X::a"}
			]}`))
			require.NoError(t, err)

			problems := CheckInjectionVerification(baseline, verify, nil)
			require.NotEmpty(t, problems, "%s -> %s must be reported", tc.before, tc.after)
			joined := strings.Join(problems, "\n")
			assert.Contains(t, joined, "X::a")
			assert.Contains(t, joined, tc.before)
			assert.Contains(t, joined, tc.after)
		})
	}
}

func TestCheckInjectionVerification_EscalationSwapIsAProblem(t *testing.T) {
	t.Parallel()
	baseline, err := ParsePreviewJSON([]byte(`{"steps": [
		{"op": "update", "urn": "urn:pulumi:dev::proj::aws:ec2/x:X::a"},
		{"op": "update", "urn": "urn:pulumi:dev::proj::aws:ec2/y:Y::b"}
	]}`))
	require.NoError(t, err)
	verify, err := ParsePreviewJSON([]byte(`{"steps": [
		{"op": "delete", "urn": "urn:pulumi:dev::proj::aws:ec2/x:X::a"},
		{"op": "replace", "urn": "urn:pulumi:dev::proj::aws:ec2/y:Y::b"}
	]}`))
	require.NoError(t, err)

	problems := CheckInjectionVerification(baseline, verify, nil)
	require.NotEmpty(t, problems)
	joined := strings.Join(problems, "\n")
	assert.Contains(t, joined, "X::a")
	assert.Contains(t, joined, "Y::b")
}

func TestCheckInjectionVerification_DeEscalationIsNotAProblem(t *testing.T) {
	t.Parallel()
	baseline, err := ParsePreviewJSON([]byte(`{"steps": [
		{"op": "replace", "urn": "urn:pulumi:dev::proj::aws:ec2/x:X::a"}
	]}`))
	require.NoError(t, err)
	verify, err := ParsePreviewJSON([]byte(`{"steps": [
		{"op": "update", "urn": "urn:pulumi:dev::proj::aws:ec2/x:X::a"}
	]}`))
	require.NoError(t, err)

	assert.Empty(t, CheckInjectionVerification(baseline, verify, nil))
}

func TestOpGotWorse(t *testing.T) {
	t.Parallel()
	assert.False(t, opGotWorse("update", "update"))
	assert.False(t, opGotWorse("replace", "update"))
	assert.False(t, opGotWorse("update", "same"))
	assert.True(t, opGotWorse("update", "replace"))
	assert.True(t, opGotWorse("same", "delete"))
	assert.True(t, opGotWorse("create", "replace"))
	assert.True(t, opGotWorse("update", "brand-new-op"))
	assert.True(t, opGotWorse("brand-new-op", "update"))
}
