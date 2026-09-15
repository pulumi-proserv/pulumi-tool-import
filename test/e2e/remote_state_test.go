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

//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Terraform Cloud organization and workspace the remote-state test reads.
// The workspace holds state for test/e2e/testdata/tfc-remote/main.tf and is
// never written to by the test; see that file for its (public) contents.
const (
	tfcHostname     = "app.terraform.io"
	tfcOrganization = "import-tool-test"
	tfcWorkspace    = "tool-import-e2e"
	tfcTokenEnv     = "TFC_TOKEN"
)

// TestRemoteStateTerraformCloud proves `digest tf` pulls state and workspace
// variables from a real Terraform Cloud workspace, and that a wrong workspace
// or token fails with the request and status in the message. The unit tests
// in pkg/tfc run the same exchange against fake servers shaped like the
// responses observed on 2026-09-15; this test is what notices when the real
// API drifts from those shapes.
//
// Needs TFC_TOKEN set to a token that can read the workspace. Creates
// nothing: the workspace's state is a single terraform_data resource that
// was applied once, by hand, when the fixture was set up.
func TestRemoteStateTerraformCloud(t *testing.T) {
	if os.Getenv(tfcTokenEnv) == "" {
		t.Skipf("%s is not set; set it to a Terraform Cloud token that can read %s/%s "+
			"(the one `terraform login` stores works)", tfcTokenEnv, tfcOrganization, tfcWorkspace)
	}

	ctx := context.Background()
	repoRoot := repoRoot(t)
	binPath := buildTool(t, ctx, repoRoot)
	fixtureDir := filepath.Join(repoRoot, "test", "e2e", "testdata", "tfc-remote")
	env := sanitizedEnv()

	digestArgs := func(workspace, tokenEnv, out string) []string {
		return []string{"digest", "tf",
			"--from", fixtureDir,
			"--hostname", tfcHostname,
			"--organization", tfcOrganization,
			"--workspace", workspace,
			"--token-env", tokenEnv,
			"--out", out,
			"--pulumi-project", "toolimport",
			"--pulumi-stack", "e2e",
			"--skip-secrets",
			"--skip-import-check",
		}
	}

	t.Run("PullsStateAndVariables", func(t *testing.T) {
		digestPath := filepath.Join(t.TempDir(), "tf-digest.json")
		out, err := runToolAllowFail(t, ctx, binPath, repoRoot, env,
			digestArgs(tfcWorkspace, tfcTokenEnv, digestPath)...)
		if err != nil {
			t.Fatalf("digest tf against %s/%s failed: %v\n%s", tfcOrganization, tfcWorkspace, err, out)
		}
		if !strings.Contains(out, "Fetched 1 workspace variables") {
			t.Errorf("expected the one workspace variable (greeting) to be fetched; output:\n%s", out)
		}

		raw, err := os.ReadFile(digestPath)
		if err != nil {
			t.Fatalf("reading digest: %v", err)
		}
		var digest struct {
			RootResources []struct {
				TerraformAddress string `json:"terraformAddress"`
				ImportID         string `json:"importId"`
				Attributes       struct {
					ID    string `json:"id"`
					Input struct {
						Value string `json:"value"`
					} `json:"input"`
				} `json:"attributes"`
			} `json:"rootResources"`
		}
		if err := json.Unmarshal(raw, &digest); err != nil {
			t.Fatalf("parsing digest: %v\n%s", err, raw)
		}
		if len(digest.RootResources) != 1 {
			t.Fatalf("expected exactly one root resource in the digest, got %d:\n%s", len(digest.RootResources), raw)
		}
		res := digest.RootResources[0]
		if res.TerraformAddress != "terraform_data.fixture" {
			t.Errorf("root resource address = %q, want terraform_data.fixture", res.TerraformAddress)
		}
		if res.ImportID == "" || res.ImportID != res.Attributes.ID {
			t.Errorf("importId %q should be the resource's id %q", res.ImportID, res.Attributes.ID)
		}
		if res.Attributes.Input.Value != "hello" {
			t.Errorf("input.value = %q, want \"hello\" (the value applied into the workspace state)", res.Attributes.Input.Value)
		}
	})

	t.Run("WrongWorkspaceNamesRequestAndStatus", func(t *testing.T) {
		out, err := runToolAllowFail(t, ctx, binPath, repoRoot, env,
			digestArgs("no-such-workspace", tfcTokenEnv, filepath.Join(t.TempDir(), "x.json"))...)
		if err == nil {
			t.Fatalf("expected failure for a nonexistent workspace; output:\n%s", out)
		}
		wantURL := "GET https://" + tfcHostname + "/api/v2/organizations/" + tfcOrganization + "/workspaces/no-such-workspace"
		for _, want := range []string{"workspace " + tfcOrganization + "/no-such-workspace not found", wantURL, "404 Not Found"} {
			if !strings.Contains(out, want) {
				t.Errorf("error should contain %q; output:\n%s", want, out)
			}
		}
	})

	t.Run("BadTokenNamesRequestAndStatus", func(t *testing.T) {
		const badTokenEnv = "TFC_E2E_BAD_TOKEN"
		badEnv := sanitizedEnv(badTokenEnv + "=not-a-token")
		out, err := runToolAllowFail(t, ctx, binPath, repoRoot, badEnv,
			digestArgs(tfcWorkspace, badTokenEnv, filepath.Join(t.TempDir(), "x.json"))...)
		if err == nil {
			t.Fatalf("expected failure for a bad token; output:\n%s", out)
		}
		for _, want := range []string{"authentication failed for " + tfcHostname, "401 Unauthorized", "check token in env var " + badTokenEnv} {
			if !strings.Contains(out, want) {
				t.Errorf("error should contain %q; output:\n%s", want, out)
			}
		}
	})
}
