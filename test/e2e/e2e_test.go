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
// A full run takes roughly 10 minutes, almost all of it "tofu apply" and
// "tofu destroy" for the VPN connection (3-5 minutes each way). Everything
// this test proves about patch-state and injection happens against a Pulumi
// stack, which is seconds of work — so the tofu fixture is applied exactly
// once, in TestNonImportableStateInjection, and every scenario below gets its
// own Pulumi stack (and its own working copy of any file it mutates) carved
// out of that one shared fixture. Scenarios never share a stack name or a
// state file, and never run concurrently against the fixture's Terraform
// state, so they stay independent of each other even though the AWS
// resources underneath are shared.
//
// Start it detached — output redirected to a file with "&", or under a tool
// like `nohup` — rather than in a foreground shell with its own timeout: a
// shell or harness that kills the process mid-run leaves t.Cleanup's "tofu
// destroy" without a chance to run, which is exactly how a VPN connection got
// orphaned during an earlier manual run of this pipeline.
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
	"sync/atomic"
	"testing"
	"time"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg"
)

// pulumiProject must match testdata/pulumi/Pulumi.yaml's "name:" field —
// it is one half of the URN this test builds to look up preview steps by
// resource, and it is also passed to "digest tf" so the digest's own URN
// construction agrees with the real Pulumi program.
const pulumiProject = "tool-import-e2e"

// wantNonImportable is the sidecar "resolve tf" is expected to produce for
// testdata/tf/main.tf: the three route-table propagations and the one
// connection route that aws_vpn_gateway_route_propagation and
// aws_vpn_connection_route declare no importer for.
var wantNonImportable = map[string]string{
	"prop[0]": "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation",
	"prop[1]": "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation",
	"prop[2]": "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation",
	"route":   "aws:ec2/vpnConnectionRoute:VpnConnectionRoute",
}

// fixture bundles what is shared, read-only, across every scenario: the
// built binary, the repo root, the applied Terraform fixture, and the
// isolated Pulumi backend every scenario's stack lives in. Nothing here is
// mutated once TestNonImportableStateInjection finishes setting it up, so
// scenarios can share it freely without stepping on each other — only the
// per-scenario Pulumi stack (via provisionStack) and per-scenario file
// copies are exclusive to one scenario.
type fixture struct {
	repoRoot         string
	binPath          string
	tfDir            string
	tfStatePath      string
	pulumiFixtureDir string
	env              []string
}

// TestNonImportableStateInjection applies the shared Terraform fixture once,
// then runs every scenario below against it as a subtest. See the package
// doc comment at the top of this file for why the fixture is shared and how
// scenario isolation is maintained despite that.
func TestNonImportableStateInjection(t *testing.T) {
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
	//
	// One backend directory is shared by every scenario's stack: a file
	// backend holds any number of stacks side by side, distinguished by
	// name, so sharing it is not a source of cross-scenario coupling as
	// long as (see provisionStack) every scenario's stack name is unique.
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

	// --- Create the real fixture, once, shared by every scenario below. ---
	runTofu(t, ctx, tfDir, env, "init", "-input=false")
	runTofu(t, ctx, tfDir, env, "apply", "-auto-approve", "-input=false")
	// Registered immediately after apply succeeds, so it runs on failure or
	// panic too, and so it runs after every subtest below regardless of
	// which ones fail. The VPN connection this fixture creates costs money
	// and takes 3-5 minutes to tear down each way.
	t.Cleanup(func() {
		out, err := runTofuCombined(ctx, tfDir, env, "destroy", "-auto-approve", "-input=false")
		if err != nil {
			t.Errorf("tofu destroy failed — clean up by hand from %s (terraform state left in place):\n%v\n%s",
				tfDir, err, out)
		}
	})

	fx := &fixture{
		repoRoot:         repoRoot,
		binPath:          binPath,
		tfDir:            tfDir,
		tfStatePath:      filepath.Join(tfDir, "terraform.tfstate"),
		pulumiFixtureDir: filepath.Join(repoRoot, "test", "e2e", "testdata", "pulumi"),
		env:              env,
	}

	t.Run("PreviewGoesFromCreateToSame", func(t *testing.T) {
		testPreviewGoesFromCreateToSame(t, ctx, fx)
	})
	t.Run("RevertRestoresStackExactly", func(t *testing.T) {
		testRevertRestoresStackExactly(t, ctx, fx)
	})
	t.Run("Idempotence", func(t *testing.T) {
		testIdempotence(t, ctx, fx)
	})
	t.Run("FileMode", func(t *testing.T) {
		testFileMode(t, ctx, fx)
	})
	t.Run("PatchOnlyStackModeWithOutstandingDiffs", func(t *testing.T) {
		testPatchOnlyStackMode(t, ctx, fx)
	})
}

// stackSeq gives every scenario's stack a unique name even if two scenarios
// happen to provision within the same nanosecond.
var stackSeq int64

// provisioned is what provisionStack hands back: a fresh Pulumi stack, in
// its own working copy of the pulumi fixture, with the importable resources
// already imported and the non-importable sidecar ready to inject.
type provisioned struct {
	pulumiDir        string
	stackName        string
	digestPath       string
	filledImportPath string
	sidecarPath      string
	sidecar          *pkg.NonImportableFile
	backupDir        string
}

// provisionStack does everything every scenario needs before it can
// exercise "patch-state tf --non-importable" or "patch-state tf": a fresh
// stack, digest tf, the import skeleton, resolve tf, and "pulumi import" of
// the importable resources. It never touches AWS beyond what "pulumi
// import" itself does (reading current attributes for the resources it
// imports) — the fixture's tofu apply already happened once, in the caller.
func provisionStack(t *testing.T, ctx context.Context, fx *fixture) *provisioned {
	t.Helper()

	pulumiDir := filepath.Join(t.TempDir(), "pulumi")
	if err := copyDir(fx.pulumiFixtureDir, pulumiDir); err != nil {
		t.Fatalf("copying pulumi fixture: %v", err)
	}

	n := atomic.AddInt64(&stackSeq, 1)
	stackName := fmt.Sprintf("e2e-%d-%d", time.Now().UnixNano(), n)

	runPulumi(t, ctx, pulumiDir, fx.env, "stack", "init", stackName)
	runPulumi(t, ctx, pulumiDir, fx.env, "config", "set", "aws:region", "us-west-2")

	digestPath := filepath.Join(t.TempDir(), "tf-digest.json")
	runTool(t, ctx, fx.binPath, fx.repoRoot, fx.env, "digest", "tf",
		"--from", fx.tfDir,
		"--state-file", fx.tfStatePath,
		"--out", digestPath,
		"--pulumi-stack", stackName,
		"--pulumi-project", pulumiProject,
		"--project-dir", pulumiDir,
		"--skip-secrets", // fixture has no sensitive attributes to carry through config
	)

	// "resolve tf" only fills IDs into entries that already exist; it does
	// not invent them, so this is the step that actually determines the
	// resource names the digest must match. See the comment at the top of
	// testdata/pulumi/Pulumi.yaml for how those names were derived and
	// verified before this test could run against AWS at all.
	importSkeletonPath := filepath.Join(t.TempDir(), "import-skeleton.json")
	runPulumi(t, ctx, pulumiDir, fx.env, "preview",
		"--stack", stackName, "--import-file", importSkeletonPath)

	filledImportPath := filepath.Join(t.TempDir(), "filled-import.json")
	runTool(t, ctx, fx.binPath, fx.repoRoot, fx.env, "resolve", "tf",
		"--digest", digestPath,
		"--import-file", importSkeletonPath,
		"--out", filledImportPath,
	)

	sidecarPath := nonImportableSidecarPath(filledImportPath)
	sidecar, err := pkg.LoadNonImportableFile(sidecarPath)
	if err != nil {
		t.Fatalf("loading non-importable sidecar %s: %v", sidecarPath, err)
	}
	assertSidecarMatches(t, sidecar, wantNonImportable)

	runPulumi(t, ctx, pulumiDir, fx.env, "import",
		"--file", filledImportPath,
		"--stack", stackName,
		"--yes",
		"--generate-code=false",
	)

	return &provisioned{
		pulumiDir:        pulumiDir,
		stackName:        stackName,
		digestPath:       digestPath,
		filledImportPath: filledImportPath,
		sidecarPath:      sidecarPath,
		sidecar:          sidecar,
		backupDir:        t.TempDir(),
	}
}

// assertSidecarMatches checks that a loaded sidecar names exactly the
// expected non-importable resources, with the expected types.
func assertSidecarMatches(t *testing.T, sidecar *pkg.NonImportableFile, want map[string]string) {
	t.Helper()
	if len(sidecar.Resources) != len(want) {
		t.Fatalf("sidecar has %d resource(s), want %d: %+v",
			len(sidecar.Resources), len(want), sidecar.Resources)
	}
	for _, r := range sidecar.Resources {
		wantType, ok := want[r.Name]
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
}

// patchStateArgs builds the flags common to every "patch-state tf"
// invocation in this test (digest, fields, config-dir), so each scenario
// only has to add its mode-specific flags.
func patchStateArgs(fx *fixture, p *provisioned) []string {
	return []string{"patch-state", "tf",
		"--digest", p.digestPath,
		"--fields", filepath.Join(fx.repoRoot, "data", "aws-import-diff-fields.json"),
		"--config-dir", fx.tfDir,
	}
}

// ---------------------------------------------------------------------------
// Scenario 1: the original assertion — non-importable resources preview as
// "create" before injection and "same" after.
// ---------------------------------------------------------------------------

// testPreviewGoesFromCreateToSame proves the two resource types that declare
// no Terraform importer (aws_vpn_gateway_route_propagation,
// aws_vpn_connection_route) go from previewing as "create" to previewing as
// "same".
//
// "pulumi refresh" cannot make this claim: it reports these resource types
// unchanged even when the injected values are wrong, because their Read
// either sets no attributes or re-derives them from the ID alone. Only
// "pulumi preview" — compared before and after injection — validates the
// values that were written into state. See docs/non-importable-resources.md.
func testPreviewGoesFromCreateToSame(t *testing.T, ctx context.Context, fx *fixture) {
	p := provisionStack(t, ctx, fx)

	// --- Assert the "before" direction: without this, the "after"
	// assertion could pass vacuously if, say, a name mismatch meant these
	// resources were never previewed at all.
	before := runPreviewJSON(t, ctx, p.pulumiDir, fx.env, p.stackName)
	beforeOps := before.OpsByURN()
	for _, r := range p.sidecar.Resources {
		urn := expectedURN(pulumiProject, p.stackName, r.Type, r.Name)
		op, ok := beforeOps[urn]
		if !ok {
			t.Fatalf("before injection: %s has no step in the preview at all "+
				"(want \"create\") — the program may not declare a matching resource", urn)
		}
		if op != "create" {
			t.Fatalf("before injection: %s previews as %q, want \"create\"", urn, op)
		}
	}
	t.Logf("confirmed %d non-importable resource(s) preview as \"create\" before injection", len(p.sidecar.Resources))

	args := append(patchStateArgs(fx, p),
		"--project-dir", p.pulumiDir,
		"--stack", p.stackName,
		"--non-importable", p.sidecarPath,
		"--backup-dir", p.backupDir,
	)
	runTool(t, ctx, fx.binPath, fx.repoRoot, fx.env, args...)

	after := runPreviewJSON(t, ctx, p.pulumiDir, fx.env, p.stackName)
	afterOps := after.OpsByURN()
	for _, r := range p.sidecar.Resources {
		urn := expectedURN(pulumiProject, p.stackName, r.Type, r.Name)
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
		t.Logf("confirmed %d non-importable resource(s) preview as \"same\" after injection", len(p.sidecar.Resources))
	}
}

// ---------------------------------------------------------------------------
// Scenario 2: a corrupted sidecar makes injection fail verification, and the
// revert path it takes must restore the stack exactly.
// ---------------------------------------------------------------------------

// testRevertRestoresStackExactly deliberately corrupts one injected value —
// gives one route propagation a different, real route table's ID, which is
// wrong for that propagation but not obviously malformed — so that the
// verifying preview inside "patch-state tf --non-importable" reports that
// resource as something other than "same". That must make the command fail,
// name the resource, and — this is the part with no prior coverage — leave
// the stack's exported deployment byte-for-byte (after JSON canonicalization)
// identical to what it was before the command ran at all.
//
// This revert path has fired three times during development on real
// failures, but nothing had ever checked that what it restores is actually
// what was there.
func testRevertRestoresStackExactly(t *testing.T, ctx context.Context, fx *fixture) {
	p := provisionStack(t, ctx, fx)

	corruptedPath, corruptedName := corruptSidecarRouteTableID(t, p.sidecarPath)

	before := canonicalStackExport(t, ctx, p.pulumiDir, fx.env, p.stackName)

	args := append(patchStateArgs(fx, p),
		"--project-dir", p.pulumiDir,
		"--stack", p.stackName,
		"--non-importable", corruptedPath,
		"--backup-dir", p.backupDir,
	)
	out, err := runToolAllowFail(t, ctx, fx.binPath, fx.repoRoot, fx.env, args...)
	if err == nil {
		t.Fatalf("patch-state tf --non-importable unexpectedly succeeded with a corrupted "+
			"route table ID for %s; output:\n%s", corruptedName, out)
	}
	if !strings.Contains(out, corruptedName) {
		t.Errorf("failure output does not name the corrupted resource %q; output:\n%s", corruptedName, out)
	}
	if !strings.Contains(out, "expected \"same\"") {
		t.Errorf("failure output does not report the verification check that failed "+
			"(want a substring of pkg.CheckInjectedOps's \"expected \\\"same\\\"\" message); output:\n%s", out)
	}
	if !strings.Contains(out, "injection reverted") {
		t.Errorf("failure output does not confirm the revert ran (want \"injection reverted\"); output:\n%s", out)
	}

	after := canonicalStackExport(t, ctx, p.pulumiDir, fx.env, p.stackName)
	if !bytes.Equal(before, after) {
		t.Errorf("stack export after the reverted run differs from before it — the revert did not "+
			"restore the stack exactly.\nbefore:\n%s\nafter:\n%s", before, after)
	} else {
		t.Logf("confirmed the stack's exported deployment is unchanged after the reverted run "+
			"(%d bytes, canonicalized)", len(before))
	}
}

// ---------------------------------------------------------------------------
// Scenario 3: running a successful injection twice.
// ---------------------------------------------------------------------------

// testIdempotence runs a successful injection, then runs the identical
// command again against the same stack, and reports what actually happens —
// per the task, whatever that turns out to be gets asserted explicitly
// rather than assumed.
//
// Reading pkg.InjectNonImportable: it matches each sidecar entry against a
// "create" step in the preview (preview.CreatesByTypeName) and errors if no
// such step exists ("no create step in the preview matches ..."). After a
// successful first injection the resources are no longer creates — they
// preview as "same" — so a second, identical run is expected, by that code
// path, to fail rather than silently no-op. That is the behaviour asserted
// below; if it turns out not to hold live, that is itself the finding this
// scenario exists to surface.
func testIdempotence(t *testing.T, ctx context.Context, fx *fixture) {
	p := provisionStack(t, ctx, fx)

	args := append(patchStateArgs(fx, p),
		"--project-dir", p.pulumiDir,
		"--stack", p.stackName,
		"--non-importable", p.sidecarPath,
		"--backup-dir", p.backupDir,
	)
	runTool(t, ctx, fx.binPath, fx.repoRoot, fx.env, args...)

	afterFirst := runPreviewJSON(t, ctx, p.pulumiDir, fx.env, p.stackName)
	afterFirstOps := afterFirst.OpsByURN()
	for _, r := range p.sidecar.Resources {
		urn := expectedURN(pulumiProject, p.stackName, r.Type, r.Name)
		if op := afterFirstOps[urn]; op != "same" {
			t.Fatalf("after the first injection: %s previews as %q, want \"same\"", urn, op)
		}
	}

	// Run the identical command again, with a fresh backup dir so the two
	// runs' backup files (timestamp-named) cannot collide.
	secondBackupDir := t.TempDir()
	args2 := append(patchStateArgs(fx, p),
		"--project-dir", p.pulumiDir,
		"--stack", p.stackName,
		"--non-importable", p.sidecarPath,
		"--backup-dir", secondBackupDir,
	)
	out2, err2 := runToolAllowFail(t, ctx, fx.binPath, fx.repoRoot, fx.env, args2...)

	if err2 == nil {
		// If a future change makes the second run succeed, it must still be
		// non-destructive: no duplicate resources, no new injections.
		t.Logf("FINDING: a second, identical \"patch-state tf --non-importable\" run against an "+
			"already-injected stack succeeded (this contradicts what reading pkg.InjectNonImportable "+
			"predicted — worth a closer look). Output:\n%s", out2)
		verify := runPreviewJSON(t, ctx, p.pulumiDir, fx.env, p.stackName)
		verifyOps := verify.OpsByURN()
		for _, r := range p.sidecar.Resources {
			urn := expectedURN(pulumiProject, p.stackName, r.Type, r.Name)
			if op := verifyOps[urn]; op != "same" {
				t.Errorf("after the second injection: %s previews as %q, want \"same\" — "+
					"the second run appears to have duplicated or corrupted the resource", urn, op)
			}
		}
		if strings.Contains(out2, "Injected:") && !strings.Contains(out2, "Injected:           0 resources") {
			t.Errorf("second run reported injecting resources that were already in state; output:\n%s", out2)
		}
		return
	}

	// The expected-from-source-reading outcome: the second run fails
	// because none of the sidecar's resources are creates in the preview
	// anymore.
	t.Logf("second, identical injection run failed as predicted from source reading "+
		"(no create step left to match): %v", err2)
	if !strings.Contains(out2, "no create step in the preview matches") {
		t.Errorf("second run failed, but not with the expected \"no create step in the preview "+
			"matches\" message — got a different failure mode, which is itself worth reporting. output:\n%s",
			out2)
	}

	// Whichever way it failed, the stack must not have been left corrupted:
	// the resources injected by the first run must still preview as "same".
	verify := runPreviewJSON(t, ctx, p.pulumiDir, fx.env, p.stackName)
	verifyOps := verify.OpsByURN()
	for _, r := range p.sidecar.Resources {
		urn := expectedURN(pulumiProject, p.stackName, r.Type, r.Name)
		if op := verifyOps[urn]; op != "same" {
			t.Errorf("after the failed second run: %s previews as %q, want \"same\" — "+
				"the failed second run left the stack worse than the first run left it", urn, op)
		}
	}
}

// ---------------------------------------------------------------------------
// Scenario 4: the offline file-mode path documented in
// docs/non-importable-resources.md.
// ---------------------------------------------------------------------------

// testFileMode exercises "pulumi stack export" / "pulumi preview --json" to
// files, "patch-state tf --non-importable --state ... --out ...
// --preview-json ...", and "pulumi stack import" of the result — the
// offline path documented in docs/non-importable-resources.md, which had no
// end-to-end coverage before this.
func testFileMode(t *testing.T, ctx context.Context, fx *fixture) {
	p := provisionStack(t, ctx, fx)

	previewJSONPath := filepath.Join(t.TempDir(), "preview.json")
	runPulumiToFile(t, ctx, p.pulumiDir, fx.env, previewJSONPath,
		"preview", "--json", "--stack", p.stackName)

	statePath := filepath.Join(t.TempDir(), "state.json")
	runPulumiToFile(t, ctx, p.pulumiDir, fx.env, statePath,
		"stack", "export", "--stack", p.stackName)

	outPath := filepath.Join(t.TempDir(), "injected.json")
	args := append(patchStateArgs(fx, p),
		"--state", statePath,
		"--out", outPath,
		"--non-importable", p.sidecarPath,
		"--preview-json", previewJSONPath,
	)
	runTool(t, ctx, fx.binPath, fx.repoRoot, fx.env, args...)

	runPulumi(t, ctx, p.pulumiDir, fx.env, "stack", "import",
		"--stack", p.stackName, "--file", outPath)

	after := runPreviewJSON(t, ctx, p.pulumiDir, fx.env, p.stackName)
	afterOps := after.OpsByURN()
	for _, r := range p.sidecar.Resources {
		urn := expectedURN(pulumiProject, p.stackName, r.Type, r.Name)
		op, ok := afterOps[urn]
		if !ok {
			t.Errorf("after file-mode injection: %s has no step in the preview at all (want \"same\")", urn)
			continue
		}
		if op != "same" {
			t.Errorf("after file-mode injection: %s previews as %q, want \"same\"", urn, op)
		}
	}
	if !t.Failed() {
		t.Logf("confirmed %d non-importable resource(s) preview as \"same\" after file-mode injection",
			len(p.sidecar.Resources))
	}
}

// ---------------------------------------------------------------------------
// Scenario 5: patch-only stack mode (no --non-importable) against a stack
// that still has outstanding diffs.
// ---------------------------------------------------------------------------

// testPatchOnlyStackMode runs "patch-state tf" in stack mode with no
// --non-importable, against a stack that still has outstanding diffs — it
// always will here, since the non-importable resources were never injected
// and so still preview as "create". This is where a Critical review finding
// lived: with no injected URNs, verification was vacuous and printed
// "Verified: all 0 injected resource(s) preview as unchanged" regardless of
// whether the patch actually left the stack sound. It is now guarded by a
// baseline/post comparison (pkg.CheckInjectionVerification with an empty
// injected set), but that guard had never run live before this test.
func testPatchOnlyStackMode(t *testing.T, ctx context.Context, fx *fixture) {
	p := provisionStack(t, ctx, fx)

	before := runPreviewJSON(t, ctx, p.pulumiDir, fx.env, p.stackName)
	beforeOps := before.OpsByURN()
	for _, r := range p.sidecar.Resources {
		urn := expectedURN(pulumiProject, p.stackName, r.Type, r.Name)
		if op, ok := beforeOps[urn]; !ok || op != "create" {
			t.Fatalf("before the patch-only run: %s previews as %q (ok=%v), want \"create\" — "+
				"this scenario needs an outstanding diff to prove the verification guard isn't vacuous",
				urn, op, ok)
		}
	}
	baselineOutstanding := pkg.CheckPreviewClean(before)

	args := append(patchStateArgs(fx, p),
		"--project-dir", p.pulumiDir,
		"--stack", p.stackName,
		"--backup-dir", p.backupDir,
	)
	out, err := runToolAllowFail(t, ctx, fx.binPath, fx.repoRoot, fx.env, args...)
	if err != nil {
		t.Fatalf("patch-state tf (no --non-importable) failed against a stack with outstanding, "+
			"unrelated diffs — it should have verified cleanly by comparison against its own "+
			"baseline: %v\noutput:\n%s", err, out)
	}
	if strings.Contains(out, "Restoring") || strings.Contains(out, "injection reverted") {
		t.Errorf("patch-state tf reported success but its output shows it reverted the patch "+
			"anyway — that is the \"falsely reports success\" failure mode this scenario guards "+
			"against. output:\n%s", out)
	}
	if !strings.Contains(out, "Verified: the patch introduced no new operations") {
		t.Errorf("success output does not contain the expected patch-only verification message; "+
			"output:\n%s", out)
	}

	after := runPreviewJSON(t, ctx, p.pulumiDir, fx.env, p.stackName)
	afterOps := after.OpsByURN()
	for _, r := range p.sidecar.Resources {
		urn := expectedURN(pulumiProject, p.stackName, r.Type, r.Name)
		if op, ok := afterOps[urn]; !ok || op != "create" {
			t.Errorf("after the patch-only run: %s previews as %q (ok=%v), want it to remain "+
				"\"create\" — a patch-only run with no --non-importable must not have silently "+
				"resolved or reverted the outstanding diff on a resource it never touched",
				urn, op, ok)
		}
	}
	afterOutstanding := pkg.CheckPreviewClean(after)
	if len(afterOutstanding) > len(baselineOutstanding) {
		t.Errorf("patch-only run increased the number of outstanding (non-\"same\") preview steps: "+
			"%d before, %d after — that is a regression the verification guard should have caught",
			len(baselineOutstanding), len(afterOutstanding))
	} else {
		t.Logf("confirmed the patch-only run did not increase outstanding diffs (%d before, %d after) "+
			"and did not touch the %d resources it left un-injected",
			len(baselineOutstanding), len(afterOutstanding), len(p.sidecar.Resources))
	}
}

// What gates this test: the only credentials it needs are AWS ones. It runs
// against an isolated local file backend (PULUMI_BACKEND_URL plus its own
// PULUMI_HOME), so it needs no Pulumi Cloud login and no
// PULUMI_ACCESS_TOKEN. An earlier version gated on PULUMI_ACCESS_TOKEN, which
// silently skipped the whole test whenever the caller brokered AWS credentials
// without also exporting a Pulumi token — a skip that looks like a pass.
// logCallerIdentity below is the actual gate: if AWS is unreachable, it skips.

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
		if errors.Is(err, exec.ErrNotFound) {
			t.Skipf("the aws CLI is not installed (not found in $PATH): %v\n"+
				"install the AWS CLI, then run via the ESC wrapper documented at the top of this file "+
				"(also: make test-e2e)", err)
		}
		t.Skipf("aws sts get-caller-identity failed (no AWS credentials?): %v\n"+
			"note: sanitizedEnv (helpers.go) strips AWS_PROFILE from this call because it shadows "+
			"brokered credentials — if your only working credential path is an SSO AWS_PROFILE "+
			"(no static keys, no ESC-injected env vars), this call fails even though plain \"aws\" "+
			"commands succeed in your shell; that is expected, not a bug\n"+
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

// runTool runs a subcommand of the binary under test, failing the test on
// error.
func runTool(t *testing.T, ctx context.Context, binPath, dir string, env []string, args ...string) {
	t.Helper()
	out, err := runToolAllowFail(t, ctx, binPath, dir, env, args...)
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", binPath, strings.Join(args, " "), err, out)
	}
}

// runToolAllowFail runs a subcommand of the binary under test and returns
// its combined output and error without failing the test — for scenarios
// that expect the command to fail and need to assert on how.
func runToolAllowFail(t *testing.T, ctx context.Context, binPath, dir string, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	t.Logf("$ %s %s\n%s", binPath, strings.Join(args, " "), out)
	return string(out), err
}

// runPulumi runs the pulumi CLI in dir with the given environment, failing
// the test on error.
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

// runPulumiToFile runs the pulumi CLI in dir and writes its stdout to path,
// failing the test on error. Used for the file-mode scenario, which needs
// "pulumi preview --json" and "pulumi stack export" captured to disk exactly
// as an operator following docs/non-importable-resources.md would produce
// them (">" redirection), rather than parsed in-process.
func runPulumiToFile(t *testing.T, ctx context.Context, dir string, env []string, outPath string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, "pulumi", args...)
	cmd.Dir = dir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	t.Logf("$ pulumi %s > %s\n%s", strings.Join(args, " "), outPath, stderr.String())
	if err != nil {
		t.Fatalf("pulumi %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	if err := os.WriteFile(outPath, stdout.Bytes(), 0o600); err != nil {
		t.Fatalf("writing %s: %v", outPath, err)
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

// canonicalStackExport runs "pulumi stack export" and returns its content
// re-marshaled with sorted keys, so two exports of an unchanged deployment
// compare equal byte-for-byte regardless of incidental key ordering in the
// CLI's own output. Numbers are decoded with UseNumber so large IDs are not
// perturbed by float64 round-tripping.
func canonicalStackExport(t *testing.T, ctx context.Context, dir string, env []string, stackName string) []byte {
	t.Helper()
	cmd := exec.CommandContext(ctx, "pulumi", "stack", "export", "--stack", stackName)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		stderr := ""
		if errors.As(err, &exitErr) {
			stderr = string(exitErr.Stderr)
		}
		t.Fatalf("pulumi stack export: %v\n%s", err, stderr)
	}
	canon, err := canonicalizeJSON(out)
	if err != nil {
		t.Fatalf("canonicalizing stack export: %v", err)
	}
	return canon
}

// canonicalizeJSON decodes data with UseNumber (so large integers survive
// exactly) and re-encodes it. encoding/json always emits object keys in
// sorted order on marshal, so two structurally identical documents produce
// identical bytes here even if their original key order differed.
func canonicalizeJSON(data []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v interface{}
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("decoding: %w", err)
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("re-encoding: %w", err)
	}
	return out, nil
}

// corruptSidecarRouteTableID copies the sidecar at path to a new file with
// one route propagation's route table ID replaced by a different
// propagation's — a value that is real and correctly formatted (so it is
// not rejected as malformed) but wrong for the entry it ends up on. It
// returns the new file's path and the Pulumi resource name (e.g. "prop[0]")
// whose value was corrupted, for asserting the failure names it.
//
// Both places a route table ID can live are corrupted: Attributes["route_
// table_id"] (the raw Terraform attribute, always present) and
// PulumiOutputs["routeTableId"] (present when "digest tf" computed outputs
// with a live provider open, which takes priority over Attributes when
// building the injected resource — see buildInjectedResource in
// pkg/state_injector.go). Corrupting only one of the two would silently do
// nothing if the other is the one actually used.
func corruptSidecarRouteTableID(t *testing.T, sidecarPath string) (corruptedPath, corruptedName string) {
	t.Helper()

	sidecar, err := pkg.LoadNonImportableFile(sidecarPath)
	if err != nil {
		t.Fatalf("loading sidecar %s to corrupt: %v", sidecarPath, err)
	}

	var propagations []*pkg.NonImportableResource
	for i := range sidecar.Resources {
		if strings.HasPrefix(sidecar.Resources[i].Name, "prop[") {
			propagations = append(propagations, &sidecar.Resources[i])
		}
	}
	if len(propagations) < 2 {
		t.Fatalf("expected at least 2 \"prop[*]\" entries in the sidecar to swap a route table ID "+
			"between, found %d: %+v", len(propagations), sidecar.Resources)
	}
	target, donor := propagations[0], propagations[1]
	corruptedName = target.Name

	corrupted := false
	if v, ok := donor.Attributes["route_table_id"]; ok {
		target.Attributes["route_table_id"] = v
		corrupted = true
	}
	if donorOutputs := donor.PulumiOutputs; donorOutputs != nil {
		if v, ok := donorOutputs["routeTableId"]; ok {
			if target.PulumiOutputs == nil {
				target.PulumiOutputs = map[string]interface{}{}
			}
			target.PulumiOutputs["routeTableId"] = v
			corrupted = true
		}
	}
	if !corrupted {
		t.Fatalf("neither Attributes[\"route_table_id\"] nor PulumiOutputs[\"routeTableId\"] was "+
			"present on the donor %q — nothing was corrupted, so this scenario would prove nothing. "+
			"donor: %+v", donor.Name, donor)
	}

	data, err := json.MarshalIndent(sidecar, "", "    ")
	if err != nil {
		t.Fatalf("marshaling corrupted sidecar: %v", err)
	}
	corruptedPath = filepath.Join(filepath.Dir(sidecarPath), "corrupted.non-importable.json")
	if err := os.WriteFile(corruptedPath, append(data, '\n'), 0o644); err != nil {
		t.Fatalf("writing corrupted sidecar %s: %v", corruptedPath, err)
	}
	return corruptedPath, corruptedName
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
