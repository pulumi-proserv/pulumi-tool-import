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

// This test creates and destroys real infrastructure in AWS. It reads AWS
// credentials from the environment and knows nothing about ESC or which
// account it is pointed at — providing valid, scoped-down credentials is the
// caller's job. Run it with:
//
//	esc run <your-aws-environment> -- \
//	  env -u AWS_PROFILE go test -tags e2e ./test/e2e/ -v -timeout 40m
//
// "env -u AWS_PROFILE" matters whenever the shell exports AWS_PROFILE: it
// shadows brokered credentials and makes every AWS call fail with "the config
// profile (...) could not be found". See sanitizedEnv in helpers.go for the
// in-process backstop.
//
// A full run takes roughly 10 minutes (the VPN connection alone is 3-5
// minutes to create and to destroy). Start it detached — output redirected
// to a file with "&", or under a tool like `nohup` — rather than in a
// foreground shell with its own timeout: a shell or harness that kills the
// process mid-run leaves t.Cleanup's "tofu destroy" without a chance to run,
// which is exactly how a VPN connection got orphaned during an earlier
// manual run of this pipeline.
//
// See also `make test-e2e`.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg"
)

// pulumiProject must match testdata/pulumi/Pulumi.yaml's "name:" field —
// it is one half of the URN this test builds to look up preview steps by
// resource, and it is also passed to "digest tf" so the digest's own URN
// construction agrees with the real Pulumi program.
const pulumiProject = "tool-import-e2e"

// TestPatchStateTfNonImportableInjection drives the full pipeline —
// tofu apply, digest tf, resolve tf, pulumi import, patch-state tf
// --non-importable — against real AWS infrastructure, and proves the two
// resource types that declare no Terraform importer
// (aws_vpn_gateway_route_propagation, aws_vpn_connection_route) go from
// previewing as "create" to previewing as "same".
//
// "pulumi refresh" cannot make this claim: it reports these resource types
// unchanged even when the injected values are wrong, because their Read
// either sets no attributes or re-derives them from the ID alone. Only
// "pulumi preview" — compared before and after injection — validates the
// values that were written into state. See docs/non-importable-resources.md
// and the design/plan under docs/superpowers/{specs,plans}/*non-importable*.
func TestPatchStateTfNonImportableInjection(t *testing.T) {
	ctx := context.Background()

	logCallerIdentity(t, ctx)
	requireBinary(t, "tofu")
	requireBinary(t, "pulumi")
	requireGlobalLoginUntouched(t)

	repoRoot := repoRoot(t)
	binPath := buildTool(t, ctx, repoRoot)

	tfDir := filepath.Join(t.TempDir(), "tf")
	if err := copyDir(filepath.Join(repoRoot, "test", "e2e", "testdata", "tf"), tfDir); err != nil {
		t.Fatalf("copying tf fixture: %v", err)
	}
	pulumiDir := filepath.Join(t.TempDir(), "pulumi")
	if err := copyDir(filepath.Join(repoRoot, "test", "e2e", "testdata", "pulumi"), pulumiDir); err != nil {
		t.Fatalf("copying pulumi fixture: %v", err)
	}

	// A local file backend keeps this test self-contained: it does not need
	// a Pulumi Cloud org to exist, or credentials for one, beyond whatever
	// "esc run" already put in the environment for AWS.
	//
	// PULUMI_HOME is isolated alongside the backend, and there is
	// deliberately no "pulumi login" call: login is what writes
	// ~/.pulumi/credentials.json, and a login against this test's temporary
	// backend directory would leave the developer's global CLI state
	// pointing at a path that stops existing the moment the test cleans up
	// its temp dirs — every later "pulumi"/"esc" command in that shell then
	// fails until the developer runs "pulumi login" by hand to repair it.
	// PULUMI_BACKEND_URL alone is sufficient to select the backend for every
	// command below; requireGlobalLoginUntouched (via t.Cleanup) proves the
	// global credentials file was never touched, rather than assuming it.
	tmpRoot := t.TempDir()
	backendDir := filepath.Join(tmpRoot, "backend")
	pulumiHomeDir := filepath.Join(tmpRoot, "pulumi-home")
	for _, dir := range []string{backendDir, pulumiHomeDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("creating %s: %v", dir, err)
		}
	}
	env := sanitizedEnv(
		"PULUMI_BACKEND_URL=file://"+backendDir,
		"PULUMI_CONFIG_PASSPHRASE=",
		"PULUMI_HOME="+pulumiHomeDir,
	)
	stackName := fmt.Sprintf("e2e-%d", time.Now().UnixNano())

	runPulumi(t, ctx, pulumiDir, env, "stack", "init", stackName)
	runPulumi(t, ctx, pulumiDir, env, "config", "set", "aws:region", "us-west-2")

	// --- Create the real fixture. -------------------------------------
	runTofu(t, ctx, tfDir, env, "init", "-input=false")
	runTofu(t, ctx, tfDir, env, "apply", "-auto-approve", "-input=false")
	// Registered immediately after apply succeeds, so it runs on failure or
	// panic too. The VPN connection this fixture creates costs money and
	// takes 3-5 minutes to tear down each way.
	t.Cleanup(func() {
		out, err := runTofuCombined(ctx, tfDir, env, "destroy", "-auto-approve", "-input=false")
		if err != nil {
			t.Errorf("tofu destroy failed — clean up by hand from %s (terraform state left in place):\n%v\n%s",
				tfDir, err, out)
		}
	})

	tfStatePath := filepath.Join(tfDir, "terraform.tfstate")

	// --- digest tf: enumerate resources and flag the non-importable ones.
	digestPath := filepath.Join(t.TempDir(), "tf-digest.json")
	runTool(t, ctx, binPath, repoRoot, env, "digest", "tf",
		"--from", tfDir,
		"--state-file", tfStatePath,
		"--out", digestPath,
		"--pulumi-stack", stackName,
		"--pulumi-project", pulumiProject,
		"--project-dir", pulumiDir,
		"--skip-secrets", // fixture has no sensitive attributes to carry through config
	)

	// --- Generate the import-file skeleton from the real Pulumi program.
	// "resolve tf" only fills IDs into entries that already exist; it does
	// not invent them, so this is the step that actually determines the
	// resource names the digest must match. See the comment at the top of
	// testdata/pulumi/Pulumi.yaml for how those names were derived and
	// verified before this test could run against AWS at all.
	importSkeletonPath := filepath.Join(t.TempDir(), "import-skeleton.json")
	runPulumi(t, ctx, pulumiDir, env, "preview",
		"--stack", stackName, "--import-file", importSkeletonPath)

	// --- resolve tf: fill importable IDs, drop the non-importable ones
	// into a sidecar.
	filledImportPath := filepath.Join(t.TempDir(), "filled-import.json")
	runTool(t, ctx, binPath, repoRoot, env, "resolve", "tf",
		"--digest", digestPath,
		"--import-file", importSkeletonPath,
		"--out", filledImportPath,
	)

	sidecarPath := nonImportableSidecarPath(filledImportPath)
	sidecar, err := pkg.LoadNonImportableFile(sidecarPath)
	if err != nil {
		t.Fatalf("loading non-importable sidecar %s: %v", sidecarPath, err)
	}
	wantNonImportable := map[string]string{
		"prop[0]": "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation",
		"prop[1]": "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation",
		"prop[2]": "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation",
		"route":   "aws:ec2/vpnConnectionRoute:VpnConnectionRoute",
	}
	if len(sidecar.Resources) != len(wantNonImportable) {
		t.Fatalf("sidecar has %d resource(s), want %d: %+v",
			len(sidecar.Resources), len(wantNonImportable), sidecar.Resources)
	}
	for _, r := range sidecar.Resources {
		wantType, ok := wantNonImportable[r.Name]
		if !ok {
			t.Errorf("sidecar has unexpected resource %q (type %s)", r.Name, r.Type)
			continue
		}
		if r.Type != wantType {
			t.Errorf("sidecar resource %q has type %q, want %q", r.Name, r.Type, wantType)
		}
	}
	if t.Failed() {
		t.FailNow()
	}

	// --- Bring the importable resources into state. -------------------
	runPulumi(t, ctx, pulumiDir, env, "import",
		"--file", filledImportPath,
		"--stack", stackName,
		"--yes",
		"--generate-code=false",
	)

	// --- Assert the "before" direction: without this, the "after"
	// assertion could pass vacuously if, say, a name mismatch meant these
	// resources were never previewed at all. That exact failure mode — a
	// check that only looked one direction — was found in this branch's
	// stack-mode verification during review.
	before := runPreviewJSON(t, ctx, pulumiDir, env, stackName)
	beforeOps := before.OpsByURN()
	for _, r := range sidecar.Resources {
		urn := expectedURN(pulumiProject, stackName, r.Type, r.Name)
		op, ok := beforeOps[urn]
		if !ok {
			t.Fatalf("before injection: %s has no step in the preview at all "+
				"(want \"create\") — the program may not declare a matching resource", urn)
		}
		if op != "create" {
			t.Fatalf("before injection: %s previews as %q, want \"create\"", urn, op)
		}
	}
	t.Logf("confirmed %d non-importable resource(s) preview as \"create\" before injection", len(sidecar.Resources))

	// --- Inject. --------------------------------------------------------
	backupDir := t.TempDir()
	runTool(t, ctx, binPath, repoRoot, env, "patch-state", "tf",
		"--project-dir", pulumiDir,
		"--stack", stackName,
		"--digest", digestPath,
		"--fields", filepath.Join(repoRoot, "data", "aws-import-diff-fields.json"),
		"--config-dir", tfDir,
		"--non-importable", sidecarPath,
		"--backup-dir", backupDir,
	)

	// --- Assert the "after" direction. -----------------------------------
	after := runPreviewJSON(t, ctx, pulumiDir, env, stackName)
	afterOps := after.OpsByURN()
	for _, r := range sidecar.Resources {
		urn := expectedURN(pulumiProject, stackName, r.Type, r.Name)
		op, ok := afterOps[urn]
		if !ok {
			t.Errorf("after injection: %s has no step in the preview at all (want \"same\")", urn)
			continue
		}
		if op != "same" {
			t.Errorf("after injection: %s previews as %q, want \"same\" — "+
				"the injected values do not match reality", urn, op)
		}
	}
	if !t.Failed() {
		t.Logf("confirmed %d non-importable resource(s) preview as \"same\" after injection", len(sidecar.Resources))
	}
}

// requireCredentials skips the test when the environment is not set up for
// it, rather than failing. PULUMI_ACCESS_TOKEN is checked as a signal that
// the ESC wrapper was actually used — this test's own Pulumi operations run
// against a local file backend and do not consume the token directly, but
// its absence means AWS credentials almost certainly aren't present either.
// Note on what gates this test: the only credentials it needs are AWS ones.
// It runs against an isolated local file backend (PULUMI_BACKEND_URL plus its
// own PULUMI_HOME), so it needs no Pulumi Cloud login and no
// PULUMI_ACCESS_TOKEN. An earlier version gated on PULUMI_ACCESS_TOKEN, which
// silently skipped the whole test whenever the caller brokered AWS credentials
// without also exporting a Pulumi token — a skip that looks like a pass.
// logCallerIdentity below is the gate: if AWS is unreachable, it skips.

// logCallerIdentity records which AWS account and role this run is about to
// create infrastructure in. It does not gate on the result — which account
// is correct to use is the operator's responsibility, not this test's — but
// an unreachable or unconfigured AWS CLI is exactly the "no AWS
// credentials" case this test should skip rather than fail on.
func logCallerIdentity(t *testing.T, ctx context.Context) {
	t.Helper()
	cmd := exec.CommandContext(ctx, "aws", "sts", "get-caller-identity", "--output", "json")
	cmd.Env = sanitizedEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("aws sts get-caller-identity failed (no AWS credentials?): %v\n"+
			"run via the ESC wrapper documented at the top of this file (also: make test-e2e)", err)
	}
	var identity struct {
		Account string `json:"Account"`
		Arn     string `json:"Arn"`
		UserID  string `json:"UserId"`
	}
	if jsonErr := json.Unmarshal(out, &identity); jsonErr != nil {
		t.Fatalf("parsing aws sts get-caller-identity output: %v\n%s", jsonErr, out)
	}
	t.Logf("running against AWS account %s as %s", identity.Account, identity.Arn)
}

// requireGlobalLoginUntouched snapshots the developer's global Pulumi
// credentials file and registers a t.Cleanup that fails the test if it
// changed. This test isolates PULUMI_HOME for every "pulumi" invocation it
// makes precisely so this file is never touched — but a test that can
// corrupt the developer's login should prove that it doesn't, rather than
// assume its own isolation is airtight.
func requireGlobalLoginUntouched(t *testing.T) {
	t.Helper()
	path := globalCredentialsPath()
	before, beforeExists, err := readFileIfExists(path)
	if err != nil {
		t.Fatalf("reading global pulumi credentials %s: %v", path, err)
	}
	t.Cleanup(func() {
		after, afterExists, err := readFileIfExists(path)
		if err != nil {
			t.Errorf("re-reading global pulumi credentials %s: %v", path, err)
			return
		}
		if beforeExists != afterExists || !bytes.Equal(before, after) {
			t.Errorf("global Pulumi login at %s changed during this test run "+
				"(existed before=%v, after=%v) — every \"pulumi\" call this test makes should "+
				"carry an isolated PULUMI_HOME, so this should be impossible; if it fired, run "+
				"\"pulumi login\" to repair your shell", path, beforeExists, afterExists)
		}
	})
}

// globalCredentialsPath resolves the credentials file "pulumi login" writes
// to in the ambient (non-test) environment, honoring an already-set
// PULUMI_HOME the same way the pulumi CLI does.
func globalCredentialsPath() string {
	home := os.Getenv("PULUMI_HOME")
	if home == "" {
		if dir, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(dir, ".pulumi")
		}
	}
	return filepath.Join(home, "credentials.json")
}

// readFileIfExists reads path, treating "does not exist" as a valid, empty
// result rather than an error.
func readFileIfExists(path string) (data []byte, exists bool, err error) {
	data, err = os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return data, true, nil
}

func requireBinary(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%q not found on PATH: %v", name, err)
	}
}

// repoRoot returns the repository root, computed from this file's own path
// rather than the working directory `go test` happens to run from.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed to resolve this file's path")
	}
	// This file lives at <repoRoot>/test/e2e/e2e_test.go.
	root, err := filepath.Abs(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}
	return root
}

// buildTool builds the CLI binary under test from the working tree, rather
// than assuming an installed copy is on PATH and matches this branch.
func buildTool(t *testing.T, ctx context.Context, repoRoot string) string {
	t.Helper()
	binPath := filepath.Join(t.TempDir(), "pulumi-tool-import")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", binPath, ".")
	cmd.Dir = repoRoot
	cmd.Env = sanitizedEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("building pulumi-tool-import: %v\n%s", err, out)
	}
	return binPath
}

// runTool runs a subcommand of the binary under test.
func runTool(t *testing.T, ctx context.Context, binPath, dir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	t.Logf("$ %s %s\n%s", binPath, strings.Join(args, " "), out)
	if err != nil {
		t.Fatalf("%s %s: %v", binPath, strings.Join(args, " "), err)
	}
}

// runPulumi runs the pulumi CLI in dir with the given environment.
func runPulumi(t *testing.T, ctx context.Context, dir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, "pulumi", args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	t.Logf("$ pulumi %s\n%s", strings.Join(args, " "), out)
	if err != nil {
		t.Fatalf("pulumi %s: %v", strings.Join(args, " "), err)
	}
}

// runPreviewJSON runs "pulumi preview --json --show-sames" and parses the
// result.
//
// --show-sames is not optional here: shouldShow (pulumi/pkg/v3's
// backend/display/display.go) returns opts.ShowSameResources for OpSame,
// and preview's own JSON output defaults that to false with nothing forcing
// it on. Without this flag, every resource this test expects to see as
// "same" after injection is simply absent from the preview's steps — and
// the "after" assertion below would then fail to find a step at all for a
// correctly injected resource, which reads exactly like an injection
// failure. Do not remove this flag as redundant.
func runPreviewJSON(t *testing.T, ctx context.Context, dir string, env []string, stackName string) *pkg.PreviewDigest {
	t.Helper()
	cmd := exec.CommandContext(ctx, "pulumi", "preview", "--json", "--show-sames", "--stack", stackName)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		stderr := ""
		if errors.As(err, &exitErr) {
			stderr = string(exitErr.Stderr)
		}
		t.Fatalf("pulumi preview --json --show-sames: %v\n%s", err, stderr)
	}
	digest, err := pkg.ParsePreviewJSON(out)
	if err != nil {
		t.Fatalf("parsing preview JSON: %v", err)
	}
	return digest
}

// runTofu runs the tofu CLI in dir, failing the test on error.
func runTofu(t *testing.T, ctx context.Context, dir string, env []string, args ...string) {
	t.Helper()
	out, err := runTofuCombined(ctx, dir, env, args...)
	t.Logf("$ tofu %s\n%s", strings.Join(args, " "), out)
	if err != nil {
		t.Fatalf("tofu %s: %v", strings.Join(args, " "), err)
	}
}

// runTofuCombined runs the tofu CLI in dir and returns its combined output,
// without failing the test — used by the destroy cleanup, which must report
// through t.Error (not Fatal) so it never panics during cleanup.
func runTofuCombined(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "tofu", args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}
