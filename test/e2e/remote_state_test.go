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

// Each host's fixture workspace holds state for the config in fixtureDir and
// only public test data; the tests read it and never write. The pkg/tfc unit
// tests pin each host's response shapes as observed on a given day; these
// tests are what notice when a real API drifts from them.
type remoteHost struct {
	hostname     string
	organization string
	workspace    string
	tokenEnv     string
	fixtureDir   string
}

var terraformCloud = remoteHost{
	hostname:     "app.terraform.io",
	organization: "import-tool-test",
	workspace:    "tool-import-e2e",
	tokenEnv:     "TFC_TOKEN",
	fixtureDir:   "tfc-remote",
}

// Pulumi Cloud stores a workspace as the stack org/project/stack, hence the
// Pulumi organization and a project_stack name.
var pulumiCloud = remoteHost{
	hostname:     "tf.pulumi.com",
	organization: "team-ce",
	workspace:    "toolimport_e2e",
	tokenEnv:     "PULUMI_ACCESS_TOKEN",
	fixtureDir:   "pulumi-cloud-remote",
}

// On Scalr the TFE-compatible organization is the environment ID.
var scalr = remoteHost{
	hostname:     "pulumi-proserv.scalr.io",
	organization: "env-v0pdr54u1htkjaue6",
	workspace:    "tool-import-e2e",
	tokenEnv:     "SCALR_TOKEN",
	fixtureDir:   "scalr-remote",
}

func TestRemoteStateTerraformCloud(t *testing.T) {
	h := terraformCloud
	fx := newRemoteFixture(t, h)

	t.Run("PullsStateAndVariables", func(t *testing.T) {
		out, digestPath := fx.digest(t, h.workspace, h.tokenEnv, nil)
		fx.assertOutput(t, out, "Fetched 1 workspace variables (1 workspace-scoped, 0 environment-scoped): greeting [workspace]")
		assertFixtureDigest(t, digestPath)
	})

	t.Run("WrongWorkspaceNamesRequestAndStatus", func(t *testing.T) {
		out := fx.digestFails(t, "no-such-workspace", h.tokenEnv, nil)
		fx.assertOutput(t, out,
			"workspace "+h.organization+"/no-such-workspace not found",
			"GET https://"+h.hostname+"/api/v2/organizations/"+h.organization+"/workspaces/no-such-workspace",
			"→ 404",
		)
	})
}

func TestRemoteStatePulumiCloud(t *testing.T) {
	h := pulumiCloud
	fx := newRemoteFixture(t, h)

	t.Run("PullsStateAndWarnsAboutVariables", func(t *testing.T) {
		out, digestPath := fx.digest(t, h.workspace, h.tokenEnv, nil)
		fx.assertOutput(t, out,
			"Warning: could not fetch workspace variables",
			"→ 404",
			"Continuing with local tfvars only.",
		)
		assertFixtureDigest(t, digestPath)
	})

	t.Run("SlashNameGetsNamingHint", func(t *testing.T) {
		out := fx.digestFails(t, "toolimport/e2e", h.tokenEnv, nil)
		fx.assertOutput(t, out,
			"workspace "+h.organization+"/toolimport/e2e not found",
			"→ 404",
			"<project>_<stack>",
		)
	})
}

func TestRemoteStateScalr(t *testing.T) {
	h := scalr
	fx := newRemoteFixture(t, h)

	t.Run("PullsStateAndBothVariableScopes", func(t *testing.T) {
		out, digestPath := fx.digest(t, h.workspace, h.tokenEnv, nil)
		// greeting exists at both scopes on the fixture; the workspace one must win.
		fx.assertOutput(t, out,
			"Fetched 2 workspace variables (1 workspace-scoped, 1 environment-scoped): env_scoped [environment], greeting [workspace]")
		assertFixtureDigest(t, digestPath)
	})

	t.Run("WrongWorkspaceNamesRequestAndStatus", func(t *testing.T) {
		out := fx.digestFails(t, "no-such-workspace", h.tokenEnv, nil)
		fx.assertOutput(t, out,
			"workspace "+h.organization+"/no-such-workspace not found",
			"GET https://"+h.hostname+"/api/tfe/v2/organizations/"+h.organization+"/workspaces/no-such-workspace",
			"→ 404",
		)
	})
}

type remoteFixture struct {
	host       remoteHost
	ctx        context.Context
	binPath    string
	repoRoot   string
	fixtureDir string
}

func newRemoteFixture(t *testing.T, h remoteHost) *remoteFixture {
	t.Helper()
	if os.Getenv("REMOTE_STATE_E2E") == "off" {
		t.Skip("REMOTE_STATE_E2E=off: the tests against third-party hosts are switched off")
	}
	if os.Getenv(h.tokenEnv) == "" {
		t.Skipf("%s is not set; set it to a token that can read %s/%s on %s",
			h.tokenEnv, h.organization, h.workspace, h.hostname)
	}
	ctx := context.Background()
	root := repoRoot(t)
	return &remoteFixture{
		host:       h,
		ctx:        ctx,
		binPath:    buildTool(t, ctx, root),
		repoRoot:   root,
		fixtureDir: filepath.Join(root, "test", "e2e", "testdata", h.fixtureDir),
	}
}

func (fx *remoteFixture) args(workspace, tokenEnv, out string) []string {
	return []string{"digest", "tf",
		"--from", fx.fixtureDir,
		"--hostname", fx.host.hostname,
		"--organization", fx.host.organization,
		"--workspace", workspace,
		"--token-env", tokenEnv,
		"--out", out,
		"--pulumi-project", "toolimport",
		"--pulumi-stack", "e2e",
		"--skip-secrets",
		"--skip-import-check",
	}
}

func (fx *remoteFixture) digest(t *testing.T, workspace, tokenEnv string, extraEnv []string) (out, digestPath string) {
	t.Helper()
	digestPath = filepath.Join(t.TempDir(), "tf-digest.json")
	out, err := runToolAllowFail(t, fx.ctx, fx.binPath, fx.repoRoot, sanitizedEnv(extraEnv...),
		fx.args(workspace, tokenEnv, digestPath)...)
	if err != nil {
		t.Fatalf("digest tf against %s %s/%s failed: %v\n%s", fx.host.hostname, fx.host.organization, workspace, err, out)
	}
	return out, digestPath
}

func (fx *remoteFixture) digestFails(t *testing.T, workspace, tokenEnv string, extraEnv []string) string {
	t.Helper()
	out, err := runToolAllowFail(t, fx.ctx, fx.binPath, fx.repoRoot, sanitizedEnv(extraEnv...),
		fx.args(workspace, tokenEnv, filepath.Join(t.TempDir(), "x.json"))...)
	if err == nil {
		t.Fatalf("expected digest tf against %s %s/%s to fail; output:\n%s", fx.host.hostname, fx.host.organization, workspace, out)
	}
	return out
}

// assertOutput checks only what the client itself composes — its own
// wording, the request it built, the status code — never a vendor's message
// text, so a reworded error page cannot fail the gate.
func (fx *remoteFixture) assertOutput(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Errorf("output should contain %q (if only this host fails, check whether %s changed its API); output:\n%s",
				want, fx.host.hostname, out)
		}
	}
}

func assertFixtureDigest(t *testing.T, digestPath string) {
	t.Helper()
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
}
