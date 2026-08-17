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

// orderInjected is the one piece of the injection path that has never had
// real work to do: the e2e fixture's non-importable resources all depend on
// IMPORTABLE ones, which are already earlier in the deployment array and need
// no reordering. So every live run has exercised the no-op case only, and
// TestInjectNonImportable_OrdersDependenciesFirst covers a single edge.
//
// These tests supply the work: transitive chains, parent edges, cycles, and
// edges pointing outside the batch. Recorded as gap 2 in
// docs/superpowers/plans/2026-08-14-remaining-test-coverage.md.

const orderURNPrefix = "urn:pulumi:dev::proj::aws:ec2/x:X::"

// injectedObj builds the minimal shape orderInjected reads: a URN, plus
// whichever of "dependencies"/"parent" the case needs.
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

// orderOf returns the resulting names in array order, which is what
// VerifyIntegrity checks: a dependency must appear before its dependent.
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
	// b depends on a, but the sidecar lists b first — the case that would
	// produce a forward reference if nothing reordered.
	objs := []map[string]interface{}{
		injectedObj("b", []string{"a"}, ""),
		injectedObj("a", nil, ""),
	}
	orderInjected(objs)
	assert.Equal(t, []string{"a", "b"}, orderOf(objs))
}

func TestOrderInjected_ParentEdgeMovesAhead(t *testing.T) {
	t.Parallel()
	// The parent edge is consulted separately from "dependencies", so it
	// needs its own case: a child listed first must still land after its
	// parent.
	objs := []map[string]interface{}{
		injectedObj("child", nil, "parent"),
		injectedObj("parent", nil, ""),
	}
	orderInjected(objs)
	assert.Equal(t, []string{"parent", "child"}, orderOf(objs))
}

// TestOrderInjected_TransitiveChain is the case a single-edge test cannot
// reach: c -> b -> a, listed in exactly reverse order, so a correct result
// requires the sort to be transitive rather than a one-pass swap.
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

// TestOrderInjected_DiamondPutsEveryDependencyFirst covers a node reachable
// by two paths, which a naive depth-first walk can emit twice or place wrong.
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

// TestOrderInjected_MixedParentAndDependencyEdges exercises both edge kinds
// in one batch, which is how a real component-parented resource would arrive.
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

// TestOrderInjected_UnrelatedResourcesKeepSidecarOrder pins the stability
// the implementation promises: resources with no edges between them stay in
// the order the sidecar listed them, so injection is reproducible.
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

// TestOrderInjected_IgnoresEdgesOutsideTheBatch covers the documented
// assumption that a reference to a resource already in the deployment needs
// no reordering: such a URN is not in the batch's index, so it must be
// skipped rather than treated as a missing node.
func TestOrderInjected_IgnoresEdgesOutsideTheBatch(t *testing.T) {
	t.Parallel()
	objs := []map[string]interface{}{
		{
			"urn": orderURNPrefix + "only",
			// An importable resource imported earlier — not in this batch.
			"dependencies": []interface{}{"urn:pulumi:dev::proj::aws:ec2/vpc:Vpc::main"},
			"parent":       "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev",
		},
	}
	orderInjected(objs)
	assert.Equal(t, []string{"only"}, orderOf(objs))
}

// TestOrderInjected_SelfReferenceIsIgnored covers the i == j guard. A
// resource listing itself must not become its own predecessor.
func TestOrderInjected_SelfReferenceIsIgnored(t *testing.T) {
	t.Parallel()
	objs := []map[string]interface{}{
		injectedObj("solo", []string{"solo"}, ""),
	}
	orderInjected(objs)
	assert.Equal(t, []string{"solo"}, orderOf(objs))
}

// TestOrderInjected_CycleTerminatesAndKeepsEveryResource pins the documented
// degradation rather than a correct answer, because there is no correct
// answer: a cycle cannot be topologically ordered.
//
// What must hold is that orderInjected terminates (the visiting/done marking
// is what prevents infinite recursion), and that it neither drops nor
// duplicates a resource — because the array it produces is handed to
// VerifyDeploymentIntegrity, which is what actually rejects the result. A
// silent hang or a lost resource would defeat that backstop; an unsatisfiable
// edge merely reaches it.
//
// Not reachable from a real preview, which cannot emit a dependency cycle.
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

	// And state the consequence explicitly: with a cycle, some edge is
	// necessarily unsatisfied, which is exactly what VerifyIntegrity reports.
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
