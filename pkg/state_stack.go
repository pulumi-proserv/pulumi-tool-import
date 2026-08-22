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

type StackSession struct {
	stack      auto.Stack
	projectDir string
	stackName  string
}

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

// Export returns the full {"version":…,"deployment":{…}} envelope that
// "pulumi stack export" writes. auto.Stack.Export returns an
// apitype.UntypedDeployment whose Deployment field is only the inner object,
// so it must be re-marshalled whole; returning dep.Deployment alone fails
// every consumer here with a misleading "state missing deployment".
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

// refreshPreviewJSONArgs builds the CLI args for the refresh report run.
// --preview-only is load-bearing: a real refresh can DELETE an injected
// resource from state when Read reports it gone, resurrecting the destructive
// create this feature exists to prevent. This path must never write.
func refreshPreviewJSONArgs(stackName string) []string {
	return []string{"refresh", "--preview-only", "--json", "--stack", stackName}
}

// RefreshPreviewJSON runs "pulumi refresh --preview-only --json" and parses
// the result — the same step envelope as PreviewJSON, with oldState populated.
func (s *StackSession) RefreshPreviewJSON(ctx context.Context) (*PreviewDigest, error) {
	stdout, stderr, code, err := s.stack.Workspace().PulumiCommand().Run(
		ctx, s.projectDir, nil, nil, nil, nil, refreshPreviewJSONArgs(s.stackName)...)
	if err != nil {
		return nil, fmt.Errorf("pulumi refresh --preview-only failed (exit %d): %w\n%s", code, err, stderr)
	}
	if code != 0 {
		return nil, fmt.Errorf("pulumi refresh --preview-only failed (exit %d)\n%s", code, stderr)
	}
	return ParsePreviewJSON([]byte(stdout))
}

// PreviewJSON runs "pulumi preview --json" and parses the result.
// auto.Stack.Preview cannot be used: it tails an event stream whose
// StepEventStateMetadata carries no dependency edges, and optpreview has no
// JSON option.
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
// --show-sames is required, not cosmetic: the preview command defaults it to
// false, and without it "same" steps are absent from the JSON entirely, so
// every URN CheckInjectedOps looks up misses and a correct injection is
// reported as unverified and reverted. Do not remove it as redundant.
func previewJSONArgs(stackName string) []string {
	return []string{"preview", "--json", "--show-sames", "--stack", stackName}
}

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

const maxDiffReasonsShown = 8

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

// CheckPreviewClean reports every step whose operation is not "same". It is a
// diagnostic, not a pass/fail gate — gating is CheckInjectionVerification's job.
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

// CheckInjectionVerification is the verification gate for a stack-mode run:
// injected URNs must all settle to "same", and the mutation must not make
// anything else worse. An empty result means the mutation verified.
//
// "Worse" is judged by comparison against the pre-mutation baseline, not an
// absolute bar: patch-state runs against a stack mid-migration, which nearly
// always has outstanding diffs already, so demanding a clean preview would
// revert almost every legitimate run.
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
			newlyDirty = append(newlyDirty, fmt.Sprintf(
				"%s reports %q%s", urn, op, formatDiffReasons(verifyReasons[urn])))
			continue
		}
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

// opSeverity ranks operations by how much of the live resource they destroy,
// so "must not make things worse" can be judged on the operation and not only
// on how many resources report one. The ranking is what matters, not the
// numbers.
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

// opGotWorse reports whether an operation became more destructive between the
// two previews — many not_read fields are ForceNew, so a wrongly patched value
// turns "update" into "replace" while staying non-"same", which no count can
// see. An operation this table does not know is reported rather than assumed
// benign; the engine's op vocabulary can grow.
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
