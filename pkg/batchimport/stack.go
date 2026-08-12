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

package batchimport

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optimport"
)

// stackImporter is the production Importer, backed by the Automation API.
type stackImporter struct {
	stack auto.Stack
}

// NewStackImporter selects an existing stack in projectDir.
func NewStackImporter(ctx context.Context, projectDir, stackName string) (Importer, error) {
	ws, err := auto.NewLocalWorkspace(ctx, auto.WorkDir(projectDir))
	if err != nil {
		return nil, fmt.Errorf("creating workspace: %w", err)
	}
	s, err := auto.SelectStack(ctx, stackName, ws)
	if err != nil {
		return nil, fmt.Errorf("selecting stack %q: %w", stackName, err)
	}
	return &stackImporter{stack: s}, nil
}

// ImportBatch runs pulumi import for the given resources.
//
// Protect and GenerateCode are always disabled: the migration workflow
// hand-authors its program, and protected resources produce a spurious
// ~protect diff. The returned error is diagnostic only — see Run.
func (s *stackImporter) ImportBatch(
	ctx context.Context,
	rs []*optimport.ImportResource,
	nameTable map[string]string,
) error {
	_, err := s.stack.ImportResources(ctx,
		optimport.Resources(rs),
		optimport.NameTable(nameTable),
		optimport.Protect(false),
		optimport.GenerateCode(false),
		optimport.ProgressStreams(os.Stderr),
		optimport.ErrorProgressStreams(os.Stderr),
	)
	return err
}

// ExistingResources reads the stack's current resources.
func (s *stackImporter) ExistingResources(ctx context.Context) (map[ResourceKey]bool, error) {
	dep, err := s.stack.Export(ctx)
	if err != nil {
		return nil, fmt.Errorf("exporting stack state: %w", err)
	}
	return resourceKeysFromDeployment(dep.Deployment)
}

// resourceKeysFromDeployment parses the inner deployment object returned by
// Stack.Export — {"resources": [...]}, with no outer "deployment" wrapper.
func resourceKeysFromDeployment(raw []byte) (map[ResourceKey]bool, error) {
	keys := map[ResourceKey]bool{}
	if len(raw) == 0 {
		return keys, nil
	}
	var dep struct {
		Resources []struct {
			URN string `json:"urn"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(raw, &dep); err != nil {
		return nil, fmt.Errorf("parsing stack deployment: %w", err)
	}
	for _, r := range dep.Resources {
		if k, ok := ParseURN(r.URN); ok {
			keys[k] = true
		}
	}
	return keys, nil
}
