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

const pulumiProject = "tool-import-e2e"

var wantNonImportable = map[string]string{
	"prop[0]": "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation",
	"prop[1]": "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation",
	"prop[2]": "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation",
	"route":   "aws:ec2/vpnConnectionRoute:VpnConnectionRoute",
	"cert":    "aws:iot/certificate:Certificate",
	"attach":  "aws:vpclattice/targetGroupAttachment:TargetGroupAttachment",

	"east": "aws:iot/certificate:Certificate",

	"policy_attach": "aws:iot/policyAttachment:PolicyAttachment",

	`each["alpha"]`: "aws:iot/certificate:Certificate",
	`each["beta"]`:  "aws:iot/certificate:Certificate",

	"inmodule": "aws:iot/certificate:Certificate",
}

const eastCertName = "east"

const parentedCertName = "inmodule"

const componentType = "toolimport:index:Certs"

var qualifiedTypes = map[string]string{
	parentedCertName: componentType + "$aws:iot/certificate:Certificate",
}

func sidecarURN(stackName string, r pkg.NonImportableResource) string {
	typ := r.Type
	if qualified, ok := qualifiedTypes[r.Name]; ok {
		typ = qualified
	}
	return expectedURN(pulumiProject, stackName, typ, r.Name)
}

const secretSigKey = "4dabf18193072939515e22adb298388d"

type fixture struct {
	repoRoot         string
	binPath          string
	tfDir            string
	tfStatePath      string
	pulumiFixtureDir string
	env              []string

	nodeModulesDir string
}

func TestNonImportableStateInjection(t *testing.T) {
	ctx := context.Background()

	logCallerIdentity(t, ctx)
	requireBinary(t, "tofu")
	requireBinary(t, "pulumi")
	requireGlobalLoginUntouched(t)

	repoRoot := repoRoot(t)
	binPath := buildTool(t, ctx, repoRoot)

	tfRoot, err := os.MkdirTemp("", "pulumi-tool-import-e2e-tf-")
	if err != nil {
		t.Fatalf("creating tf working directory: %v", err)
	}
	tfDir := filepath.Join(tfRoot, "tf")
	if err := copyDir(filepath.Join(repoRoot, "test", "e2e", "testdata", "tf"), tfDir); err != nil {
		t.Fatalf("copying tf fixture: %v", err)
	}

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

	pulumiFixtureDir := filepath.Join(repoRoot, "test", "e2e", "testdata", "pulumi-ts")
	nodeModulesDir := installNodeModules(t, ctx, pulumiFixtureDir, env)

	runTofu(t, ctx, tfDir, env, "init", "-input=false")
	t.Cleanup(func() {
		ids, idErr := loadFixtureResourceIDs(tfDir)
		if idErr != nil {
			t.Errorf("could not read fixture resource IDs from %s's Terraform state to double "+
				"check them against AWS directly: %v — this does NOT mean nothing is left behind, "+
				"only that this check could not run; check the account by hand", tfDir, idErr)
		}

		out, err := runTofuCombined(ctx, tfDir, env, "destroy", "-auto-approve", "-input=false")
		if err == nil {
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

var stackSeq int64

type provisioned struct {
	pulumiDir        string
	stackName        string
	digestPath       string
	filledImportPath string
	sidecarPath      string
	sidecar          *pkg.NonImportableFile
	backupDir        string
}

func provisionStack(t *testing.T, ctx context.Context, fx *fixture) *provisioned {
	t.Helper()
	return provisionStackWith(t, ctx, fx, "")
}

func provisionStackWith(t *testing.T, ctx context.Context, fx *fixture, secretsProvider string) *provisioned {
	t.Helper()

	pulumiDir := filepath.Join(t.TempDir(), "pulumi")
	if err := copyDir(fx.pulumiFixtureDir, pulumiDir); err != nil {
		t.Fatalf("copying pulumi fixture: %v", err)
	}
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

	runPulumi(t, ctx, pulumiDir, fx.env, "up",
		"--stack", stackName,
		"--target", expectedURN(pulumiProject, stackName, "pulumi:providers:aws", "eastProvider"),
		"--yes",
		"--skip-preview",
	)

	digestPath := filepath.Join(t.TempDir(), "tf-digest.json")
	runTool(t, ctx, fx.binPath, fx.repoRoot, fx.env, "digest", "tf",
		"--from", fx.tfDir,
		"--state-file", fx.tfStatePath,
		"--out", digestPath,
		"--pulumi-stack", stackName,
		"--pulumi-project", pulumiProject,
		"--project-dir", pulumiDir,
	)

	importSkeletonPath := filepath.Join(t.TempDir(), "import-skeleton.json")
	runPulumi(t, ctx, pulumiDir, fx.env, "preview",
		"--stack", stackName, "--import-file", importSkeletonPath)

	filledImportPath := filepath.Join(t.TempDir(), "filled-import.json")
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

func patchStateArgs(fx *fixture, p *provisioned) []string {
	return []string{"patch-state", "tf",
		"--digest", p.digestPath,
		"--fields", filepath.Join(fx.repoRoot, "data", "aws-import-diff-fields.json"),
		"--config-dir", fx.tfDir,
	}
}

func testPreviewGoesFromCreateToSame(t *testing.T, ctx context.Context, fx *fixture) {
	p := provisionStack(t, ctx, fx)

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

func testClassificationIsNotOverBroad(t *testing.T, ctx context.Context, fx *fixture) {
	p := provisionStack(t, ctx, fx)

	type outcome struct {
		name, typ, detail string
	}
	var importable, wrongState []outcome

	for _, r := range p.sidecar.Resources {
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
			t.Logf("confirmed non-importable: %s (%s) — pulumi import failed", r.Name, r.Type)
			continue
		}

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

func testKMSSecretsProvider(t *testing.T, ctx context.Context, fx *fixture) {
	keyID := tofuOutput(t, ctx, fx.tfDir, fx.env, "kms_key_id")
	if keyID == "" {
		t.Fatalf("the tofu fixture produced no kms_key_id output — this scenario cannot " +
			"fall back to passphrase, because passphrase is the configuration it exists to differ from")
	}
	p := provisionStackWith(t, ctx, fx, "awskms://"+keyID+"?region="+fixtureRegion)

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

func tofuOutput(t *testing.T, ctx context.Context, tfDir string, env []string, name string) string {
	t.Helper()
	out, err := runTofuCombined(ctx, tfDir, env, "output", "-raw", name)
	if err != nil {
		t.Fatalf("tofu output -raw %s: %v\n%s", name, err, out)
	}
	return strings.TrimSpace(out)
}

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

func testInjectionSurvivesPreExistingDrift(t *testing.T, ctx context.Context, fx *fixture) {
	p := provisionStack(t, ctx, fx)

	driftedURN := introduceProgramDrift(t, p.pulumiDir, p.stackName)

	baseline := runPreviewJSON(t, ctx, p.pulumiDir, fx.env, p.stackName)
	baselineOps := baseline.OpsByURN()
	if op := baselineOps[driftedURN]; op == "same" || op == "" {
		t.Fatalf("the drift did not take: %s previews as %q before the run, want a non-\"same\" "+
			"op — without a dirty baseline this scenario is vacuous", driftedURN, op)
	}
	t.Logf("confirmed a pre-existing diff: %s previews as %q before the run",
		driftedURN, baselineOps[driftedURN])

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

func introduceProgramDrift(t *testing.T, pulumiDir, stackName string) string {
	t.Helper()
	path := filepath.Join(pulumiDir, "index.ts")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	src := string(data)

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
			"restore the stack exactly. Differing paths (values withheld — this export contains "+
			"decrypted secrets):\n  %s", strings.Join(differingJSONPaths(t, before, after), "\n  "))
	} else {
		t.Logf("confirmed the stack's exported deployment is unchanged after the reverted run "+
			"(%d bytes, canonicalized)", len(before))
	}
}

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

	secondBackupDir := t.TempDir()
	args2 := append(patchStateArgs(fx, p),
		"--project-dir", p.pulumiDir,
		"--stack", p.stackName,
		"--non-importable", p.sidecarPath,
		"--backup-dir", secondBackupDir,
	)
	out2, err2 := runToolAllowFail(t, ctx, fx.binPath, fx.repoRoot, fx.env, args2...)

	if err2 == nil {
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

	t.Logf("second, identical injection run failed as predicted from source reading "+
		"(no create step left to match): %v", err2)
	if !strings.Contains(out2, "no create step in the preview matches") {
		t.Errorf("second run failed, but not with the expected \"no create step in the preview "+
			"matches\" message — got a different failure mode, which is itself worth reporting. output:\n%s",
			out2)
	}

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

	after := runPreviewJSON(t, ctx, p.pulumiDir, fx.env, p.stackName)
	if op, ok := after.OpsByURN()[certURN]; !ok || op != "same" {
		t.Errorf("after injection: %s previews as %q (ok=%v), want \"same\"", certURN, op, ok)
	} else {
		t.Logf("confirmed %s previews as \"same\" after injection", certURN)
	}

	raw := rawStackExport(t, ctx, p.pulumiDir, fx.env, p.stackName)
	if bytes.Contains(raw, []byte(redactedPlaceholderForTest)) {
		t.Errorf("stack export for %s contains the literal placeholder %q — a redacted secret "+
			"was never resolved and reached state", p.stackName, redactedPlaceholderForTest)
	} else {
		t.Logf("confirmed no %q placeholder anywhere in the raw stack export (%d bytes)",
			redactedPlaceholderForTest, len(raw))
	}

	certRes := findResourceByURN(t, raw, certURN)
	outputs, _ := certRes["outputs"].(map[string]interface{})
	if outputs == nil {
		t.Fatalf("%s has no \"outputs\" in the raw export: %+v", certURN, certRes)
	}
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

	if val, ok := outputs["caPem"]; !ok {
		t.Logf("confirmed %s has no %q output at all (Terraform had no value for it)", certURN, "caPem")
	} else if val != nil {
		t.Errorf("%s output %q is %#v, want null — ca_pem is unset in testdata/tf/main.tf, so "+
			"there is no secret here to resolve or envelope", certURN, "caPem", val)
	} else {
		t.Logf("confirmed %s output %q is null rather than an invented secret", certURN, "caPem")
	}

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

const redactedPlaceholderForTest = "(sensitive)"

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

func globalCredentialsPath() string {
	home := os.Getenv("PULUMI_HOME")
	if home == "" {
		if dir, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(dir, ".pulumi")
		}
	}
	return filepath.Join(home, "credentials.json")
}

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

func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed to resolve this file's path")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}
	return root
}

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

func runTool(t *testing.T, ctx context.Context, binPath, dir string, env []string, args ...string) {
	t.Helper()
	out, err := runToolAllowFail(t, ctx, binPath, dir, env, args...)
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", binPath, strings.Join(args, " "), err, out)
	}
}

func runToolAllowFail(t *testing.T, ctx context.Context, binPath, dir string, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	t.Logf("$ %s %s\n%s", binPath, strings.Join(args, " "), out)
	return string(out), err
}

func runPulumi(t *testing.T, ctx context.Context, dir string, env []string, args ...string) {
	t.Helper()
	out, err := runPulumiAllowFail(t, ctx, dir, env, args...)
	if err != nil {
		t.Fatalf("pulumi %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func runPulumiAllowFail(t *testing.T, ctx context.Context, dir string, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(ctx, "pulumi", args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	t.Logf("$ pulumi %s\n%s", strings.Join(args, " "), out)
	return string(out), err
}

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
		return []string{"(documents decode identically — the difference is in JSON encoding, not content)"}
	}
	if len(paths) > diffPathLimit {
		return append(paths[:diffPathLimit],
			fmt.Sprintf("... and %d more", len(paths)-diffPathLimit))
	}
	return paths
}

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

func runTofu(t *testing.T, ctx context.Context, dir string, env []string, args ...string) {
	t.Helper()
	out, err := runTofuCombined(ctx, dir, env, args...)
	t.Logf("$ tofu %s\n%s", strings.Join(args, " "), out)
	if err != nil {
		t.Fatalf("tofu %s: %v", strings.Join(args, " "), err)
	}
}

func runTofuCombined(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "tofu", args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

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

var deltasAttachedRe = regexp.MustCompile(`Deltas attached \(injected\):\s+(\d+) of (\d+)`)

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
