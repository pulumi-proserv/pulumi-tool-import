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
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
)

// StackSession wraps the Automation API calls injection needs: export the
// current deployment, import a rewritten one, and preview.
type StackSession struct {
	stack      auto.Stack
	projectDir string
	stackName  string
}

// NewStackSession selects an existing stack in the given project directory.
func NewStackSession(ctx context.Context, projectDir, stackName string) (*StackSession, error) {
	ws, err := auto.NewLocalWorkspace(ctx, auto.WorkDir(projectDir))
	if err != nil {
		return nil, fmt.Errorf("creating workspace: %w", err)
	}
	s, err := auto.SelectStack(ctx, stackName, ws)
	if err != nil {
		return nil, fmt.Errorf("selecting stack %s: %w", stackName, err)
	}
	return &StackSession{stack: s, projectDir: projectDir, stackName: stackName}, nil
}

// Export returns the stack's current deployment in the same shape
// "pulumi stack export" writes: the full {"version":…,"deployment":{…}} envelope.
//
// auto.Stack.Export returns an apitype.UntypedDeployment whose Deployment field
// is only the inner object, so it must be re-marshalled whole. Returning
// dep.Deployment alone would fail every consumer here — PatchState,
// InjectNonImportable and VerifyDeploymentIntegrity all read the envelope — with
// a misleading "state missing deployment". Marshalling the struct also preserves
// Version and Features, which Import needs back.
func (s *StackSession) Export(ctx context.Context) ([]byte, error) {
	dep, err := s.stack.Export(ctx)
	if err != nil {
		return nil, fmt.Errorf("exporting stack: %w", err)
	}
	data, err := json.Marshal(dep)
	if err != nil {
		return nil, fmt.Errorf("serializing exported deployment: %w", err)
	}
	return data, nil
}

// Import replaces the stack's deployment.
func (s *StackSession) Import(ctx context.Context, state []byte) error {
	var untyped apitype.UntypedDeployment
	if err := json.Unmarshal(state, &untyped); err != nil {
		return fmt.Errorf("parsing state for import: %w", err)
	}
	if err := s.stack.Import(ctx, untyped); err != nil {
		return fmt.Errorf("importing stack state: %w", err)
	}
	return nil
}

// PreviewJSON runs "pulumi preview --json" and parses the result.
//
// auto.Stack.Preview cannot be used: it tails an --event-log stream whose
// StepEventStateMetadata carries no dependency edges, and optpreview has no JSON
// option. Running the CLI through the workspace's own PulumiCommand keeps the
// binary, working directory, and environment the Automation API resolved.
func (s *StackSession) PreviewJSON(ctx context.Context) (*PreviewDigest, error) {
	stdout, stderr, code, err := s.stack.Workspace().PulumiCommand().Run(
		ctx, s.projectDir, nil, nil, nil, nil, previewJSONArgs(s.stackName)...)
	if err != nil {
		return nil, fmt.Errorf("running preview (exit %d): %w\n%s", code, err, stderr)
	}
	return ParsePreviewJSON([]byte(stdout))
}

// previewJSONArgs builds the CLI args for "pulumi preview --json".
//
// --show-sames is required, not cosmetic: shouldShow (pulumi/pkg/v3's
// backend/display/display.go) returns opts.ShowSameResources for OpSame, and
// cmd/pulumi's preview command (operations/preview.go) defaults
// --show-sames to false with nothing forcing it under --json. Without this
// flag "same" steps are absent from preview.json entirely — every URN
// CheckInjectedOps looks up then misses, and a correct injection is reported
// as unverified and reverted. Do not remove this flag as "redundant": it is
// the only thing making "same" steps appear at all. Split out as its own
// function so a test can assert on it without shelling out to the real CLI.
func previewJSONArgs(stackName string) []string {
	return []string{"preview", "--json", "--show-sames", "--stack", stackName}
}

// CheckInjectedOps reports every injected resource the preview does not show as
// unchanged. An empty result means the injection verified.
//
// "pulumi preview" reporting zero operations is the only check that validates
// injected values. "pulumi refresh" is not: for these resource types Read either
// sets no attributes or re-derives them from the resource ID, so refresh reports
// "unchanged" even when the values in state are wrong.
func CheckInjectedOps(preview *PreviewDigest, injectedURNs []string) []string {
	ops := preview.OpsByURN()
	reasons := preview.DiffReasonsByURN()
	var problems []string
	for _, urn := range injectedURNs {
		op, ok := ops[urn]
		if !ok {
			problems = append(problems, fmt.Sprintf(
				"%s: no step in the preview — the program does not declare this resource", urn))
			continue
		}
		if op != "same" {
			problems = append(problems, fmt.Sprintf(
				"%s: preview reports %q, expected \"same\"%s", urn, op, formatDiffReasons(reasons[urn])))
		}
	}
	return problems
}

// maxDiffReasonsShown caps how many property keys formatDiffReasons lists
// inline, so one resource with a huge diff cannot flood the terminal.
const maxDiffReasonsShown = 8

// formatDiffReasons renders the property keys behind an unexpected preview
// op as a trailing parenthetical for a one-line failure message, e.g.
// " (differs on: routeTableId, vpnGatewayId)". When the preview reported an
// op but no per-property reasons for it, that absence is itself a clue —
// it points at metadata (e.g. resource options, provider version) rather
// than a property value — so it is called out explicitly instead of being
// rendered as an empty "()".
func formatDiffReasons(reasons []string) string {
	if len(reasons) == 0 {
		return " (no property-level diff reported)"
	}
	shown := reasons
	var more string
	if len(reasons) > maxDiffReasonsShown {
		shown = reasons[:maxDiffReasonsShown]
		more = fmt.Sprintf(", and %d more", len(reasons)-maxDiffReasonsShown)
	}
	return fmt.Sprintf(" (differs on: %s%s)", strings.Join(shown, ", "), more)
}

// CheckPreviewClean reports every step of the preview whose operation is not
// "same". It is a diagnostic — how many operations remain outstanding — not a
// pass/fail gate: patch-state runs iteratively against a stack mid-migration,
// which nearly always still has diffs after a single patch pass, so demanding
// zero remaining operations would revert almost every legitimate run. Gating
// is CheckInjectionVerification's job.
func CheckPreviewClean(preview *PreviewDigest) []string {
	var problems []string
	for _, step := range preview.Steps {
		if step.Op != "same" {
			problems = append(problems, fmt.Sprintf(
				"%s: preview reports %q, expected \"same\"", step.URN, step.Op))
		}
	}
	return problems
}

// CheckInjectionVerification is the verification gate for a stack-mode run.
// It compares the preview taken before any mutation (baseline) against the
// preview taken after import (verify), per the design's Verification section:
// injected URNs must all settle to "same", and the mutation must not make
// anything else worse.
//
// "Worse" is judged by comparison, not an absolute bar, because patch-state is
// run iteratively against a stack mid-migration — that is why the operator is
// running it — so the preview taken before a patch-only run almost always has
// outstanding diffs already. Requiring a perfectly clean preview after every
// pass would revert nearly every legitimate run. What must not happen is
// regression: a resource that reported "same" (or was entirely absent) in the
// baseline must not turn non-"same" afterward, and the total count of
// non-"same" steps outside the injected set must not increase.
//
// An empty result means the mutation verified and should be kept.
func CheckInjectionVerification(baseline, verify *PreviewDigest, injectedURNs []string) []string {
	problems := CheckInjectedOps(verify, injectedURNs)

	injected := make(map[string]bool, len(injectedURNs))
	for _, urn := range injectedURNs {
		injected[urn] = true
	}

	baseOps := baseline.OpsByURN()
	verifyOps := verify.OpsByURN()
	verifyReasons := verify.DiffReasonsByURN()

	baseNonSame := 0
	for urn, op := range baseOps {
		if !injected[urn] && op != "same" {
			baseNonSame++
		}
	}

	verifyNonSame := 0
	var newlyDirty []string
	var escalated []string
	for urn, op := range verifyOps {
		if injected[urn] || op == "same" {
			continue
		}
		verifyNonSame++
		baseOp, ok := baseOps[urn]
		if !ok || baseOp == "same" {
			// Named with the properties behind the diff, the same as
			// CheckInjectedOps does for injected resources. Without them a
			// failure here reports only a URN, which is not enough to act on:
			// the e2e run of 2026-08-15 hit exactly this on a patched Lambda
			// and the cause could not be recovered from the log afterwards.
			newlyDirty = append(newlyDirty, fmt.Sprintf(
				"%s reports %q%s", urn, op, formatDiffReasons(verifyReasons[urn])))
			continue
		}
		// Already non-"same" before the run, so neither check above fires and
		// the aggregate count below cannot see it either — the resource is one
		// of baseNonSame AND one of verifyNonSame, so the totals match. But an
		// operation can get WORSE while staying non-"same": many not_read
		// fields are ForceNew, so a wrongly patched value turns "update" into
		// "replace", and the next "pulumi up" then destroys and recreates a
		// live resource. Comparing severity is what makes "must not make
		// things worse" mean the operation as well as the count.
		if opGotWorse(baseOp, op) {
			escalated = append(escalated, fmt.Sprintf(
				"%s escalated from %q to %q%s", urn, baseOp, op,
				formatDiffReasons(verifyReasons[urn])))
		}
	}

	if len(newlyDirty) > 0 {
		sort.Strings(newlyDirty)
		problems = append(problems, fmt.Sprintf(
			"%d resource(s) newly report changes that were unchanged (or absent) before this run:\n    %s",
			len(newlyDirty), strings.Join(newlyDirty, "\n    ")))
	}
	if len(escalated) > 0 {
		sort.Strings(escalated)
		problems = append(problems, fmt.Sprintf(
			"%d resource(s) report a more destructive operation than before this run:\n    %s",
			len(escalated), strings.Join(escalated, "\n    ")))
	}
	if verifyNonSame > baseNonSame {
		problems = append(problems, fmt.Sprintf(
			"preview shows more outstanding changes after the patch than before: %d before, %d after",
			baseNonSame, verifyNonSame))
	}

	return problems
}

// opSeverity ranks preview operations by how much of the live resource they
// destroy, so "must not make things worse" can be judged on the operation and
// not only on how many resources report one.
//
// The ranking is what matters, not the absolute numbers: "same" is no change,
// an update mutates in place, a create means the resource is missing from
// state entirely, and the replace/delete family destroys something that
// exists.
var opSeverity = map[string]int{
	"same":    0,
	"refresh": 0,
	"read":    0,

	"import": 1,
	"update": 1,

	"create": 2,

	"replace":            3,
	"create-replacement": 3,
	"delete-replaced":    3,
	"import-replacement": 3,

	"delete":                 4,
	"discard":                4,
	"remove-pending-replace": 4,
}

// opGotWorse reports whether a resource's operation became more destructive
// between the baseline and the verifying preview.
//
// An operation this table does not know is reported rather than assumed
// benign: guessing wrong in that direction keeps a stack whose verification
// silently failed, while guessing wrong the other way costs one revert of a
// run the operator can repeat. The engine's op vocabulary can grow, so an
// unrecognised value is a reason to stop, not to continue.
func opGotWorse(before, after string) bool {
	if before == after {
		return false
	}
	beforeRank, beforeKnown := opSeverity[before]
	afterRank, afterKnown := opSeverity[after]
	if !beforeKnown || !afterKnown {
		return true
	}
	return afterRank > beforeRank
}
