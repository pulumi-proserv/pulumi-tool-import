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
	"regexp"
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
// testdata/tf/main.tf: the three route-table propagations and the
// connection route that aws_vpn_gateway_route_propagation and
// aws_vpn_connection_route declare no importer for, plus the IoT
// certificate (Sensitive attributes, exercises the secrets path end to end)
// and the VPC Lattice target group attachment (a nested list-of-objects
// property, exercises MakeTerraformOutputs/RawStateComputeDelta on a
// non-flat shape for the first time).
var wantNonImportable = map[string]string{
	"prop[0]": "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation",
	"prop[1]": "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation",
	"prop[2]": "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation",
	"route":   "aws:ec2/vpnConnectionRoute:VpnConnectionRoute",
	"cert":    "aws:iot/certificate:Certificate",
	"attach":  "aws:vpclattice/targetGroupAttachment:TargetGroupAttachment",

	// Under a second, aliased provider in us-east-1 — the provider-reference
	// resolution the sidecar cannot carry and injection takes from the
	// preview's create step.
	"east": "aws:iot/certificate:Certificate",

	// Non-importable AND dependent on "cert", which is also injected: the
	// dependency edge orderInjected's topological sort exists for.
	"policy_attach": "aws:iot/policyAttachment:PolicyAttachment",

	// for_each rather than count: quoted keys inside the brackets, a
	// different address shape from "prop[0]".
	`each["alpha"]`: "aws:iot/certificate:Certificate",
	`each["beta"]`:  "aws:iot/certificate:Certificate",

	// Inside a Terraform module, mapped to a Pulumi ComponentResource — the
	// only resource here whose parent is not the stack root. Its sidecar
	// entry records the bare type; its URN carries the qualified
	// "component$child" form. See qualifiedTypes.
	"inmodule": "aws:iot/certificate:Certificate",
}

// eastCertName is the one non-importable resource under a non-default
// provider. Named here because two scenarios need to single it out.
const eastCertName = "east"

// parentedCertName is the one non-importable resource whose parent is a
// component rather than the stack root.
const parentedCertName = "inmodule"

// componentType is the ComponentResource type token testdata/pulumi-ts
// registers for the component that parents parentedCertName.
const componentType = "toolimport:index:Certs"

// qualifiedTypes maps a sidecar resource's name to the QUALIFIED type its URN
// carries, for resources whose parent is not the stack root.
//
// A URN's type segment is "parentType$childType" for a parented resource, but
// the sidecar records only the child's own type — it has no reason to know
// about the parent. So expectedURN(…, r.Type, r.Name) builds the right URN for
// every resource here except the component's child, and sidecarURN exists to
// bridge that.
var qualifiedTypes = map[string]string{
	parentedCertName: componentType + "$aws:iot/certificate:Certificate",
}

// sidecarURN is the URN a sidecar resource actually has in the deployment,
// accounting for a component parent. Use this rather than expectedURN when
// iterating over sidecar entries.
func sidecarURN(stackName string, r pkg.NonImportableResource) string {
	typ := r.Type
	if qualified, ok := qualifiedTypes[r.Name]; ok {
		typ = qualified
	}
	return expectedURN(pulumiProject, stackName, typ, r.Name)
}

// secretSigKey is the Pulumi property-value signature key that marks a
// serialized value as a secret (resource.SigKey in the Pulumi SDK, mirrored
// here rather than imported so this test can check for it in raw exported
// JSON without pulling in the SDK's property-value types). Injection writes
// it into a resolved secret's envelope (see pkg/state_injector.go's
// resolveOutputSecrets/resolveSecretInputs); its presence in a raw stack
// export, on a map value rather than a bare string, is what "the secret is
// enveloped, not bare plaintext" means concretely.
const secretSigKey = "4dabf18193072939515e22adb298388d"

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

	// nodeModulesDir is a single "npm ci" tree shared by every scenario's
	// working copy through a symlink. The nodejs runtime needs node_modules
	// present, and installing it once per scenario would repeat the same
	// download eleven times per run for no benefit.
	nodeModulesDir string
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

	// Deliberately NOT t.TempDir(): Go removes a t.TempDir when the test
	// ends, including on failure, which takes the Terraform state with it.
	//
	// That is what happened on 2026-08-17. DNS failed mid-apply, "tofu
	// destroy" could not reach STS, and the cleanup printed "clean up by hand
	// from <dir>" naming a directory Go had already deleted — so the state
	// needed to destroy a running VPN connection was gone, and every orphan
	// had to be found and deleted through the AWS API by hand instead.
	//
	// So this directory is created outside the testing framework's lifecycle
	// and removed explicitly only when teardown actually succeeded. A leftover
	// directory is a much cheaper failure than unreachable state.
	tfRoot, err := os.MkdirTemp("", "pulumi-tool-import-e2e-tf-")
	if err != nil {
		t.Fatalf("creating tf working directory: %v", err)
	}
	tfDir := filepath.Join(tfRoot, "tf")
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

	// The Pulumi program is TypeScript rather than YAML. Both expressed the
	// same resources under the same logical names — the port was verified by
	// generating an import file from each and comparing them entry for entry
	// — but YAML cannot declare a ComponentResource, which blocked the
	// component-parent coverage gap. See the header comment in
	// testdata/pulumi-ts/index.ts.
	pulumiFixtureDir := filepath.Join(repoRoot, "test", "e2e", "testdata", "pulumi-ts")
	nodeModulesDir := installNodeModules(t, ctx, pulumiFixtureDir, env)

	// --- Create the real fixture, once, shared by every scenario below. ---
	runTofu(t, ctx, tfDir, env, "init", "-input=false")
	// Registered before "apply" runs — not after it succeeds — so it also
	// covers a failed or partial apply, not just a successful one. This is
	// the fix for a real incident: apply created a VPC, three route tables,
	// a VPN gateway, a customer gateway and a VPN connection (which bills),
	// then failed on its last resource; because the cleanup was registered
	// only after "apply" returned, t.Fatalf inside runTofu exited the test
	// before the cleanup was ever registered, and everything was left
	// running until it was torn down by hand.
	//
	// "tofu destroy" against a partially-applied or even empty state is
	// safe — it is a no-op when there is nothing to destroy — so
	// registering this early costs nothing. t.Cleanup runs on every path
	// out of this test (success, t.Fatalf, panic), which is the actual
	// guarantee here; it is not specific to subtest failures.
	t.Cleanup(func() {
		// Capture the resource IDs BEFORE destroy, while state still lists
		// them. Destroy removes each resource from state as it destroys it,
		// and the success path below then deletes tfRoot outright, so
		// reading state after destroy — which verifyFixtureResourcesGone
		// used to do for itself — found nothing and silently skipped every
		// AWS-side check, leaving only the tag scan. That disabled the
		// check on exactly the case it exists for: a destroy that exits
		// zero while leaving resources behind.
		//
		// Reading here rather than after "apply" also covers a failed or
		// partial apply, which fatals before any post-apply capture would
		// run but still leaves what it did create recorded in state.
		ids, idErr := loadFixtureResourceIDs(tfDir)
		if idErr != nil {
			t.Errorf("could not read fixture resource IDs from %s's Terraform state to double "+
				"check them against AWS directly: %v — this does NOT mean nothing is left behind, "+
				"only that this check could not run; check the account by hand", tfDir, idErr)
		}

		out, err := runTofuCombined(ctx, tfDir, env, "destroy", "-auto-approve", "-input=false")
		if err == nil {
			// Only now is the state safe to discard.
			if rmErr := os.RemoveAll(tfRoot); rmErr != nil {
				t.Logf("removing %s: %v", tfRoot, rmErr)
			}
		} else {
			t.Errorf("RECOVERY: the Terraform state is preserved at %s — re-run\n"+
				"    esc run team-ce/aws/pulumi-ce -- env -u AWS_PROFILE tofu -chdir=%s destroy -auto-approve\n"+
				"once the cause below is resolved. Delete that directory afterwards.",
				tfDir, tfDir)
			t.Errorf("tofu destroy failed — clean up by hand from %s (terraform state left in place):\n%v\n%s",
				tfDir, err, out)
		}
		verifyFixtureResourcesGone(t, ctx, ids)
	})
	runTofu(t, ctx, tfDir, env, "apply", "-auto-approve", "-input=false")

	fx := &fixture{
		repoRoot:         repoRoot,
		binPath:          binPath,
		tfDir:            tfDir,
		tfStatePath:      filepath.Join(tfDir, "terraform.tfstate"),
		pulumiFixtureDir: pulumiFixtureDir,
		nodeModulesDir:   nodeModulesDir,
		env:              env,
	}

	t.Run("PreviewGoesFromCreateToSame", func(t *testing.T) {
		testPreviewGoesFromCreateToSame(t, ctx, fx)
	})
	t.Run("ClassificationIsNotOverBroad", func(t *testing.T) {
		testClassificationIsNotOverBroad(t, ctx, fx)
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
	t.Run("ProviderAndDependencyEdges", func(t *testing.T) {
		testInjectedStateCarriesProviderAndDependencyEdges(t, ctx, fx)
	})
	t.Run("KMSSecretsProvider", func(t *testing.T) {
		testKMSSecretsProvider(t, ctx, fx)
	})
	t.Run("ComponentParent", func(t *testing.T) {
		testComponentParent(t, ctx, fx)
	})
	t.Run("InjectionSurvivesPreExistingDrift", func(t *testing.T) {
		testInjectionSurvivesPreExistingDrift(t, ctx, fx)
	})
	t.Run("SecretInjectedEndToEnd", func(t *testing.T) {
		testSecretInjectedEndToEnd(t, ctx, fx)
	})
	t.Run("NestedBlockInjection", func(t *testing.T) {
		testNestedBlockInjection(t, ctx, fx)
	})
	t.Run("CorruptDeltaFailsPreview", func(t *testing.T) {
		testCorruptDeltaFailsPreview(t, ctx, fx)
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
	return provisionStackWith(t, ctx, fx, "")
}

// provisionStackWith is provisionStack with an explicit stack secrets
// provider. An empty secretsProvider keeps the default, which under
// PULUMI_CONFIG_PASSPHRASE is "passphrase" — what every scenario but
// KMSSecretsProvider uses.
//
// Split out rather than adding a parameter to provisionStack so that the one
// scenario needing a different provider cannot change the configuration the
// other eight run under.
func provisionStackWith(t *testing.T, ctx context.Context, fx *fixture, secretsProvider string) *provisioned {
	t.Helper()

	pulumiDir := filepath.Join(t.TempDir(), "pulumi")
	if err := copyDir(fx.pulumiFixtureDir, pulumiDir); err != nil {
		t.Fatalf("copying pulumi fixture: %v", err)
	}
	// Symlinked rather than copied: node_modules is thousands of files, and
	// copying it once per scenario would cost more than the scenarios do.
	// Nothing writes to it, so sharing one tree is safe.
	if err := os.Symlink(fx.nodeModulesDir, filepath.Join(pulumiDir, "node_modules")); err != nil {
		t.Fatalf("linking node_modules into %s: %v", pulumiDir, err)
	}

	n := atomic.AddInt64(&stackSeq, 1)
	stackName := fmt.Sprintf("e2e-%d-%d", time.Now().UnixNano(), n)

	initArgs := []string{"stack", "init", stackName}
	if secretsProvider != "" {
		initArgs = append(initArgs, "--secrets-provider", secretsProvider)
	}
	runPulumi(t, ctx, pulumiDir, fx.env, initArgs...)
	runPulumi(t, ctx, pulumiDir, fx.env, "config", "set", "aws:region", "us-west-2")

	// An EXPLICIT provider has to be created before anything can be imported
	// or injected under it. "pulumi import" creates the default provider it
	// needs, but one declared in the program (eastProvider, for the aliased
	// aws.east resources) only comes into existence when the program runs —
	// so it must be brought up first, targeted so that nothing else is
	// created with it.
	//
	// Without this step every scenario failed run 10 with "state integrity
	// check failed: ... refers to unknown provider": injection had copied the
	// correct provider reference out of the preview's create step, but the
	// provider it named was not in the deployment yet.
	//
	// --target creates only this resource; the provider itself is state and
	// configuration, so nothing is created in AWS here.
	runPulumi(t, ctx, pulumiDir, fx.env, "up",
		"--stack", stackName,
		"--target", expectedURN(pulumiProject, stackName, "pulumi:providers:aws", "eastProvider"),
		"--yes",
		"--skip-preview",
	)

	// --skip-secrets is deliberately not passed: the fixture's IoT
	// certificate has real Sensitive attributes (private_key, certificate_pem,
	// ca_pem, public_key — see testdata/tf/main.tf), and every scenario below
	// injects it as part of the generic sidecar loop, not just the dedicated
	// secrets scenario. Without this, "digest tf" would still redact those
	// attributes to "(sensitive)" in the digest (that happens unconditionally,
	// regardless of --skip-secrets) but never write the real values anywhere
	// resolvable, and every scenario's injection would hard-fail trying to
	// resolve a stack config key that was never set. --project-dir and
	// --pulumi-stack are what give it somewhere to write those values.
	digestPath := filepath.Join(t.TempDir(), "tf-digest.json")
	runTool(t, ctx, fx.binPath, fx.repoRoot, fx.env, "digest", "tf",
		"--from", fx.tfDir,
		"--state-file", fx.tfStatePath,
		"--out", digestPath,
		"--pulumi-stack", stackName,
		"--pulumi-project", pulumiProject,
		"--project-dir", pulumiDir,
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
	// --map is REQUIRED for the module's resource to be matched, not a
	// convenience. FillImportFile (pkg/import_filler.go:198) puts
	// module-scoped TF resources in tfByModule and parented import entries in
	// byParent[componentName], and ONLY the moduleMappings loop joins those
	// two sets — the root-resource fallback matches digest.RootResources
	// against orphaned entries, and a module resource is in neither. Without
	// this the module's certificate would simply never be matched.
	runTool(t, ctx, fx.binPath, fx.repoRoot, fx.env, "resolve", "tf",
		"--digest", digestPath,
		"--import-file", importSkeletonPath,
		"--out", filledImportPath,
		"--map", "module.certs=certs",
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

// installNodeModules runs "npm ci" once, in its own directory, and returns
// the resulting node_modules path for every scenario to symlink to.
//
// "npm ci" rather than "npm install": it installs exactly what
// testdata/pulumi-ts/package-lock.json pins and fails if package.json and the
// lock disagree. That matters more than usual here — the nodejs SDK writes
// the provider version into every generated import-file entry, so a resolved
// version drifting from the pinned one would silently change what "resolve
// tf" sees, and the fixture would stop matching the Terraform side for a
// reason with no visible connection to the failure.
func installNodeModules(t *testing.T, ctx context.Context, fixtureDir string, env []string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"package.json", "package-lock.json"} {
		if err := copyFile(filepath.Join(fixtureDir, name), filepath.Join(dir, name)); err != nil {
			t.Fatalf("copying %s for the shared npm install: %v", name, err)
		}
	}

	cmd := exec.CommandContext(ctx, "npm", "ci", "--no-audit", "--no-fund")
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("npm ci in %s: %v\n%s", dir, err, out)
	}

	modules := filepath.Join(dir, "node_modules")
	if _, err := os.Stat(modules); err != nil {
		t.Fatalf("npm ci reported success but %s is missing: %v", modules, err)
	}
	t.Logf("installed shared node_modules at %s", modules)
	return modules
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
		urn := sidecarURN(p.stackName, r)
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
	out, err := runToolAllowFail(t, ctx, fx.binPath, fx.repoRoot, fx.env, args...)
	if err != nil {
		t.Fatalf("patch-state tf --non-importable failed: %v\n%s", err, out)
	}
	assertEveryInjectedResourceGotADelta(t, out)

	after := runPreviewJSON(t, ctx, p.pulumiDir, fx.env, p.stackName)
	afterOps := after.OpsByURN()
	for _, r := range p.sidecar.Resources {
		urn := sidecarURN(p.stackName, r)
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
// Scenario 1b: every resource the tool diverts into injection must actually
// need it. Injection is the risky path; import is the safe one.
// ---------------------------------------------------------------------------

// testClassificationIsNotOverBroad attempts "pulumi import" on each resource
// the tool classified as non-importable, and fails if any of them imports
// correctly. A resource that import can handle should never be injected.
//
// The classification is a SCHEMA judgement: importsupport asks whether the
// Terraform resource type declares an Importer. The bridge's actual
// requirement is weaker. In pkg/tfbridge/provider.go (v3.121.0:1454):
//
//	isRefresh := len(req.GetProperties().GetFields()) != 0
//	if !isRefresh && res.TF.Importer() != nil {
//	    state, err = res.runTerraformImporter(ctx, id, p)
//	}
//
// When the type declares no Importer that branch is skipped entirely and
// control falls through to p.tf.Refresh with an InstanceState carrying only
// the ID. So the real question is not "is an Importer declared?" but "can the
// provider's Read reconstruct this resource from its ID alone?" — and for
// some types the answer is yes despite no Importer being declared.
//
// aws_vpn_gateway_route_propagation is the concrete suspect: its Read parses
// the ID via vpnGatewayRoutePropagationParseID(d.Id()), queries AWS to
// confirm the propagation exists, and reads no attribute that is not encoded
// in the ID. Nothing about that needs an Importer.
//
// The evidence that these types cannot be imported is a v0.2.0 field run that
// reported "resource '<id>' does not exist", and no test has ever re-checked
// it. The rest of this file asserts that injection works; nothing asserts
// that injection was NECESSARY. That gap is what this closes.
//
// Import "succeeding" is not enough to call a resource importable — the
// import must also leave state that is actually correct, so each successful
// import is followed by a preview and only counts if the resource comes back
// "same". That is the same bar the injection scenarios are held to.
//
// If this test fails it is a FINDING, not a defect: it means the tool is
// pushing resources through injection that "pulumi import" already handles,
// and the fix is to narrow the classification (see #31) rather than to
// weaken this assertion.
func testClassificationIsNotOverBroad(t *testing.T, ctx context.Context, fx *fixture) {
	p := provisionStack(t, ctx, fx)

	type outcome struct {
		name, typ, detail string
	}
	var importable, wrongState []outcome

	for _, r := range p.sidecar.Resources {
		// One entry at a time: a resource that fails to import must not
		// prevent the others from being attempted, and attributing a
		// failure to a specific resource is the whole point.
		oneEntry := pkg.ImportFile{
			Resources: []pkg.ImportEntry{{Type: r.Type, Name: r.Name, ID: r.ID}},
		}
		data, err := json.MarshalIndent(oneEntry, "", "  ")
		if err != nil {
			t.Fatalf("marshalling one-entry import file for %s: %v", r.Name, err)
		}
		entryPath := filepath.Join(t.TempDir(), "one-import.json")
		if err := os.WriteFile(entryPath, data, 0o600); err != nil {
			t.Fatalf("writing one-entry import file for %s: %v", r.Name, err)
		}

		_, err = runPulumiAllowFail(t, ctx, p.pulumiDir, fx.env, "import",
			"--file", entryPath,
			"--stack", p.stackName,
			"--yes",
			"--generate-code=false",
		)
		if err != nil {
			// The expected outcome: import cannot bring this resource in, so
			// diverting it into injection was correct.
			t.Logf("confirmed non-importable: %s (%s) — pulumi import failed", r.Name, r.Type)
			continue
		}

		// Import reported success. That alone does not make the resource
		// importable: the state it produced has to be right.
		urn := sidecarURN(p.stackName, r)
		op, ok := runPreviewJSON(t, ctx, p.pulumiDir, fx.env, p.stackName).OpsByURN()[urn]
		switch {
		case !ok:
			wrongState = append(wrongState, outcome{r.Name, r.Type,
				"import succeeded but the resource has no step in the preview at all"})
		case op == "same":
			importable = append(importable, outcome{r.Name, r.Type,
				"import succeeded and the resource previews as \"same\""})
		default:
			wrongState = append(wrongState, outcome{r.Name, r.Type,
				fmt.Sprintf("import succeeded but the resource previews as %q, not \"same\"", op)})
		}
	}

	// Import that "succeeds" into incorrect state is the failure mode the
	// v0.2.0 field run described. It is not a finding — it confirms the
	// resource needs injection — but it is worth naming, because it is a
	// materially different outcome from a clean refusal and an operator
	// following the docs by hand would be misled by it.
	for _, o := range wrongState {
		t.Logf("needs injection (import is not a clean refusal): %s (%s) — %s", o.name, o.typ, o.detail)
	}

	if len(importable) > 0 {
		for _, o := range importable {
			t.Errorf("FINDING: %s (%s) was classified non-importable, but %s. "+
				"It should be imported, not injected — injection is the riskier path, and the "+
				"classification (Terraform type declares no Importer) is stricter than what the "+
				"bridge actually requires.", o.name, o.typ, o.detail)
		}
		return
	}
	t.Logf("confirmed all %d non-importable resource(s) genuinely need injection", len(p.sidecar.Resources))
}

// ---------------------------------------------------------------------------
// Scenario 1d: the two edges injected state carries that a "same" preview
// does not by itself prove — the provider reference and dependency ordering.
// ---------------------------------------------------------------------------

// testInjectedStateCarriesProviderAndDependencyEdges inspects the deployment
// injection produced, rather than only asking the preview whether it likes it.
//
// Both properties here are ones the sidecar cannot carry and injection must
// take from the preview's create step:
//
//   - The PROVIDER REFERENCE. The uuid in
//     "urn:...::pulumi:providers:aws::eastProvider::<uuid>" exists only in the
//     target stack. The design claims this resolves correctly when several
//     provider instances exist; that claim has never been tested, and a wrong
//     reference silently targets the wrong region or account. It is issue 3 of
//     #11, which has happened in the field.
//
//   - The DEPENDENCY EDGES. VerifyIntegrity rejects a resource whose
//     dependency appears later in the resources array. Until this fixture grew
//     an injected resource that depends on another injected resource, every
//     edge pointed at an IMPORTABLE resource already earlier in the array, so
//     orderInjected's sort has only ever been exercised on already-valid input.
//
// A "same" preview is necessary but not sufficient for either: the preview is
// evaluated per resource, so it would not notice an array ordering that
// VerifyIntegrity rejects, and a provider mix-up in the same account and
// region could still preview clean.
func testInjectedStateCarriesProviderAndDependencyEdges(t *testing.T, ctx context.Context, fx *fixture) {
	p := provisionStack(t, ctx, fx)

	args := append(patchStateArgs(fx, p),
		"--project-dir", p.pulumiDir,
		"--stack", p.stackName,
		"--non-importable", p.sidecarPath,
		"--backup-dir", p.backupDir,
	)
	runTool(t, ctx, fx.binPath, fx.repoRoot, fx.env, args...)

	raw := rawStackExport(t, ctx, p.pulumiDir, fx.env, p.stackName)

	// --- 1. The us-east-1 certificate must reference a DIFFERENT provider
	// than the us-west-2 one, and that provider must actually be us-east-1.
	certType := "aws:iot/certificate:Certificate"
	eastURN := expectedURN(pulumiProject, p.stackName, certType, eastCertName)
	westURN := expectedURN(pulumiProject, p.stackName, certType, "cert")

	eastProviderRef, _ := findResourceByURN(t, raw, eastURN)["provider"].(string)
	westProviderRef, _ := findResourceByURN(t, raw, westURN)["provider"].(string)
	if eastProviderRef == "" || westProviderRef == "" {
		t.Fatalf("a certificate carries no provider reference (east=%q, west=%q)",
			eastProviderRef, westProviderRef)
	}

	if eastProviderRef == westProviderRef {
		t.Fatalf("both certificates were injected against the SAME provider (%s), but they live "+
			"in different regions — injection resolved the provider reference from the wrong "+
			"create step", eastProviderRef)
	}

	// Resolve the reference back to the provider resource and check its
	// region, rather than trusting that "different" means "correct".
	if region := providerRegion(t, raw, eastProviderRef); region != "us-east-1" {
		t.Errorf("%s was injected against a provider configured for region %q, want \"us-east-1\" — "+
			"a mis-resolved provider reference silently targets the wrong region", eastURN, region)
	} else {
		t.Logf("confirmed %s references a us-east-1 provider, distinct from the default", eastURN)
	}
	if region := providerRegion(t, raw, westProviderRef); region != "us-west-2" {
		t.Errorf("%s was injected against a provider configured for region %q, want \"us-west-2\"",
			westURN, region)
	}

	// --- 2. The dependent injected resource must appear AFTER the injected
	// resource it depends on, and must actually record the edge.
	attachURN := expectedURN(pulumiProject, p.stackName, "aws:iot/policyAttachment:PolicyAttachment", "policy_attach")
	order := resourceURNOrder(t, raw)
	certPos, certOK := order[westURN]
	attachPos, attachOK := order[attachURN]
	if !certOK || !attachOK {
		t.Fatalf("a resource is absent from the deployment (cert present=%v, attachment present=%v)",
			certOK, attachOK)
	}

	if attachPos < certPos {
		t.Errorf("%s (index %d) precedes the resource it depends on, %s (index %d) — "+
			"VerifyIntegrity rejects a forward reference, so orderInjected's sort did not "+
			"handle an edge between two INJECTED resources", attachURN, attachPos, westURN, certPos)
	} else {
		t.Logf("confirmed the dependent injected resource is ordered after its dependency (%d > %d)",
			attachPos, certPos)
	}

	deps, _ := findResourceByURN(t, raw, attachURN)["dependencies"].([]interface{})
	found := false
	for _, d := range deps {
		if s, ok := d.(string); ok && s == westURN {
			found = true
		}
	}
	if !found {
		t.Errorf("%s records dependencies %v, which do not include %s — without the edge, "+
			"the ordering above held by luck rather than by the sort", attachURN, deps, westURN)
	}
}

// providerRegion resolves a resource's provider reference (of the form
// "<provider urn>::<uuid>") back to that provider resource and returns the
// region it is configured for.
func providerRegion(t *testing.T, exportJSON []byte, providerRef string) string {
	t.Helper()
	urn := providerRef
	if i := strings.LastIndex(providerRef, "::"); i > 0 {
		urn = providerRef[:i]
	}
	prov := findResourceByURN(t, exportJSON, urn)
	for _, section := range []string{"inputs", "outputs"} {
		if m, ok := prov[section].(map[string]interface{}); ok {
			if region, ok := m["region"].(string); ok && region != "" {
				return region
			}
		}
	}
	t.Fatalf("provider %s declares no region in its inputs or outputs: %+v", urn, prov)
	return ""
}

// resourceURNOrder maps each URN to its index in the deployment's resources
// array. The array order is what VerifyIntegrity checks: a dependency must
// appear before its dependent.
func resourceURNOrder(t *testing.T, exportJSON []byte) map[string]int {
	t.Helper()
	var doc struct {
		Deployment struct {
			Resources []struct {
				URN string `json:"urn"`
			} `json:"resources"`
		} `json:"deployment"`
	}
	if err := json.Unmarshal(exportJSON, &doc); err != nil {
		t.Fatalf("decoding stack export: %v", err)
	}
	order := make(map[string]int, len(doc.Deployment.Resources))
	for i, r := range doc.Deployment.Resources {
		order[r.URN] = i
	}
	return order
}

// ---------------------------------------------------------------------------
// Scenario 1e: a stack whose secrets provider is not "passphrase".
// ---------------------------------------------------------------------------

// testKMSSecretsProvider runs the whole injection flow against a stack whose
// secrets are encrypted with AWS KMS rather than a passphrase.
//
// This is the class of bug that has already reached a real deployment
// undetected. E2E run 1 found that VerifyDeploymentIntegrity used
// stack.Base64SecretsProvider, whose OfType errors for any type but "b64" — so
// it had NEVER worked against a real deployment. Nine per-task reviews, a
// whole-branch review and the full unit suite all missed it, because every
// unit fixture carried no secrets_providers block. The fix added hand-written
// passphrase and service fixtures, which is the same kind of artifact that hid
// the bug in the first place.
//
// awskms rather than "service": it needs no Pulumi Cloud org or token, which
// is the dependency the local file backend was chosen to avoid (an earlier
// version of this test gated on PULUMI_ACCESS_TOKEN and silently skipped —
// a skip that reads as a pass). The key comes from the tofu fixture; see the
// comment on aws_kms_key.secrets in testdata/tf/main.tf for its cost.
func testKMSSecretsProvider(t *testing.T, ctx context.Context, fx *fixture) {
	keyID := tofuOutput(t, ctx, fx.tfDir, fx.env, "kms_key_id")
	if keyID == "" {
		t.Fatalf("the tofu fixture produced no kms_key_id output — this scenario cannot " +
			"fall back to passphrase, because passphrase is the configuration it exists to differ from")
	}
	// Key ID plus an explicit region, not the ARN: the secrets-provider value
	// is parsed as a URL, and an ARN's colons are read as a port
	// ("invalid port \":key\" after host"), which is how this scenario failed
	// on its first run.
	p := provisionStackWith(t, ctx, fx, "awskms://"+keyID+"?region="+fixtureRegion)

	// Prove the stack really is on a different provider before asserting
	// anything about it — otherwise a silent fallback to passphrase would
	// make this scenario a duplicate of the others.
	raw := rawStackExport(t, ctx, p.pulumiDir, fx.env, p.stackName)
	var doc struct {
		Deployment struct {
			SecretsProviders struct {
				Type string `json:"type"`
			} `json:"secrets_providers"`
		} `json:"deployment"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decoding stack export: %v", err)
	}
	if got := doc.Deployment.SecretsProviders.Type; got != "cloud" {
		t.Fatalf("stack secrets provider is %q, want \"cloud\" (awskms) — this scenario is "+
			"vacuous unless the provider actually differs from passphrase", got)
	}
	t.Logf("confirmed the stack's secrets provider is %q, not passphrase",
		doc.Deployment.SecretsProviders.Type)

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
		urn := sidecarURN(p.stackName, r)
		if op, ok := afterOps[urn]; !ok || op != "same" {
			t.Errorf("after injection on a KMS-encrypted stack: %s previews as %q, want \"same\"", urn, op)
		}
	}
	if !t.Failed() {
		t.Logf("confirmed injection verifies end to end on a stack encrypted with AWS KMS "+
			"(%d resources)", len(p.sidecar.Resources))
	}
}

// tofuOutput reads a single output value from the applied fixture.
func tofuOutput(t *testing.T, ctx context.Context, tfDir string, env []string, name string) string {
	t.Helper()
	out, err := runTofuCombined(ctx, tfDir, env, "output", "-raw", name)
	if err != nil {
		t.Fatalf("tofu output -raw %s: %v\n%s", name, err, out)
	}
	return strings.TrimSpace(out)
}

// ---------------------------------------------------------------------------
// Scenario 1f: an injected resource whose parent is a component.
// ---------------------------------------------------------------------------

// testComponentParent covers the last of the AWS-fixture coverage gaps
// (gap 7). Real migrations map Terraform modules to Pulumi components, so a
// component parent is the common shape rather than an edge case — and until
// the fixture program was ported from YAML to TypeScript it was unreachable,
// because Pulumi YAML cannot declare a ComponentResource.
//
// Two things here are not exercised anywhere else:
//
//   - The URN's type segment becomes "parentType$childType" rather than the
//     bare resource type. The sidecar records only the child's own type, so
//     everything that reconstructs a URN from a sidecar entry has to account
//     for the parent (see qualifiedTypes / sidecarURN).
//
//   - The injected resource carries a "parent" field, taken — like the
//     provider reference — from the preview's create step.
//
// The parent needs its own assertion rather than relying on the preview,
// because VerifyIntegrity only WARNS when a child's URN disagrees with its
// parent's; it does not error. So unlike the provider reference, whose
// mismatch failed the whole run loudly, a wrong parent would slip past the
// integrity check and show up only as a diff — or not at all.
func testComponentParent(t *testing.T, ctx context.Context, fx *fixture) {
	p := provisionStack(t, ctx, fx)

	var parented *pkg.NonImportableResource
	for i := range p.sidecar.Resources {
		if p.sidecar.Resources[i].Name == parentedCertName {
			parented = &p.sidecar.Resources[i]
		}
	}
	if parented == nil {
		t.Fatalf("the sidecar has no %q entry — the module's certificate was not matched. "+
			"Check that \"resolve tf\" was given --map module.certs=certs: without it the "+
			"module's resources and the component's children are never joined "+
			"(pkg/import_filler.go:198)", parentedCertName)
	}
	urn := sidecarURN(p.stackName, *parented)

	// Anti-vacuous: it must preview as "create" first, as every other
	// injected resource does.
	if op := runPreviewJSON(t, ctx, p.pulumiDir, fx.env, p.stackName).OpsByURN()[urn]; op != "create" {
		t.Fatalf("before injection: %s previews as %q, want \"create\" — if the URN is wrong "+
			"(a parented resource's type segment is \"parentType$childType\") this scenario "+
			"would silently assert nothing", urn, op)
	}

	args := append(patchStateArgs(fx, p),
		"--project-dir", p.pulumiDir,
		"--stack", p.stackName,
		"--non-importable", p.sidecarPath,
		"--backup-dir", p.backupDir,
	)
	runTool(t, ctx, fx.binPath, fx.repoRoot, fx.env, args...)

	if op := runPreviewJSON(t, ctx, p.pulumiDir, fx.env, p.stackName).OpsByURN()[urn]; op != "same" {
		t.Errorf("after injection: %s previews as %q, want \"same\"", urn, op)
	}

	// The parent edge itself, which the preview alone does not prove.
	raw := rawStackExport(t, ctx, p.pulumiDir, fx.env, p.stackName)
	res := findResourceByURN(t, raw, urn)
	gotParent, _ := res["parent"].(string)
	wantParent := expectedURN(pulumiProject, p.stackName, componentType, "certs")
	if gotParent != wantParent {
		t.Errorf("%s was injected with parent %q, want %q — VerifyIntegrity only warns about a "+
			"mismatched parent, so nothing else in this suite would catch it",
			urn, gotParent, wantParent)
	} else {
		t.Logf("confirmed the injected resource is parented by the component (%s)", wantParent)
	}
}

// ---------------------------------------------------------------------------
// Scenario 1c: a stack that already has diffs before the run must not be
// reverted for having them afterwards.
// ---------------------------------------------------------------------------

// testInjectionSurvivesPreExistingDrift covers the case CheckInjectionVerification
// was rewritten for in fix round 2 of task 6, and which no live run has ever
// exercised: patch-state is run ITERATIVELY during a migration, so the stack
// it runs against nearly always still has outstanding diffs. An absolute
// "preview must be clean" bar would revert almost every legitimate run, which
// is why the gate compares the preview before against the preview after.
//
// Every other scenario here starts from a stack whose baseline is clean, so
// the comparison has only ever been exercised in its 0-before case. This one
// makes the baseline genuinely dirty first — recorded as gap 4 in
// docs/superpowers/plans/2026-08-14-remaining-test-coverage.md.
//
// The drift is introduced in the PROGRAM rather than in state, because that is
// what a real mid-migration stack looks like: the operator has written more of
// their Pulumi program than they have patched into state yet. It is applied to
// an IMPORTABLE resource, so it is independent of anything injection does.
func testInjectionSurvivesPreExistingDrift(t *testing.T, ctx context.Context, fx *fixture) {
	p := provisionStack(t, ctx, fx)

	driftedURN := introduceProgramDrift(t, p.pulumiDir, p.stackName)

	// --- The baseline must actually be dirty, or the scenario proves
	// nothing: a clean baseline would make this a duplicate of scenario 1.
	baseline := runPreviewJSON(t, ctx, p.pulumiDir, fx.env, p.stackName)
	baselineOps := baseline.OpsByURN()
	if op := baselineOps[driftedURN]; op == "same" || op == "" {
		t.Fatalf("the drift did not take: %s previews as %q before the run, want a non-\"same\" "+
			"op — without a dirty baseline this scenario is vacuous", driftedURN, op)
	}
	t.Logf("confirmed a pre-existing diff: %s previews as %q before the run",
		driftedURN, baselineOps[driftedURN])

	// --- The run must SUCCEED despite that diff.
	args := append(patchStateArgs(fx, p),
		"--project-dir", p.pulumiDir,
		"--stack", p.stackName,
		"--non-importable", p.sidecarPath,
		"--backup-dir", p.backupDir,
	)
	out, err := runToolAllowFail(t, ctx, fx.binPath, fx.repoRoot, fx.env, args...)
	if err != nil {
		t.Fatalf("patch-state reverted a run against a stack that merely had a PRE-EXISTING "+
			"diff — the verification gate must compare before against after, not demand an "+
			"absolutely clean preview:\n%v\n%s", err, out)
	}
	if strings.Contains(out, "injection reverted") {
		t.Errorf("patch-state reported a revert despite exiting 0; output:\n%s", out)
	}

	// --- Injected resources still had to settle, and the pre-existing diff
	// must still be there: the gate tolerates it, it does not silence it.
	after := runPreviewJSON(t, ctx, p.pulumiDir, fx.env, p.stackName)
	afterOps := after.OpsByURN()
	for _, r := range p.sidecar.Resources {
		urn := sidecarURN(p.stackName, r)
		if op, ok := afterOps[urn]; !ok || op != "same" {
			t.Errorf("after injection: %s previews as %q, want \"same\" — a dirty baseline must "+
				"not lower the bar for the injected resources themselves", urn, op)
		}
	}
	if op := afterOps[driftedURN]; op == "same" {
		t.Errorf("the pre-existing diff on %s disappeared; the run was supposed to tolerate it, "+
			"not resolve it — this suggests patch-state wrote the program's drifted value into "+
			"state, which would mask a real difference rather than report it", driftedURN)
	} else {
		t.Logf("confirmed the pre-existing diff on %s survives the run (previews as %q)",
			driftedURN, op)
	}
}

// introduceProgramDrift edits the copied Pulumi program so one IMPORTABLE
// resource no longer matches its imported state, and returns that resource's
// URN. Editing the copy is safe: provisionStack gives every scenario its own.
//
// The target is the VPC Lattice target group's tag map. Tags are the safest
// possible drift for this purpose: the diff is an "update" rather than a
// "replace", it needs no AWS call to produce, and it cannot interact with the
// injected resources (the target group is imported, and the ATTACHMENT is what
// gets injected).
func introduceProgramDrift(t *testing.T, pulumiDir, stackName string) string {
	t.Helper()
	path := filepath.Join(pulumiDir, "index.ts")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	src := string(data)

	// Anchor on the target group's own declaration, then on the tags line
	// INSIDE it — every resource in this program shares the same "tags"
	// object, so an unanchored edit would drift all of them at once.
	//
	// These anchors are checked rather than assumed. When the fixture was
	// ported from YAML to TypeScript this function still anchored on YAML
	// (`\n  tg:\n`), and the guard below is what turned that into a failure
	// naming the stale anchor rather than a scenario that silently drifted
	// nothing and then asserted on a clean baseline.
	const decl = `new aws.vpclattice.TargetGroup("tg", {`
	i := strings.Index(src, decl)
	if i < 0 {
		t.Fatalf("%s no longer declares the target group as %s; this scenario's anchor is stale",
			path, decl)
	}
	const tagsLine = "\n    tags: tags,\n"
	j := strings.Index(src[i:], tagsLine)
	if j < 0 {
		t.Fatalf("%s's target group no longer carries the line %q; this scenario's anchor is stale",
			path, strings.TrimSpace(tagsLine))
	}

	// Spread rather than mutating the shared object, so exactly one resource
	// drifts.
	at := i + j
	drifted := src[:at] +
		"\n    tags: { ...tags, DriftMarker: \"introduced-by-InjectionSurvivesPreExistingDrift\" },\n" +
		src[at+len(tagsLine):]
	if err := os.WriteFile(path, []byte(drifted), 0o600); err != nil {
		t.Fatalf("writing drifted %s: %v", path, err)
	}
	t.Logf("introduced drift: added a tag to the target group in %s", path)

	return expectedURN(pulumiProject, stackName, "aws:vpclattice/targetGroup:TargetGroup", "tg")
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
		// Values are deliberately NOT printed: this document holds decrypted
		// secrets (see canonicalStackExport), and the whole point of the
		// comparison is that they match. Paths alone locate the regression.
		t.Errorf("stack export after the reverted run differs from before it — the revert did not "+
			"restore the stack exactly. Differing paths (values withheld — this export contains "+
			"decrypted secrets):\n  %s", strings.Join(differingJSONPaths(t, before, after), "\n  "))
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
		urn := sidecarURN(p.stackName, r)
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
			urn := sidecarURN(p.stackName, r)
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
		urn := sidecarURN(p.stackName, r)
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
//
// --project-dir and --stack are passed alongside --state/--out/--preview-json
// even though that combination still selects file mode (file mode is chosen
// by --state/--out being set, not by whether --project-dir/--stack are
// absent): patch-state_tf.go reads stack config secrets whenever
// --project-dir and --stack are both set, independent of mode. Without them,
// resolving the fixture's IoT certificate secrets would have no source and
// injection would hard-fail — the same reasoning as provisionStack's digest
// tf call not passing --skip-secrets.
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
		"--project-dir", p.pulumiDir,
		"--stack", p.stackName,
	)
	runTool(t, ctx, fx.binPath, fx.repoRoot, fx.env, args...)

	runPulumi(t, ctx, p.pulumiDir, fx.env, "stack", "import",
		"--stack", p.stackName, "--file", outPath)

	after := runPreviewJSON(t, ctx, p.pulumiDir, fx.env, p.stackName)
	afterOps := after.OpsByURN()
	for _, r := range p.sidecar.Resources {
		urn := sidecarURN(p.stackName, r)
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
		urn := sidecarURN(p.stackName, r)
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
		urn := sidecarURN(p.stackName, r)
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

// ---------------------------------------------------------------------------
// Scenario 6: a non-importable resource with Sensitive attributes, injected
// end to end — the highest-value gap in this fixture before this scenario.
// ---------------------------------------------------------------------------

// testSecretInjectedEndToEnd exercises the one path in this whole test that
// has never run before: "digest tf" redacting a sensitive attribute to
// "(sensitive)" and writing the real value into Pulumi stack config as a
// secret; the sidecar's redactedAttributes recording the config key;
// "patch-state tf --non-importable" resolving it and writing Pulumi's secret
// envelope into state; and (as it turns out, see the delta-outcome logging
// below) a raw-state delta embedding "(sensitive)" being dropped rather than
// repaired.
//
// The four assertions below are ordered by importance, per the design this
// scenario exists to validate:
//  1. the injected certificate previews as "same" — weak evidence on its own
//     (the program declares no input that would make a wrong secret show up
//     as a diff; certificatePem/privateKey/publicKey are outputs only,
//     which preview does not diff against without --refresh), but it does
//     confirm the resource otherwise matches the program's create step, and
//     a failed resolution would have failed the command outright before
//     reaching this preview at all (checkNoPlaceholders hard-errors on any
//     leftover placeholder — see pkg/state_injector.go).
//  2. no "(sensitive)" placeholder appears anywhere in the raw exported
//     deployment — the actual failure this redaction design exists to
//     prevent, checked directly on the exported bytes rather than inferred.
//  3. the resolved secret sits inside Pulumi's secret envelope (the
//     well-known sig key on a map value), not as a bare string — checked
//     structurally, without ever decrypting or reading the real secret
//     value, so no secret material passes through this test process.
//  4. whichever way the raw-state delta went (kept, or dropped because it
//     embedded an unresolvable "(sensitive)"), report it — this path has
//     never fired live before this scenario.
func testSecretInjectedEndToEnd(t *testing.T, ctx context.Context, fx *fixture) {
	p := provisionStack(t, ctx, fx)

	certURN := expectedURN(pulumiProject, p.stackName, wantNonImportable["cert"], "cert")

	before := runPreviewJSON(t, ctx, p.pulumiDir, fx.env, p.stackName)
	if op, ok := before.OpsByURN()[certURN]; !ok || op != "create" {
		t.Fatalf("before injection: %s previews as %q (ok=%v), want \"create\"", certURN, op, ok)
	}

	args := append(patchStateArgs(fx, p),
		"--project-dir", p.pulumiDir,
		"--stack", p.stackName,
		"--non-importable", p.sidecarPath,
		"--backup-dir", p.backupDir,
	)
	out, err := runToolAllowFail(t, ctx, fx.binPath, fx.repoRoot, fx.env, args...)
	if err != nil {
		t.Fatalf("patch-state tf --non-importable failed injecting the IoT certificate: %v\n%s", err, out)
	}

	// --- 1. previews as "same".
	after := runPreviewJSON(t, ctx, p.pulumiDir, fx.env, p.stackName)
	if op, ok := after.OpsByURN()[certURN]; !ok || op != "same" {
		t.Errorf("after injection: %s previews as %q (ok=%v), want \"same\"", certURN, op, ok)
	} else {
		t.Logf("confirmed %s previews as \"same\" after injection", certURN)
	}

	// --- 2. no "(sensitive)" placeholder anywhere in the raw export. This is
	// the check the whole redaction design exists to satisfy, so it greps
	// the raw bytes rather than trusting any structured field.
	raw := rawStackExport(t, ctx, p.pulumiDir, fx.env, p.stackName)
	if bytes.Contains(raw, []byte(redactedPlaceholderForTest)) {
		t.Errorf("stack export for %s contains the literal placeholder %q — a redacted secret "+
			"was never resolved and reached state", p.stackName, redactedPlaceholderForTest)
	} else {
		t.Logf("confirmed no %q placeholder anywhere in the raw stack export (%d bytes)",
			redactedPlaceholderForTest, len(raw))
	}

	// --- 3. the resolved secret is enveloped, not bare plaintext. Checked
	// structurally (presence of the secret sig key on a map value) so the
	// real secret material is never read into this test process.
	certRes := findResourceByURN(t, raw, certURN)
	outputs, _ := certRes["outputs"].(map[string]interface{})
	if outputs == nil {
		t.Fatalf("%s has no \"outputs\" in the raw export: %+v", certURN, certRes)
	}
	// caPem is deliberately NOT in this list. It is Sensitive in the provider
	// schema like the other three, but testdata/tf/main.tf leaves ca_pem
	// unset, so there is no value to envelope — see the caPem assertion
	// below, which pins that distinction rather than letting it pass
	// silently.
	for _, prop := range []string{"privateKey", "publicKey", "certificatePem"} {
		val, ok := outputs[prop]
		if !ok {
			t.Errorf("%s outputs has no %q property", certURN, prop)
			continue
		}
		envelope, isMap := val.(map[string]interface{})
		if !isMap {
			t.Errorf("%s output %q is not enveloped (got %T, a bare value) — a resolved secret "+
				"must be wrapped in Pulumi's secret envelope, never written as plaintext", certURN, prop, val)
			continue
		}
		if _, hasSig := envelope[secretSigKey]; !hasSig {
			t.Errorf("%s output %q is a map but carries no secret sig key %q: %+v",
				certURN, prop, secretSigKey, envelope)
			continue
		}
		t.Logf("confirmed %s output %q is enveloped as a secret", certURN, prop)
	}

	// caPem is Sensitive in the provider schema but null in Terraform state,
	// because testdata/tf/main.tf leaves ca_pem unset. It must therefore
	// arrive as a null output and NOT as an envelope: there is no value to
	// protect, and manufacturing one would mean inventing a secret Terraform
	// never had. This is the state-side half of the fix for the e2e run of
	// 2026-08-14, where "pulumi preview --json" masked caPem as "[secret]"
	// (providers mask from the schema, whether or not a value exists) and
	// injection hard-failed looking for a config key that correctly did not
	// exist.
	if val, ok := outputs["caPem"]; !ok {
		t.Logf("confirmed %s has no %q output at all (Terraform had no value for it)", certURN, "caPem")
	} else if val != nil {
		t.Errorf("%s output %q is %#v, want null — ca_pem is unset in testdata/tf/main.tf, so "+
			"there is no secret here to resolve or envelope", certURN, "caPem", val)
	} else {
		t.Logf("confirmed %s output %q is null rather than an invented secret", certURN, "caPem")
	}

	// NOTE: no property in this fixture exercises resolveSecretInputs
	// (pkg/state_injector.go) end to end any more. caPem used to, while the
	// program declared it, but that made the program disagree with the
	// Terraform config it is meant to translate — see the comment on "cert"
	// in testdata/pulumi/Pulumi.yaml. The input-resolution path is still
	// covered by unit tests (TestInjectNonImportable_ResolvesSecretFromConfig
	// and _FillWrapsSecretInput); closing the e2e gap needs a non-importable
	// resource with a Sensitive INPUT that holds a real value, which this
	// fixture does not currently have. Recorded in
	// docs/superpowers/plans/2026-08-14-remaining-test-coverage.md.

	// --- 3b. the raw-state delta must not smuggle secret material into
	// state. Unlike the properties above, the delta is written as a plain
	// output with no secret envelope, so anything inside it lands in the
	// deployment unprotected. The certificate's Sensitive attributes are all
	// PEM blocks, which makes "-----BEGIN" a cheap and specific probe — and
	// checking it here rather than against the whole export is deliberate:
	// the enveloped properties legitimately contain PEM material.
	if delta, ok := outputs["__pulumi_raw_state_delta"]; ok {
		encoded, err := json.Marshal(delta)
		if err != nil {
			t.Fatalf("marshalling %s raw-state delta: %v", certURN, err)
		}
		if bytes.Contains(encoded, []byte("-----BEGIN")) {
			t.Errorf("%s raw-state delta contains PEM material — the delta is written to state "+
				"WITHOUT a secret envelope, so a Sensitive attribute reaching it is a plaintext "+
				"secret in the deployment", certURN)
		} else {
			t.Logf("confirmed %s raw-state delta carries no PEM material (%d bytes)", certURN, len(encoded))
		}
	}

	// --- 4. report whether the delta was dropped. Both outcomes are
	// informative — this is the first live run of either branch — so this
	// logs rather than asserts a specific one. InjectResult's per-resource
	// notes (see pkg/state_injector.go's attachRawStateDelta) always name the
	// resource by its Terraform address, so that address is what
	// distinguishes "mentioned in a drop/absent section" from "kept intact
	// and never mentioned at all".
	const certAddr = "aws_iot_certificate.cert"
	switch {
	case strings.Contains(out, certAddr) && strings.Contains(out, "embedded an unresolvable"):
		t.Logf("FINDING: the certificate's raw-state delta was dropped as predicted — it embedded "+
			"an unresolvable %q placeholder (every Sensitive attribute on this resource type is "+
			"redacted before the delta is computed, so this is the expected outcome). Command output:\n%s",
			redactedPlaceholderForTest, out)
	case strings.Contains(out, certAddr) && strings.Contains(out, "failed validation"):
		t.Logf("FINDING: the certificate's raw-state delta was dropped for a different reason than "+
			"expected — it failed Recover validation rather than embedding a placeholder. Command output:\n%s", out)
	case strings.Contains(out, certAddr) && strings.Contains(out, "carried no raw-state delta"):
		t.Logf("FINDING: the certificate was injected with no raw-state delta at all — \"digest tf\" "+
			"never produced one for it. Command output:\n%s", out)
	default:
		t.Logf("FINDING: the certificate's raw-state delta survived injection intact (not named in any "+
			"drop/absent section) — worth a closer look, since every Sensitive attribute on this "+
			"resource type should have embedded the redaction placeholder before the delta was "+
			"computed. Command output:\n%s", out)
	}
}

// redactedPlaceholderForTest mirrors pkg's unexported redactedPlaceholder
// ("(sensitive)"); duplicated here since the package does not export it.
const redactedPlaceholderForTest = "(sensitive)"

// rawStackExport runs "pulumi stack export" and returns its raw bytes,
// undecrypted and uncanonicalized — unlike canonicalStackExport, callers of
// this want to grep the exact bytes a real "pulumi stack export" produces,
// not a byte-for-byte diff against an earlier export.
func rawStackExport(t *testing.T, ctx context.Context, dir string, env []string, stackName string) []byte {
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
	return out
}

// findResourceByURN parses a raw "pulumi stack export" deployment and
// returns the resource entry with the given URN, failing the test if it is
// not found.
func findResourceByURN(t *testing.T, exportJSON []byte, urn string) map[string]interface{} {
	t.Helper()
	var state map[string]interface{}
	dec := json.NewDecoder(bytes.NewReader(exportJSON))
	dec.UseNumber()
	if err := dec.Decode(&state); err != nil {
		t.Fatalf("parsing stack export: %v", err)
	}
	deployment, _ := state["deployment"].(map[string]interface{})
	resources, _ := deployment["resources"].([]interface{})
	for _, r := range resources {
		res, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		if res["urn"] == urn {
			return res
		}
	}
	t.Fatalf("no resource with URN %s found in stack export", urn)
	return nil
}

// ---------------------------------------------------------------------------
// Scenario 7: a non-importable resource with a nested list-of-objects
// property, injected end to end.
// ---------------------------------------------------------------------------

// testNestedBlockInjection exercises MakeTerraformOutputs and
// RawStateComputeDelta (pkg/raw_state_delta.go) against a genuinely nested
// shape — every other resource this fixture injects is flat top-level
// strings, so this is the first time either function has run end to end
// against a "target": {id, port} object.
//
// The resource previewing as "same" is the assertion that matters; whether
// it got a working raw-state delta is secondary and reported either way,
// per the task this scenario exists to satisfy — "patch-state" prints a
// per-resource cause for a dropped delta, so a dropped delta's reason is
// captured from that output rather than left as a bare pass/fail.
func testNestedBlockInjection(t *testing.T, ctx context.Context, fx *fixture) {
	p := provisionStack(t, ctx, fx)

	attachURN := expectedURN(pulumiProject, p.stackName, wantNonImportable["attach"], "attach")

	before := runPreviewJSON(t, ctx, p.pulumiDir, fx.env, p.stackName)
	if op, ok := before.OpsByURN()[attachURN]; !ok || op != "create" {
		t.Fatalf("before injection: %s previews as %q (ok=%v), want \"create\"", attachURN, op, ok)
	}

	args := append(patchStateArgs(fx, p),
		"--project-dir", p.pulumiDir,
		"--stack", p.stackName,
		"--non-importable", p.sidecarPath,
		"--backup-dir", p.backupDir,
	)
	out, err := runToolAllowFail(t, ctx, fx.binPath, fx.repoRoot, fx.env, args...)
	if err != nil {
		t.Fatalf("patch-state tf --non-importable failed injecting the target group attachment: %v\n%s", err, out)
	}

	after := runPreviewJSON(t, ctx, p.pulumiDir, fx.env, p.stackName)
	if op, ok := after.OpsByURN()[attachURN]; !ok || op != "same" {
		t.Errorf("after injection: %s previews as %q (ok=%v), want \"same\" — the injected nested "+
			"\"target\" values do not match reality", attachURN, op, ok)
	} else {
		t.Logf("confirmed %s previews as \"same\" after injection (nested \"target\" block round-tripped)",
			attachURN)
	}

	// Same reasoning as testSecretInjectedEndToEnd's step 4: InjectResult's
	// per-resource notes always name the resource by its Terraform address,
	// so that address distinguishes "mentioned in a drop/absent section"
	// from "kept intact and never mentioned at all".
	const attachAddr = "aws_vpclattice_target_group_attachment.attach"
	switch {
	case strings.Contains(out, attachAddr) && strings.Contains(out, "failed validation"):
		t.Logf("FINDING: the target group attachment's raw-state delta was dropped — it failed "+
			"validation against its (nested) outputs. Command output:\n%s", out)
	case strings.Contains(out, attachAddr) && strings.Contains(out, "carried no raw-state delta"):
		t.Logf("FINDING: the target group attachment was injected with no raw-state delta at all — "+
			"\"digest tf\" never produced one for it. Command output:\n%s", out)
	case strings.Contains(out, attachAddr) && strings.Contains(out, "embedded an unresolvable"):
		t.Logf("FINDING: the target group attachment's raw-state delta was dropped for embedding an "+
			"unresolvable placeholder — unexpected, since this resource has no Sensitive attributes. "+
			"Command output:\n%s", out)
	default:
		t.Logf("FINDING: the target group attachment's nested raw-state delta survived injection intact "+
			"(not named in any drop/absent section) — MakeTerraformOutputs/RawStateComputeDelta round-"+
			"tripped the nested \"target\" block correctly. Command output:\n%s", out)
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
	out, err := runPulumiAllowFail(t, ctx, dir, env, args...)
	if err != nil {
		t.Fatalf("pulumi %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// runPulumiAllowFail runs the pulumi CLI in dir and returns its combined
// output and error without failing the test — for scenarios where the
// command failing is a legitimate outcome to be recorded rather than a
// test failure.
func runPulumiAllowFail(t *testing.T, ctx context.Context, dir string, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(ctx, "pulumi", args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	t.Logf("$ pulumi %s\n%s", strings.Join(args, " "), out)
	return string(out), err
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
// canonicalStackExport exports a stack's deployment and canonicalizes it for
// byte comparison.
//
// --show-secrets is required, not incidental. Without it every secret is
// emitted as "v1:<nonce>:<ciphertext>", and the passphrase provider picks a
// fresh nonce on every encryption — so an export/import round trip re-encrypts
// identical plaintext into different bytes and the comparison can never
// succeed on a stack holding any secret. The e2e run of 2026-08-15 failed
// exactly this way: the only differences between the two documents were the
// ciphertext of the VPN tunnels' pre-shared keys and customer-gateway
// configuration, whose plaintext was unchanged. Decrypted, those values are
// stable and the comparison tests what it means to test.
//
// The cost is that the returned bytes contain decrypted secrets, so callers
// must not print them — see the failure branch in testRevertRestoresStackExactly.
func canonicalStackExport(t *testing.T, ctx context.Context, dir string, env []string, stackName string) []byte {
	t.Helper()
	cmd := exec.CommandContext(ctx, "pulumi", "stack", "export", "--show-secrets", "--stack", stackName)
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

// differingJSONPaths decodes two canonicalized stack exports and reports the
// paths at which they differ, capped so a wholesale mismatch cannot flood the
// terminal. Values are never returned — see jsonPathDiff.
func differingJSONPaths(t *testing.T, before, after []byte) []string {
	t.Helper()
	var a, b interface{}
	if err := json.Unmarshal(before, &a); err != nil {
		return []string{fmt.Sprintf("(could not decode the \"before\" export: %v)", err)}
	}
	if err := json.Unmarshal(after, &b); err != nil {
		return []string{fmt.Sprintf("(could not decode the \"after\" export: %v)", err)}
	}
	paths := jsonPathDiff(a, b, "")
	if len(paths) == 0 {
		// The byte comparison failed but the decoded documents match, so the
		// difference is in encoding rather than content. Say so plainly
		// rather than reporting an empty list.
		return []string{"(documents decode identically — the difference is in JSON encoding, not content)"}
	}
	if len(paths) > diffPathLimit {
		return append(paths[:diffPathLimit],
			fmt.Sprintf("... and %d more", len(paths)-diffPathLimit))
	}
	return paths
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

// ---------------------------------------------------------------------------
// Scenario 13: a deliberately corrupted raw-state delta must break the next
// preview.
// ---------------------------------------------------------------------------

// testCorruptDeltaFailsPreview is the negative that makes every other delta
// assertion in this suite mean something.
//
// Every other scenario asserts that injected resources preview as "same" while
// carrying a delta. That is equally consistent with two very different worlds:
// the delta is correct and the bridge uses it, OR the delta is never consumed
// at all and the resource previews clean for unrelated reasons. Nothing
// distinguished them. That is the same shape as the orphan sweep which silently
// checked nothing for fourteen runs while every run reported success.
//
// The delta IS consumed on this path, established from bridge source:
// makeTerraformStateViaUpgradeEnabled is consulted in Diff (as well as Read and
// Update) at tfbridge/provider.go:1129, the sdk-v2 shim implements
// ProviderWithRawStateSupport (sdk-v2/provider2.go:540) so the path is
// available rather than skipped, and makeTerraformStateViaUpgrade wraps both
// UnmarshalRawStateDelta and Recover in contract.AssertNoErrorf — which
// panics unconditionally, with no build tag.
//
// So corrupting a delta and previewing should fail loudly. If it does NOT, the
// finding is far more valuable than a passing scenario: it means the delta is
// inert on this path and every "preview as same" in this file proves less than
// it appears to.
//
// Expect a provider crash rather than a tidy error message. The assertion is on
// failure, deliberately not on wording, since the exact text belongs to the
// engine and the plugin host and will change.
func testCorruptDeltaFailsPreview(t *testing.T, ctx context.Context, fx *fixture) {
	p := provisionStack(t, ctx, fx)

	args := append(patchStateArgs(fx, p),
		"--project-dir", p.pulumiDir,
		"--stack", p.stackName,
		"--non-importable", p.sidecarPath,
		"--backup-dir", p.backupDir,
	)
	out, err := runToolAllowFail(t, ctx, fx.binPath, fx.repoRoot, fx.env, args...)
	if err != nil {
		t.Fatalf("injection failed before the corruption step could run: %v\n%s", err, out)
	}

	// The preview must be clean BEFORE the corruption, or a failure afterwards
	// proves nothing about the delta.
	beforeOps := runPreviewJSON(t, ctx, p.pulumiDir, fx.env, p.stackName).OpsByURN()
	for _, r := range p.sidecar.Resources {
		urn := sidecarURN(p.stackName, r)
		if op, ok := beforeOps[urn]; !ok || op != "same" {
			t.Fatalf("baseline for the corruption test is not clean: %s previews as %q (ok=%v)",
				urn, op, ok)
		}
	}

	statePath := filepath.Join(t.TempDir(), "state.json")
	runPulumiToFile(t, ctx, p.pulumiDir, fx.env, statePath,
		"stack", "export", "--stack", p.stackName)
	stateBytes, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("reading stack export: %v", err)
	}

	var state map[string]interface{}
	if err := json.Unmarshal(stateBytes, &state); err != nil {
		t.Fatalf("decoding stack export: %v", err)
	}
	deployment, _ := state["deployment"].(map[string]interface{})
	if deployment == nil {
		t.Fatalf("stack export has no deployment")
	}
	resources, _ := deployment["resources"].([]interface{})

	injected := map[string]bool{}
	for _, r := range p.sidecar.Resources {
		injected[sidecarURN(p.stackName, r)] = true
	}

	// Corrupt the first INJECTED resource that actually carries a delta —
	// found rather than assumed, so this cannot silently corrupt nothing.
	const deltaKey = "__pulumi_raw_state_delta"
	corruptedURN := ""
	for _, raw := range resources {
		res, _ := raw.(map[string]interface{})
		if res == nil {
			continue
		}
		urn, _ := res["urn"].(string)
		if !injected[urn] {
			continue
		}
		outputs, _ := res["outputs"].(map[string]interface{})
		if outputs == nil {
			continue
		}
		if _, hasDelta := outputs[deltaKey]; !hasDelta {
			continue
		}
		// An assetDelta whose archiveFormat is a string where the bridge's own
		// struct declares an archive.Format. Measured offline to fail
		// UnmarshalRawStateDelta outright with "cannot unmarshal string into Go
		// struct field assetDelta...archiveFormat of type archive.Format", so
		// the corruption is guaranteed to bite rather than merely being odd.
		outputs[deltaKey] = map[string]interface{}{
			"obj": map[string]interface{}{
				"ps": map[string]interface{}{
					"corrupted": map[string]interface{}{
						"asset": map[string]interface{}{
							"kind":          1,
							"archiveFormat": "definitely-not-a-number",
						},
					},
				},
			},
		}
		corruptedURN = urn
		break
	}
	if corruptedURN == "" {
		t.Fatalf("no injected resource in the stack carries a %s — there was nothing to corrupt, "+
			"so this test cannot prove anything. Check the \"Deltas attached (injected)\" line in:\n%s",
			deltaKey, out)
	}
	t.Logf("corrupted the raw-state delta of %s", corruptedURN)

	corruptBytes, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("re-encoding corrupted state: %v", err)
	}
	corruptPath := filepath.Join(t.TempDir(), "corrupt.json")
	if err := os.WriteFile(corruptPath, corruptBytes, 0o600); err != nil {
		t.Fatalf("writing corrupted state: %v", err)
	}
	runPulumi(t, ctx, p.pulumiDir, fx.env, "stack", "import",
		"--stack", p.stackName, "--file", corruptPath)

	previewOut, previewErr := runPulumiAllowFail(t, ctx, p.pulumiDir, fx.env,
		"preview", "--stack", p.stackName)
	if previewErr == nil {
		t.Errorf("preview SUCCEEDED against a knowingly corrupt raw-state delta on %s.\n"+
			"That means the delta is not being consumed on this path, and every other "+
			"\"previews as same\" assertion in this file proves less than it appears to — "+
			"they would pass whether or not the delta were correct. Investigate whether the "+
			"provider still satisfies shim.ProviderWithRawStateSupport before trusting any "+
			"delta coverage here.\nPreview output:\n%s", corruptedURN, previewOut)
		return
	}
	t.Logf("confirmed the delta is load-bearing: preview failed against a corrupt delta on %s "+
		"(%v). Every other delta assertion in this file is therefore sensitive to delta "+
		"correctness, not merely consistent with it.", corruptedURN, previewErr)
}

// deltasAttachedRe matches patch-state's "Deltas attached (injected): X of Y"
// summary line.
var deltasAttachedRe = regexp.MustCompile(`Deltas attached \(injected\):\s+(\d+) of (\d+)`)

// assertEveryInjectedResourceGotADelta requires that every injected resource
// carried a raw-state delta into state.
//
// Nothing asserted this before, and a regression to "0 of 11" would pass every
// scenario silently: a missing delta does not fail, it degrades the resource to
// the bridge's legacy Pulumi->Terraform conversion, which still previews as
// "same". The delta is the primary conversion path for Diff, Read and Update
// (tfbridge/provider.go:1129/:1442/:1651), so losing it costs correctness on
// every later operation without costing anything visible here.
//
// This is why the summary reports X of Y positively rather than leaving it to
// be inferred from three absence counters being zero — those two readings look
// identical at zero, and only one of them is a statement about the artifact.
func assertEveryInjectedResourceGotADelta(t *testing.T, patchStateOutput string) {
	t.Helper()

	m := deltasAttachedRe.FindStringSubmatch(patchStateOutput)
	if m == nil {
		t.Errorf("patch-state output has no \"Deltas attached (injected)\" line — the summary "+
			"changed and this assertion is now blind. Output:\n%s", patchStateOutput)
		return
	}
	attached, injected := m[1], m[2]
	if injected == "0" {
		t.Errorf("patch-state reports 0 injected resources, so this assertion proves nothing")
		return
	}
	if attached != injected {
		t.Errorf("only %s of %s injected resources carried a raw-state delta. A resource without "+
			"one still previews as \"same\" — it silently falls back to the bridge's legacy state "+
			"conversion — so this will not surface anywhere else. Output:\n%s",
			attached, injected, patchStateOutput)
		return
	}
	t.Logf("confirmed all %s injected resource(s) carried a raw-state delta", attached)
}
