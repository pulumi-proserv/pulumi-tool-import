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
		ctx, s.projectDir, nil, nil, nil, nil,
		"preview", "--json", "--stack", s.stackName)
	if err != nil {
		return nil, fmt.Errorf("running preview (exit %d): %w\n%s", code, err, stderr)
	}
	return ParsePreviewJSON([]byte(stdout))
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
				"%s: preview reports %q, expected \"same\"", urn, op))
		}
	}
	return problems
}
