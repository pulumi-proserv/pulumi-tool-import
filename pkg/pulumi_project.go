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
	"fmt"
	"os"
	"path/filepath"
)

// ensurePulumiProject makes sure projectDir contains a Pulumi.yaml, creating a
// minimal one (from projectName + runtime) when it is absent. It returns an
// error if no Pulumi.yaml exists and no runtime was supplied to create one.
func ensurePulumiProject(projectDir, projectName, runtime string) error {
	pulumiYaml := filepath.Join(projectDir, "Pulumi.yaml")
	if _, err := os.Stat(pulumiYaml); err == nil {
		return nil // already exists
	}

	if runtime == "" {
		return fmt.Errorf("no Pulumi.yaml found in %s and no --runtime specified to create one", projectDir)
	}

	// Ensure the directory exists.
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		return fmt.Errorf("creating project directory: %w", err)
	}

	content := fmt.Sprintf("name: %s\nruntime: %s\n", projectName, runtime)
	fmt.Fprintf(os.Stderr, "Creating minimal Pulumi.yaml for project %q (runtime: %s)\n", projectName, runtime)
	if err := os.WriteFile(pulumiYaml, []byte(content), 0o644); err != nil {
		return fmt.Errorf("writing Pulumi.yaml: %w", err)
	}
	return nil
}
