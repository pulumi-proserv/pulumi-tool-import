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

const orderURNPrefix = "urn:pulumi:dev::proj::aws:ec2/x:X::"

func injectedObj(name string, deps []string, parent string) map[string]interface{} {
	obj := map[string]interface{}{"urn": orderURNPrefix + name}
	if len(deps) > 0 {
		ds := make([]interface{}, 0, len(deps))
		for _, d := range deps {
			ds = append(ds, orderURNPrefix+d)
		}
		obj["dependencies"] = ds
	}
	if parent != "" {
		obj["parent"] = orderURNPrefix + parent
	}
	return obj
}

func orderOf(objs []map[string]interface{}) []string {
	names := make([]string, 0, len(objs))
	for _, o := range objs {
		urn, _ := o["urn"].(string)
		names = append(names, urn[len(orderURNPrefix):])
	}
	return names
}

func TestOrderInjected_DependencyMovesAhead(t *testing.T) {
	t.Parallel()
	objs := []map[string]interface{}{
		injectedObj("b", []string{"a"}, ""),
		injectedObj("a", nil, ""),
	}
	orderInjected(objs)
	assert.Equal(t, []string{"a", "b"}, orderOf(objs))
}

func TestOrderInjected_ParentEdgeMovesAhead(t *testing.T) {
	t.Parallel()
	objs := []map[string]interface{}{
		injectedObj("child", nil, "parent"),
		injectedObj("parent", nil, ""),
	}
	orderInjected(objs)
	assert.Equal(t, []string{"parent", "child"}, orderOf(objs))
}

func TestOrderInjected_TransitiveChain(t *testing.T) {
	t.Parallel()
	objs := []map[string]interface{}{
		injectedObj("c", []string{"b"}, ""),
		injectedObj("b", []string{"a"}, ""),
		injectedObj("a", nil, ""),
	}
	orderInjected(objs)
	assert.Equal(t, []string{"a", "b", "c"}, orderOf(objs))
}

func TestOrderInjected_DiamondPutsEveryDependencyFirst(t *testing.T) {
	t.Parallel()
	objs := []map[string]interface{}{
		injectedObj("top", []string{"left", "right"}, ""),
		injectedObj("left", []string{"base"}, ""),
		injectedObj("right", []string{"base"}, ""),
		injectedObj("base", nil, ""),
	}
	orderInjected(objs)

	got := orderOf(objs)
	require.Len(t, got, 4, "no resource may be dropped or duplicated")
	pos := map[string]int{}
	for i, n := range got {
		pos[n] = i
	}
	assert.Less(t, pos["base"], pos["left"])
	assert.Less(t, pos["base"], pos["right"])
	assert.Less(t, pos["left"], pos["top"])
	assert.Less(t, pos["right"], pos["top"])
}

func TestOrderInjected_MixedParentAndDependencyEdges(t *testing.T) {
	t.Parallel()
	objs := []map[string]interface{}{
		injectedObj("leaf", []string{"sibling"}, "root"),
		injectedObj("sibling", nil, "root"),
		injectedObj("root", nil, ""),
	}
	orderInjected(objs)
	assert.Equal(t, []string{"root", "sibling", "leaf"}, orderOf(objs))
}

func TestOrderInjected_UnrelatedResourcesKeepSidecarOrder(t *testing.T) {
	t.Parallel()
	objs := []map[string]interface{}{
		injectedObj("first", nil, ""),
		injectedObj("second", nil, ""),
		injectedObj("third", nil, ""),
	}
	orderInjected(objs)
	assert.Equal(t, []string{"first", "second", "third"}, orderOf(objs))
}

func TestOrderInjected_IgnoresEdgesOutsideTheBatch(t *testing.T) {
	t.Parallel()
	objs := []map[string]interface{}{
		{
			"urn":          orderURNPrefix + "only",
			"dependencies": []interface{}{"urn:pulumi:dev::proj::aws:ec2/vpc:Vpc::main"},
			"parent":       "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev",
		},
	}
	orderInjected(objs)
	assert.Equal(t, []string{"only"}, orderOf(objs))
}

func TestOrderInjected_SelfReferenceIsIgnored(t *testing.T) {
	t.Parallel()
	objs := []map[string]interface{}{
		injectedObj("solo", []string{"solo"}, ""),
	}
	orderInjected(objs)
	assert.Equal(t, []string{"solo"}, orderOf(objs))
}

func TestOrderInjected_CycleTerminatesAndKeepsEveryResource(t *testing.T) {
	t.Parallel()
	objs := []map[string]interface{}{
		injectedObj("a", []string{"b"}, ""),
		injectedObj("b", []string{"c"}, ""),
		injectedObj("c", []string{"a"}, ""),
	}

	orderInjected(objs)

	got := orderOf(objs)
	assert.ElementsMatch(t, []string{"a", "b", "c"}, got,
		"a cycle must not drop or duplicate a resource — VerifyDeploymentIntegrity "+
			"is the backstop that rejects the result, and it can only do that if every "+
			"resource is still present")

	pos := map[string]int{}
	for i, n := range got {
		pos[n] = i
	}
	unsatisfied := 0
	for dependent, dependency := range map[string]string{"a": "b", "b": "c", "c": "a"} {
		if pos[dependency] > pos[dependent] {
			unsatisfied++
		}
	}
	assert.Positive(t, unsatisfied,
		"a cycle cannot be fully ordered, so at least one forward reference must remain "+
			"for VerifyDeploymentIntegrity to catch")
}
