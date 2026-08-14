# Non-Importable State Injection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `patch-state tf --non-importable <sidecar>` writes resources that cannot be imported into a Pulumi deployment, taking every field but `id` and `outputs` from a `pulumi preview --json` of the program, and proving the result with a second preview.

**Architecture:** The program already declares these resources, so a preview reports them as creates and `steps[].newState` is a complete `apitype.ResourceV3`. Injection copies `newState` verbatim, fills in `id` and `outputs` from the sidecar, validates the result with the engine's own `Snapshot.VerifyIntegrity`, and — in stack mode — imports it and re-previews, reverting to a backup if any injected URN reports an operation other than `same`.

**Tech Stack:** Go 1.24, cobra, `github.com/pulumi/pulumi/pkg/v3` (engine `deploy`/`stack` packages, already a direct dependency), `github.com/pulumi/pulumi/sdk/v3/go/auto` (Automation API), `pulumi-terraform-bridge/v3` (schema field info), testify.

**Spec:** `docs/superpowers/specs/2026-08-13-non-importable-state-injection-design.md`

## Global Constraints

- **Every JSON decode of state or preview output must use `json.Decoder` with `UseNumber()`.** Without it large integers (AWS account IDs) become `float64` and re-serialize as scientific notation, which Pulumi's state parser rejects. This is why `PatchState` decodes the way it does (`pkg/state_patcher.go:696`).
- **Never write a placeholder value into state.** The digest writes `(sensitive)` for sensitive Terraform attributes; `preview --json` writes the literal string `[secret]` for secret inputs. Both must be resolved from stack config, or the command fails.
- **`pulumi preview` reporting zero operations is the only acceptance signal.** `pulumi refresh` reporting "unchanged" is not, and must never be used as one.
- Existing behaviour of `patch-state tf` must not change when `--non-importable` is absent.
- Licence header (Apache 2.0, "Copyright 2016-2025, Pulumi Corporation.") at the top of every new `.go` file, copied from any existing file in the package.
- Package for all new library code is `pkg`; command code is `cmd`.

---

### Task 1: Parse `pulumi preview --json`

**Files:**
- Create: `pkg/preview.go`
- Create: `pkg/preview_test.go`
- Create: `pkg/testdata/preview_create.json`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `type PreviewKey struct { Type, Name string }`
  - `type PreviewStep struct { Op string; URN string; NewState map[string]interface{} }`
  - `type PreviewDigest struct { Steps []PreviewStep; ChangeSummary map[string]int }`
  - `func ParsePreviewJSON(data []byte) (*PreviewDigest, error)`
  - `func (d *PreviewDigest) CreatesByTypeName() (map[PreviewKey]map[string]interface{}, error)`
  - `func (d *PreviewDigest) OpsByURN() map[string]string`

Do **not** name the key type `typeNameKey` — that identifier already exists in `pkg/import_filler.go:316` and would collide.

- [ ] **Step 1: Write the fixture**

Create `pkg/testdata/preview_create.json`. This is the shape `pulumi preview --json` emits (`previewDigest` in `pulumi/pkg/v3/display/json.go`), trimmed to what we consume:

```json
{
    "steps": [
        {
            "op": "create",
            "urn": "urn:pulumi:dev::proj::aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation::prop0",
            "newState": {
                "urn": "urn:pulumi:dev::proj::aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation::prop0",
                "custom": true,
                "type": "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation",
                "inputs": { "routeTableId": "rtb-0e370d1fdde0890b3", "vpnGatewayId": "vgw-0cdee3deb918b1983" },
                "parent": "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev",
                "provider": "urn:pulumi:dev::proj::pulumi:providers:aws::default_7_24_0::9f4c2b1e-0000-4000-8000-000000000001",
                "dependencies": [
                    "urn:pulumi:dev::proj::aws:ec2/routeTable:RouteTable::rt0",
                    "urn:pulumi:dev::proj::aws:ec2/vpnGateway:VpnGateway::vgw"
                ],
                "propertyDependencies": {
                    "routeTableId": ["urn:pulumi:dev::proj::aws:ec2/routeTable:RouteTable::rt0"]
                }
            }
        },
        {
            "op": "same",
            "urn": "urn:pulumi:dev::proj::aws:ec2/routeTable:RouteTable::rt0",
            "newState": {
                "urn": "urn:pulumi:dev::proj::aws:ec2/routeTable:RouteTable::rt0",
                "custom": true,
                "type": "aws:ec2/routeTable:RouteTable",
                "id": "rtb-0e370d1fdde0890b3",
                "inputs": { "vpcId": "vpc-01234567890abcdef", "ownerId": 52848974346 }
            }
        }
    ],
    "changeSummary": { "create": 1, "same": 1 }
}
```

The `ownerId` value is deliberately a large integer — it is what proves the `UseNumber` requirement.

- [ ] **Step 2: Write the failing tests**

Create `pkg/preview_test.go`:

```go
package pkg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loadPreviewFixture(t *testing.T) *PreviewDigest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "preview_create.json"))
	require.NoError(t, err)
	d, err := ParsePreviewJSON(data)
	require.NoError(t, err)
	return d
}

func TestParsePreviewJSON_CreatesByTypeName(t *testing.T) {
	t.Parallel()
	d := loadPreviewFixture(t)

	creates, err := d.CreatesByTypeName()
	require.NoError(t, err)

	// Only the create step is collected; the "same" step is ignored.
	require.Len(t, creates, 1)

	key := PreviewKey{
		Type: "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation",
		Name: "prop0",
	}
	state, ok := creates[key]
	require.True(t, ok, "create step should be keyed by type and name")

	assert.Equal(t,
		"urn:pulumi:dev::proj::pulumi:providers:aws::default_7_24_0::9f4c2b1e-0000-4000-8000-000000000001",
		state["provider"])
	assert.Equal(t, "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev", state["parent"])

	deps, ok := state["dependencies"].([]interface{})
	require.True(t, ok, "dependencies should be carried through verbatim")
	assert.Len(t, deps, 2)

	propDeps, ok := state["propertyDependencies"].(map[string]interface{})
	require.True(t, ok, "propertyDependencies should be carried through verbatim")
	assert.Contains(t, propDeps, "routeTableId")
}

func TestParsePreviewJSON_PreservesLargeIntegers(t *testing.T) {
	t.Parallel()
	d := loadPreviewFixture(t)

	// The "same" step carries a large integer. Decoding without UseNumber turns
	// it into a float64 that re-serializes as scientific notation, which the
	// Pulumi state parser rejects.
	var sameState map[string]interface{}
	for _, s := range d.Steps {
		if s.Op == "same" {
			sameState = s.NewState
		}
	}
	require.NotNil(t, sameState)

	inputs := sameState["inputs"].(map[string]interface{})
	num, ok := inputs["ownerId"].(json.Number)
	require.True(t, ok, "numbers must decode as json.Number, got %T", inputs["ownerId"])
	assert.Equal(t, "52848974346", num.String())
}

func TestPreviewDigest_OpsByURN(t *testing.T) {
	t.Parallel()
	d := loadPreviewFixture(t)

	ops := d.OpsByURN()
	assert.Equal(t, "create",
		ops["urn:pulumi:dev::proj::aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation::prop0"])
	assert.Equal(t, "same", ops["urn:pulumi:dev::proj::aws:ec2/routeTable:RouteTable::rt0"])
	assert.Equal(t, map[string]int{"create": 1, "same": 1}, d.ChangeSummary)
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./pkg/ -run TestParsePreviewJSON -v`
Expected: FAIL — `undefined: ParsePreviewJSON`, `undefined: PreviewDigest`, `undefined: PreviewKey`.

- [ ] **Step 4: Write the implementation**

Create `pkg/preview.go`:

```go
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
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// PreviewKey identifies a resource by Pulumi type token and resource name. It
// is how a sidecar entry is matched to the preview step that describes it.
type PreviewKey struct {
	Type string
	Name string
}

// PreviewStep is one step from "pulumi preview --json". NewState is kept as a
// raw map rather than apitype.ResourceV3 so that every field is carried through
// verbatim — including ones this tool does not interpret — and so that numbers
// survive as json.Number.
type PreviewStep struct {
	Op       string                 `json:"op"`
	URN      string                 `json:"urn"`
	NewState map[string]interface{} `json:"newState"`
}

// PreviewDigest is the "pulumi preview --json" document. It mirrors the
// previewDigest type in pulumi/pkg/v3/display/json.go, decoding only the fields
// this tool consumes.
type PreviewDigest struct {
	Steps         []PreviewStep  `json:"steps"`
	ChangeSummary map[string]int `json:"changeSummary"`
}

// ParsePreviewJSON decodes "pulumi preview --json" output.
//
// UseNumber is required: preview steps carry resource inputs, and without it a
// large integer such as an AWS account ID becomes a float64 that re-serializes
// as scientific notation, which Pulumi's state parser rejects.
func ParsePreviewJSON(data []byte) (*PreviewDigest, error) {
	var digest PreviewDigest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&digest); err != nil {
		return nil, fmt.Errorf("parsing preview JSON: %w", err)
	}
	return &digest, nil
}

// CreatesByTypeName indexes every create step by Pulumi type and resource name.
// Resources the program would create are the ones injection can supply state
// for; every other operation is ignored.
func (d *PreviewDigest) CreatesByTypeName() (map[PreviewKey]map[string]interface{}, error) {
	result := make(map[PreviewKey]map[string]interface{})
	for _, step := range d.Steps {
		if step.Op != "create" || step.NewState == nil {
			continue
		}
		urn, _ := step.NewState["urn"].(string)
		if urn == "" {
			urn = step.URN
		}
		typ, name, err := splitURN(urn)
		if err != nil {
			return nil, err
		}
		key := PreviewKey{Type: typ, Name: name}
		if _, dup := result[key]; dup {
			return nil, fmt.Errorf("preview contains two create steps for %s %q", typ, name)
		}
		result[key] = step.NewState
	}
	return result, nil
}

// OpsByURN maps each step's URN to its operation, for checking that injected
// resources report "same" on the verifying preview.
func (d *PreviewDigest) OpsByURN() map[string]string {
	ops := make(map[string]string, len(d.Steps))
	for _, step := range d.Steps {
		ops[step.URN] = step.Op
	}
	return ops
}

// splitURN extracts the Pulumi type token and resource name from a URN of the
// form urn:pulumi:<stack>::<project>::<qualifiedType>::<name>. The qualified
// type may name a chain of parents separated by "$"; the resource's own type is
// the last element.
func splitURN(urn string) (string, string, error) {
	parts := strings.Split(urn, "::")
	if len(parts) < 4 {
		return "", "", fmt.Errorf("malformed URN %q", urn)
	}
	name := parts[len(parts)-1]
	qualifiedType := parts[len(parts)-2]
	typ := qualifiedType
	if idx := strings.LastIndex(qualifiedType, "$"); idx >= 0 {
		typ = qualifiedType[idx+1:]
	}
	return typ, name, nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./pkg/ -run 'TestParsePreviewJSON|TestPreviewDigest' -v`
Expected: PASS (3 tests).

- [ ] **Step 6: Commit**

```bash
git add pkg/preview.go pkg/preview_test.go pkg/testdata/preview_create.json
git commit -m "feat(preview): parse pulumi preview --json into create steps"
```

---

### Task 2: Load the sidecar and map Terraform attributes to Pulumi names

**Files:**
- Create: `pkg/non_importable_file.go`
- Create: `pkg/non_importable_file_test.go`
- Modify: `cmd/non_importable.go:48-56` (use the shared type when writing)

**Interfaces:**
- Consumes: `NonImportableResource` (`pkg/import_filler.go:52`), `GetSchemaFieldInfo` (`pkg/schema_fields.go:72`), `snakeToCamel` (`pkg/state_patcher.go:1484`).
- Produces:
  - `type NonImportableFile struct { Comment string; Resources []NonImportableResource }`
  - `func LoadNonImportableFile(path string) (*NonImportableFile, error)`
  - `func MapTFAttributesToPulumi(attrs map[string]interface{}, fields map[string]*SchemaFieldInfo) map[string]interface{}`
  - `func PulumiToTFNames(fields map[string]*SchemaFieldInfo) map[string]string`

`PulumiToTFNames` is needed by Task 3 to go from a Pulumi input name back to the Terraform attribute name, which is how a `[secret]` input finds its `redactedAttributes` entry.

- [ ] **Step 1: Write the failing tests**

Create `pkg/non_importable_file_test.go`:

```go
package pkg

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadNonImportableFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "imports-ready.non-importable.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"_comment": "…",
		"resources": [
			{
				"type": "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation",
				"name": "prop0",
				"terraformAddress": "aws_vpn_gateway_route_propagation.prop[0]",
				"id": "vgw-0cdee3deb918b1983_rtb-0e370d1fdde0890b3",
				"attributes": {"route_table_id": "rtb-0e370d1fdde0890b3", "owner_id": 52848974346},
				"redactedAttributes": {"shared_key": "route_shared_key"}
			}
		]
	}`), 0o600))

	f, err := LoadNonImportableFile(path)
	require.NoError(t, err)
	require.Len(t, f.Resources, 1)

	r := f.Resources[0]
	assert.Equal(t, "prop0", r.Name)
	assert.Equal(t, "vgw-0cdee3deb918b1983_rtb-0e370d1fdde0890b3", r.ID)
	assert.Equal(t, "route_shared_key", r.RedactedAttributes["shared_key"])

	// Large integers must survive as json.Number, not float64.
	assert.Equal(t, "52848974346", r.Attributes["owner_id"].(json.Number).String())
}

func TestMapTFAttributesToPulumi(t *testing.T) {
	t.Parallel()
	fields := map[string]*SchemaFieldInfo{
		"route_table_id": {TFName: "route_table_id", PulumiName: "routeTableId"},
		"vpn_gateway_id": {TFName: "vpn_gateway_id", PulumiName: "vpnGatewayId"},
	}
	attrs := map[string]interface{}{
		"route_table_id": "rtb-1",
		"vpn_gateway_id": "vgw-1",
		// Not in the schema — must still be carried, camelCased, never dropped.
		"custom_thing": "kept",
	}

	got := MapTFAttributesToPulumi(attrs, fields)

	assert.Equal(t, "rtb-1", got["routeTableId"])
	assert.Equal(t, "vgw-1", got["vpnGatewayId"])
	assert.Equal(t, "kept", got["customThing"])
	assert.NotContains(t, got, "route_table_id")
}

func TestPulumiToTFNames(t *testing.T) {
	t.Parallel()
	fields := map[string]*SchemaFieldInfo{
		"shared_key": {TFName: "shared_key", PulumiName: "sharedKey"},
	}
	assert.Equal(t, map[string]string{"sharedKey": "shared_key"}, PulumiToTFNames(fields))
}
```

Add `"encoding/json"` to the test file's imports.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./pkg/ -run 'TestLoadNonImportableFile|TestMapTFAttributes|TestPulumiToTFNames' -v`
Expected: FAIL — `undefined: LoadNonImportableFile`, `undefined: MapTFAttributesToPulumi`, `undefined: PulumiToTFNames`.

- [ ] **Step 3: Write the implementation**

Create `pkg/non_importable_file.go` (with the standard licence header):

```go
package pkg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// NonImportableFile is the sidecar "resolve tf" writes beside the import file,
// recording the resources it left out because their Terraform type declares no
// importer.
type NonImportableFile struct {
	Comment   string                  `json:"_comment,omitempty"`
	Resources []NonImportableResource `json:"resources"`
}

// LoadNonImportableFile reads a sidecar written by "resolve tf".
//
// UseNumber keeps large integer attributes exact; they are written into state
// unchanged, where a float64 would re-serialize as scientific notation.
func LoadNonImportableFile(path string) (*NonImportableFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading non-importable file: %w", err)
	}
	var f NonImportableFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &f, nil
}

// MapTFAttributesToPulumi renames Terraform attributes to their Pulumi property
// names using the provider schema. An attribute the schema does not describe is
// camelCased rather than dropped: losing state values silently is worse than
// carrying one under a best-guess name.
func MapTFAttributesToPulumi(
	attrs map[string]interface{},
	fields map[string]*SchemaFieldInfo,
) map[string]interface{} {
	result := make(map[string]interface{}, len(attrs))
	for tfName, value := range attrs {
		name := snakeToCamel(tfName)
		if fi, ok := fields[tfName]; ok && fi.PulumiName != "" {
			name = fi.PulumiName
		}
		result[name] = value
	}
	return result
}

// PulumiToTFNames inverts the schema's name mapping, so a Pulumi property name
// can be traced back to the Terraform attribute it came from.
func PulumiToTFNames(fields map[string]*SchemaFieldInfo) map[string]string {
	result := make(map[string]string, len(fields))
	for tfName, fi := range fields {
		if fi != nil && fi.PulumiName != "" {
			result[fi.PulumiName] = tfName
		}
	}
	return result
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./pkg/ -run 'TestLoadNonImportableFile|TestMapTFAttributes|TestPulumiToTFNames' -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Use the shared type in the writer**

In `cmd/non_importable.go`, replace the anonymous struct at lines 48-56 with the shared type so writer and reader cannot drift:

```go
	doc := pkg.NonImportableFile{
		Comment: "Resources whose Terraform type declares no importer. They were omitted from the " +
			"import file because importing them always fails. Do not simply let them be created: " +
			"write them into the stack's state instead.",
		Resources: resources,
	}
```

- [ ] **Step 6: Run the full package tests**

Run: `go test ./... 2>&1 | tail -20`
Expected: PASS — in particular `cmd`'s existing `non_importable_test.go` must still pass, proving the JSON shape is unchanged.

- [ ] **Step 7: Commit**

```bash
git add pkg/non_importable_file.go pkg/non_importable_file_test.go cmd/non_importable.go
git commit -m "feat(inject): load the non-importable sidecar and map TF names to Pulumi names"
```

---

### Task 3: Verify a deployment with the engine's integrity check

**Files:**
- Create: `pkg/state_verify.go`
- Create: `pkg/state_verify_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `func VerifyDeploymentIntegrity(stateData []byte) error`

`stateData` is a full `pulumi stack export` document — `{"version": 3, "deployment": {...}}`. This runs the same check the CLI runs on `stack import`, in-process and before anything is written, so a malformed provider reference or a missing parent is caught offline.

Deserializing here is for validation only. The bytes that get written are always the raw `UseNumber`-decoded map from Task 4 — never a re-serialization of what this function builds, which would lose number fidelity.

- [ ] **Step 1: Write the failing tests**

Create `pkg/state_verify_test.go`:

```go
package pkg

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// minimalState builds a stack export containing a stack resource, an aws
// provider, and one custom resource whose provider reference is supplied by the
// caller so tests can break it.
func minimalState(providerRef string) []byte {
	return []byte(fmt.Sprintf(`{
	  "version": 3,
	  "deployment": {
	    "manifest": {},
	    "resources": [
	      {
	        "urn": "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev",
	        "type": "pulumi:pulumi:Stack"
	      },
	      {
	        "urn": "urn:pulumi:dev::proj::pulumi:providers:aws::default_7_24_0",
	        "type": "pulumi:providers:aws",
	        "custom": true,
	        "id": "9f4c2b1e-0000-4000-8000-000000000001"
	      },
	      {
	        "urn": "urn:pulumi:dev::proj::aws:ec2/routeTable:RouteTable::rt0",
	        "type": "aws:ec2/routeTable:RouteTable",
	        "custom": true,
	        "id": "rtb-1",
	        "parent": "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev",
	        "provider": %q
	      }
	    ]
	  }
	}`, providerRef))
}

const goodProviderRef = "urn:pulumi:dev::proj::pulumi:providers:aws::default_7_24_0::" +
	"9f4c2b1e-0000-4000-8000-000000000001"

func TestVerifyDeploymentIntegrity_Valid(t *testing.T) {
	t.Parallel()
	require.NoError(t, VerifyDeploymentIntegrity(minimalState(goodProviderRef)))
}

func TestVerifyDeploymentIntegrity_EmptyProviderRef(t *testing.T) {
	t.Parallel()
	// This is the failure reported in issue #11: an injected resource whose
	// provider reference was never filled in.
	err := VerifyDeploymentIntegrity(minimalState(""))
	require.Error(t, err)
}

func TestVerifyDeploymentIntegrity_UnknownProvider(t *testing.T) {
	t.Parallel()
	// Well-formed reference naming a provider that is not in the snapshot —
	// what copying a provider ref from the wrong stack produces.
	err := VerifyDeploymentIntegrity(minimalState(
		"urn:pulumi:dev::proj::pulumi:providers:aws::default_7_24_0::" +
			"00000000-dead-4000-8000-000000000000"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown provider")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./pkg/ -run TestVerifyDeploymentIntegrity -v`
Expected: FAIL — `undefined: VerifyDeploymentIntegrity`.

- [ ] **Step 3: Write the implementation**

Create `pkg/state_verify.go` (with the licence header). Note `stack.DeserializeDeploymentV3` needs a secrets provider; `stack.DefaultSecretsProvider` handles the common cases and is only used to read, not to write:

```go
package pkg

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pulumi/pulumi/pkg/v3/resource/deploy"
	"github.com/pulumi/pulumi/pkg/v3/resource/stack"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
)

// VerifyDeploymentIntegrity runs the engine's own snapshot integrity check over
// an exported deployment, so structural mistakes are caught before the file is
// written or imported rather than by the CLI afterwards.
//
// It rejects a resource missing its URN or type, a "custom: false" resource
// carrying an ID, a provider reference that does not parse or that names a
// provider absent from the snapshot, and a parent or dependency that is missing
// or ordered after the resource that refers to it.
func VerifyDeploymentIntegrity(stateData []byte) error {
	var untyped apitype.UntypedDeployment
	if err := json.Unmarshal(stateData, &untyped); err != nil {
		return fmt.Errorf("parsing state for verification: %w", err)
	}

	var deployment apitype.DeploymentV3
	if err := json.Unmarshal(untyped.Deployment, &deployment); err != nil {
		return fmt.Errorf("parsing deployment for verification: %w", err)
	}

	snap, err := stack.DeserializeDeploymentV3(
		context.Background(), deployment, stack.DefaultSecretsProvider)
	if err != nil {
		return fmt.Errorf("deserializing deployment for verification: %w", err)
	}

	if err := snap.VerifyIntegrity(); err != nil {
		return fmt.Errorf("state integrity check failed: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./pkg/ -run TestVerifyDeploymentIntegrity -v`
Expected: PASS (3 tests).

If `DeserializeDeploymentV3` reports a version or signature problem on the fixture, add `"version": 3` handling by populating `untyped.Version` — do not weaken the assertions. If the manifest's magic cookie is checked and the fixture's empty manifest fails, that is a real finding: record it and set the fixture's manifest to the value an actual `pulumi stack export` produces.

- [ ] **Step 5: Commit**

```bash
git add pkg/state_verify.go pkg/state_verify_test.go
git commit -m "feat(inject): verify deployments with the engine's snapshot integrity check"
```

---

### Task 4: Inject sidecar resources into a deployment

**Files:**
- Create: `pkg/state_injector.go`
- Create: `pkg/state_injector_test.go`

**Interfaces:**
- Consumes: `PreviewKey`, `PreviewDigest.CreatesByTypeName` (Task 1); `NonImportableFile`, `MapTFAttributesToPulumi`, `PulumiToTFNames` (Task 2); `VerifyDeploymentIntegrity` (Task 3); `GetSchemaFieldInfo`, `LookupProviderForPulumiType`, `BuildPulumiToTFTypeMap` (`pkg/schema_fields.go`); `ProviderWithMetadata`, `providermap.TerraformProviderName`.
- Produces:
  - `type InjectResult struct { Injected int; SecretsResolved int; Skipped []string }`
  - `func InjectNonImportable(stateData []byte, sidecar *NonImportableFile, preview *PreviewDigest, providers map[providermap.TerraformProviderName]*ProviderWithMetadata, configSecrets map[string]string) ([]byte, *InjectResult, error)`

**Rules this task implements, from the spec:**

1. Each sidecar entry matches exactly one preview create, keyed by `PreviewKey{Type, Name}`. No match or a duplicate is a hard error naming both sides.
2. The injected object is `newState` copied verbatim, plus `custom: true`, `id` from the sidecar, and `outputs` from the mapped sidecar attributes.
3. `inputs` come from `newState.inputs`. Any value equal to the literal `[secret]` is resolved from `configSecrets` via the sidecar's `redactedAttributes`; if it cannot be resolved, the whole call fails.
4. `__defaults` is added as an empty array **only if absent** — for bridged providers the engine's `Check` has usually already put one in `newState.inputs`, and overwriting it would discard real information.
5. `__pulumi_raw_state_delta` and `__meta` are never written — the schema mock this tool loads exposes neither an instance state nor a trustworthy schema version, so both would have to be fabricated. See the spec's Findings.
6. Injected entries are appended in dependency order: an entry that depends on another injected entry comes after it.
7. The result is passed through `VerifyDeploymentIntegrity` before being returned.

- [ ] **Step 1: Write the failing tests**

Create `pkg/state_injector_test.go`. `injectableState` reuses the shape from Task 3 with the provider present:

```go
package pkg

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func propagationSidecar() *NonImportableFile {
	return &NonImportableFile{
		Resources: []NonImportableResource{
			{
				Type:             "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation",
				Name:             "prop0",
				TerraformAddress: "aws_vpn_gateway_route_propagation.prop[0]",
				ID:               "vgw-0cdee3deb918b1983_rtb-0e370d1fdde0890b3",
				Attributes: map[string]interface{}{
					"route_table_id": "rtb-0e370d1fdde0890b3",
					"vpn_gateway_id": "vgw-0cdee3deb918b1983",
				},
			},
		},
	}
}

func propagationPreview(t *testing.T) *PreviewDigest {
	t.Helper()
	d, err := ParsePreviewJSON([]byte(`{
	  "steps": [
	    {
	      "op": "create",
	      "urn": "urn:pulumi:dev::proj::aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation::prop0",
	      "newState": {
	        "urn": "urn:pulumi:dev::proj::aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation::prop0",
	        "type": "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation",
	        "custom": true,
	        "inputs": {"routeTableId": "rtb-0e370d1fdde0890b3", "vpnGatewayId": "vgw-0cdee3deb918b1983"},
	        "parent": "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev",
	        "provider": "urn:pulumi:dev::proj::pulumi:providers:aws::default_7_24_0::9f4c2b1e-0000-4000-8000-000000000001",
	        "protect": true,
	        "dependencies": ["urn:pulumi:dev::proj::aws:ec2/routeTable:RouteTable::rt0"]
	      }
	    }
	  ],
	  "changeSummary": {"create": 1}
	}`))
	require.NoError(t, err)
	return d
}

// injected pulls the last resource out of an injected state document.
func injected(t *testing.T, out []byte) map[string]interface{} {
	t.Helper()
	var doc map[string]interface{}
	require.NoError(t, json.Unmarshal(out, &doc))
	resources := doc["deployment"].(map[string]interface{})["resources"].([]interface{})
	return resources[len(resources)-1].(map[string]interface{})
}

func TestInjectNonImportable_CopiesPreviewMetadata(t *testing.T) {
	t.Parallel()
	out, result, err := InjectNonImportable(
		minimalState(goodProviderRef), propagationSidecar(), propagationPreview(t), nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Injected)

	r := injected(t, out)
	assert.Equal(t,
		"urn:pulumi:dev::proj::aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation::prop0",
		r["urn"])
	assert.Equal(t, true, r["custom"])
	assert.Equal(t, "vgw-0cdee3deb918b1983_rtb-0e370d1fdde0890b3", r["id"])
	assert.Equal(t, "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev", r["parent"])
	assert.Equal(t,
		"urn:pulumi:dev::proj::pulumi:providers:aws::default_7_24_0::9f4c2b1e-0000-4000-8000-000000000001",
		r["provider"])
	assert.Equal(t, true, r["protect"])
	assert.Equal(t, []interface{}{"urn:pulumi:dev::proj::aws:ec2/routeTable:RouteTable::rt0"},
		r["dependencies"])
}

func TestInjectNonImportable_OutputsFromSidecarInputsFromProgram(t *testing.T) {
	t.Parallel()
	out, _, err := InjectNonImportable(
		minimalState(goodProviderRef), propagationSidecar(), propagationPreview(t), nil, nil)
	require.NoError(t, err)

	r := injected(t, out)

	// Outputs come from the sidecar's Terraform attributes, renamed. With no
	// provider schema loaded the camelCase fallback applies.
	outputs := r["outputs"].(map[string]interface{})
	assert.Equal(t, "rtb-0e370d1fdde0890b3", outputs["routeTableId"])
	assert.Equal(t, "vgw-0cdee3deb918b1983", outputs["vpnGatewayId"])

	// Inputs come from the program, so preview diffs them to nothing.
	inputs := r["inputs"].(map[string]interface{})
	assert.Equal(t, "rtb-0e370d1fdde0890b3", inputs["routeTableId"])
	assert.Equal(t, []interface{}{}, inputs["__defaults"])

	// The raw state delta is never synthesized.
	assert.NotContains(t, r, "__pulumi_raw_state_delta")
	assert.NotContains(t, outputs, "__pulumi_raw_state_delta")
}

func TestInjectNonImportable_NoMatchingCreateFails(t *testing.T) {
	t.Parallel()
	sidecar := propagationSidecar()
	sidecar.Resources[0].Name = "not-in-the-program"

	_, _, err := InjectNonImportable(
		minimalState(goodProviderRef), sidecar, propagationPreview(t), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not-in-the-program")
}

func TestInjectNonImportable_UnresolvedSecretFails(t *testing.T) {
	t.Parallel()
	preview := propagationPreview(t)
	preview.Steps[0].NewState["inputs"].(map[string]interface{})["sharedKey"] = "[secret]"

	sidecar := propagationSidecar()
	sidecar.Resources[0].RedactedAttributes = map[string]string{"shared_key": "route_shared_key"}

	// No config secrets supplied: the placeholder cannot be resolved, so the
	// command must fail rather than write "[secret]" into state.
	_, _, err := InjectNonImportable(
		minimalState(goodProviderRef), sidecar, preview, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "route_shared_key")
}

func TestInjectNonImportable_ResolvesSecretFromConfig(t *testing.T) {
	t.Parallel()
	preview := propagationPreview(t)
	preview.Steps[0].NewState["inputs"].(map[string]interface{})["sharedKey"] = "[secret]"

	sidecar := propagationSidecar()
	sidecar.Resources[0].RedactedAttributes = map[string]string{"shared_key": "route_shared_key"}

	out, result, err := InjectNonImportable(
		minimalState(goodProviderRef), sidecar, preview, nil,
		map[string]string{"route_shared_key": "hunter2"})
	require.NoError(t, err)
	assert.Equal(t, 1, result.SecretsResolved)

	inputs := injected(t, out)["inputs"].(map[string]interface{})
	envelope, ok := inputs["sharedKey"].(map[string]interface{})
	require.True(t, ok, "secret must be written inside Pulumi's secret envelope")
	assert.Equal(t, "1b47061264138c4ac30d75fd1eb44270", envelope["4dabf18193072939515e22adb298388d"])
	assert.Equal(t, `"hunter2"`, envelope["plaintext"])
}

func TestInjectNonImportable_OutputPassesIntegrityCheck(t *testing.T) {
	t.Parallel()
	out, _, err := InjectNonImportable(
		minimalState(goodProviderRef), propagationSidecar(), propagationPreview(t), nil, nil)
	require.NoError(t, err)
	require.NoError(t, VerifyDeploymentIntegrity(out))
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./pkg/ -run TestInjectNonImportable -v`
Expected: FAIL — `undefined: InjectNonImportable`.

- [ ] **Step 3: Write the implementation**

Create `pkg/state_injector.go` (with the licence header):

```go
package pkg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg/providermap"
)

// secretPlaceholder is what "pulumi preview --json" substitutes for a secret
// input value (MassageSecrets in pulumi/pkg/v3/backend/display/json.go). It must
// never reach state.
const secretPlaceholder = "[secret]"

// InjectResult reports what injection did, for the command's summary output.
type InjectResult struct {
	Injected        int
	SecretsResolved int
	Skipped         []string
}

// InjectNonImportable appends the sidecar's resources to an exported deployment.
//
// Everything but the resource ID and outputs comes from the program: a preview
// reports these resources as creates, and each create's newState carries the
// URN, parent, provider reference, protect flag, inputs, and dependency edges
// the engine computed. Copying that is what makes injection correct without
// inferring anything from the deployment.
func InjectNonImportable(
	stateData []byte,
	sidecar *NonImportableFile,
	preview *PreviewDigest,
	providers map[providermap.TerraformProviderName]*ProviderWithMetadata,
	configSecrets map[string]string,
) ([]byte, *InjectResult, error) {
	if sidecar == nil || len(sidecar.Resources) == 0 {
		return stateData, &InjectResult{}, nil
	}
	if preview == nil {
		return nil, nil, fmt.Errorf("injection requires a preview: run \"pulumi preview --json\"")
	}

	var state map[string]interface{}
	dec := json.NewDecoder(bytes.NewReader(stateData))
	dec.UseNumber()
	if err := dec.Decode(&state); err != nil {
		return nil, nil, fmt.Errorf("parsing state: %w", err)
	}

	deployment, ok := state["deployment"].(map[string]interface{})
	if !ok {
		return nil, nil, fmt.Errorf("state missing deployment")
	}
	resources, ok := deployment["resources"].([]interface{})
	if !ok {
		return nil, nil, fmt.Errorf("state missing resources")
	}

	creates, err := preview.CreatesByTypeName()
	if err != nil {
		return nil, nil, err
	}

	typeMap := BuildPulumiToTFTypeMap(providers)
	result := &InjectResult{}

	built := make([]map[string]interface{}, 0, len(sidecar.Resources))
	for i := range sidecar.Resources {
		r := &sidecar.Resources[i]
		newState, ok := creates[PreviewKey{Type: r.Type, Name: r.Name}]
		if !ok {
			return nil, nil, fmt.Errorf(
				"no create step in the preview matches %s %q (%s); the program must declare "+
					"this resource for its state to be injected",
				r.Type, r.Name, r.TerraformAddress)
		}

		obj, secrets, err := buildInjectedResource(r, newState, typeMap, providers, configSecrets)
		if err != nil {
			return nil, nil, err
		}
		result.SecretsResolved += secrets
		built = append(built, obj)
	}

	orderInjected(built)
	for _, obj := range built {
		resources = append(resources, obj)
		result.Injected++
	}
	deployment["resources"] = resources

	out, err := json.MarshalIndent(state, "", "    ")
	if err != nil {
		return nil, nil, fmt.Errorf("serializing injected state: %w", err)
	}
	if err := VerifyDeploymentIntegrity(out); err != nil {
		return nil, nil, err
	}
	return out, result, nil
}

// buildInjectedResource copies the preview's newState and fills in the parts
// only the sidecar knows. It returns the number of secret placeholders it
// resolved.
func buildInjectedResource(
	r *NonImportableResource,
	newState map[string]interface{},
	typeMap map[string]string,
	providers map[providermap.TerraformProviderName]*ProviderWithMetadata,
	configSecrets map[string]string,
) (map[string]interface{}, int, error) {
	obj := make(map[string]interface{}, len(newState)+3)
	for k, v := range newState {
		// The delta is written by the bridge on import. An injected resource has
		// none, and the bridge handles its absence by rebuilding Terraform state
		// from the property bag. Synthesizing one would be worse than omitting it.
		if k == "__pulumi_raw_state_delta" {
			continue
		}
		obj[k] = v
	}
	obj["custom"] = true
	obj["id"] = r.ID

	// Field info lets attributes be renamed with the provider's own mapping
	// rather than a camelCase guess. Absent a loaded schema the fallback applies.
	var fields map[string]*SchemaFieldInfo
	if prov, tfType, found := LookupProviderForPulumiType(r.Type, typeMap, providers); found {
		fields = GetSchemaFieldInfo(prov, tfType)
	}

	obj["outputs"] = MapTFAttributesToPulumi(r.Attributes, fields)

	inputs := map[string]interface{}{}
	if raw, ok := newState["inputs"].(map[string]interface{}); ok {
		for k, v := range raw {
			inputs[k] = v
		}
	}

	secrets, err := resolveSecretInputs(r, inputs, fields, configSecrets)
	if err != nil {
		return nil, 0, err
	}

	// __defaults records which properties came from schema defaults. The engine's
	// Check usually supplies it already; only add it when missing, since an empty
	// list would otherwise discard what Check worked out.
	if _, ok := inputs[reservedDefaultsKey]; !ok {
		inputs[reservedDefaultsKey] = []interface{}{}
	}
	obj["inputs"] = inputs

	return obj, secrets, nil
}

const reservedDefaultsKey = "__defaults"

// resolveSecretInputs replaces "[secret]" placeholders with the real value from
// stack config, wrapped in Pulumi's secret envelope. An unresolvable placeholder
// is a hard error: writing the placeholder would put a known-wrong value into
// state, which is worse than refusing to write anything.
func resolveSecretInputs(
	r *NonImportableResource,
	inputs map[string]interface{},
	fields map[string]*SchemaFieldInfo,
	configSecrets map[string]string,
) (int, error) {
	pulumiToTF := PulumiToTFNames(fields)
	resolved := 0

	names := make([]string, 0, len(inputs))
	for name := range inputs {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if s, ok := inputs[name].(string); !ok || s != secretPlaceholder {
			continue
		}

		tfName, ok := pulumiToTF[name]
		if !ok {
			tfName = normalizeTFName(name)
		}
		configKey, ok := r.RedactedAttributes[tfName]
		if !ok {
			return 0, fmt.Errorf(
				"%s %q input %q is a masked secret but the sidecar records no config key for "+
					"Terraform attribute %q; re-run \"resolve tf\" or set the value by hand",
				r.Type, r.Name, name, tfName)
		}
		value, ok := configSecrets[configKey]
		if !ok || value == "" {
			return 0, fmt.Errorf(
				"%s %q input %q needs the secret in stack config key %q, which is not set; "+
					"pass --project-dir and --stack so it can be read",
				r.Type, r.Name, name, configKey)
		}

		encoded, err := json.Marshal(value)
		if err != nil {
			return 0, fmt.Errorf("encoding secret for %s: %w", configKey, err)
		}
		inputs[name] = map[string]interface{}{
			"4dabf18193072939515e22adb298388d": "1b47061264138c4ac30d75fd1eb44270",
			"plaintext":                        string(encoded),
		}
		resolved++
	}
	return resolved, nil
}

// orderInjected sorts injected resources so that one depending on another comes
// after it. VerifyIntegrity rejects a resource whose dependency appears later in
// the array.
func orderInjected(objs []map[string]interface{}) {
	position := make(map[string]int, len(objs))
	for i, obj := range objs {
		if urn, ok := obj["urn"].(string); ok {
			position[urn] = i
		}
	}

	dependsOn := func(a, b map[string]interface{}) bool {
		deps, _ := a["dependencies"].([]interface{})
		urn, _ := b["urn"].(string)
		for _, d := range deps {
			if s, ok := d.(string); ok && s == urn {
				return true
			}
		}
		if parent, _ := a["parent"].(string); parent == urn {
			return true
		}
		return false
	}

	sort.SliceStable(objs, func(i, j int) bool {
		if dependsOn(objs[i], objs[j]) {
			return false
		}
		if dependsOn(objs[j], objs[i]) {
			return true
		}
		ui, _ := objs[i]["urn"].(string)
		uj, _ := objs[j]["urn"].(string)
		return strings.Compare(ui, uj) < 0
	})
}
```

`normalizeTFName` already exists at `pkg/state_patcher.go:1581` — do not redefine it.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./pkg/ -run TestInjectNonImportable -v`
Expected: PASS (6 tests).

- [ ] **Step 5: Add the ordering test**

Append to `pkg/state_injector_test.go`:

```go
func TestInjectNonImportable_OrdersDependenciesFirst(t *testing.T) {
	t.Parallel()
	// Two injected resources where the second depends on the first. The
	// dependency must be written earlier in the array: VerifyIntegrity rejects a
	// forward reference.
	preview, err := ParsePreviewJSON([]byte(`{
	  "steps": [
	    {
	      "op": "create",
	      "urn": "urn:pulumi:dev::proj::aws:ec2/vpnConnectionRoute:VpnConnectionRoute::route",
	      "newState": {
	        "urn": "urn:pulumi:dev::proj::aws:ec2/vpnConnectionRoute:VpnConnectionRoute::route",
	        "type": "aws:ec2/vpnConnectionRoute:VpnConnectionRoute",
	        "custom": true,
	        "inputs": {},
	        "provider": "urn:pulumi:dev::proj::pulumi:providers:aws::default_7_24_0::9f4c2b1e-0000-4000-8000-000000000001",
	        "dependencies": ["urn:pulumi:dev::proj::aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation::prop0"]
	      }
	    },
	    {
	      "op": "create",
	      "urn": "urn:pulumi:dev::proj::aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation::prop0",
	      "newState": {
	        "urn": "urn:pulumi:dev::proj::aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation::prop0",
	        "type": "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation",
	        "custom": true,
	        "inputs": {},
	        "provider": "urn:pulumi:dev::proj::pulumi:providers:aws::default_7_24_0::9f4c2b1e-0000-4000-8000-000000000001"
	      }
	    }
	  ]
	}`))
	require.NoError(t, err)

	sidecar := &NonImportableFile{Resources: []NonImportableResource{
		{
			Type: "aws:ec2/vpnConnectionRoute:VpnConnectionRoute",
			Name: "route", ID: "vpn-1_10.0.0.0/16",
			Attributes: map[string]interface{}{"destination_cidr_block": "10.0.0.0/16"},
		},
		{
			Type: "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation",
			Name: "prop0", ID: "vgw-1_rtb-1",
			Attributes: map[string]interface{}{"route_table_id": "rtb-1"},
		},
	}}

	out, result, err := InjectNonImportable(minimalState(goodProviderRef), sidecar, preview, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, result.Injected)

	var doc map[string]interface{}
	require.NoError(t, json.Unmarshal(out, &doc))
	resources := doc["deployment"].(map[string]interface{})["resources"].([]interface{})

	last := resources[len(resources)-1].(map[string]interface{})
	secondLast := resources[len(resources)-2].(map[string]interface{})
	assert.Contains(t, last["urn"], "VpnConnectionRoute", "dependent must come last")
	assert.Contains(t, secondLast["urn"], "VpnGatewayRoutePropagation", "dependency must come first")

	require.NoError(t, VerifyDeploymentIntegrity(out))
}
```

- [ ] **Step 6: Run the ordering test**

Run: `go test ./pkg/ -run TestInjectNonImportable_OrdersDependenciesFirst -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/state_injector.go pkg/state_injector_test.go
git commit -m "feat(inject): build injected state from preview create steps"
```

---

### Task 5: Wire `--non-importable` into `patch-state tf` (file mode)

**Files:**
- Modify: `cmd/patch_state_tf.go` (flags, provider loading, injection call, summary)
- Create: `cmd/patch_state_tf_inject_test.go`

**Interfaces:**
- Consumes: `LoadNonImportableFile`, `ParsePreviewJSON`, `InjectNonImportable` (Tasks 1–4); `PulumiProvidersForTerraformProviders` (`pkg/pulumi_providers.go:75`); `ModuleMap.Providers` (`pkg/module_map.go:117`, populated for exactly this purpose).
- Produces: `func loadProvidersForDigest(digest *pkg.ModuleMap) (map[providermap.TerraformProviderName]*pkg.ProviderWithMetadata, error)` in `cmd`.

New flags:

```
--non-importable <path>   sidecar from "resolve tf"; injects its resources
--preview-json <path>     output of "pulumi preview --json"; required with --non-importable in file mode
```

Injection runs **after** patching, on the patched bytes.

- [ ] **Step 1: Write the failing test**

Create `cmd/patch_state_tf_inject_test.go`:

```go
package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPatchStateTf_NonImportableRequiresPreview(t *testing.T) {
	t.Parallel()
	cmd := newPatchStateTfCmd()
	cmd.SetArgs([]string{
		"--state", "state.json",
		"--digest", "digest.json",
		"--fields", "fields.json",
		"--config-dir", ".",
		"--out", "out.json",
		"--non-importable", "imports-ready.non-importable.json",
	})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--preview-json")
	assert.Contains(t, err.Error(), "pulumi preview --json")
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/ -run TestPatchStateTf_NonImportableRequiresPreview -v`
Expected: FAIL — the command does not know `--non-importable`, so cobra reports `unknown flag`.

- [ ] **Step 3: Add the flags and the validation**

In `cmd/patch_state_tf.go`, add to the `var` block at the top of `newPatchStateTfCmd`:

```go
	var nonImportablePath string
	var previewJSONPath string
```

Register them next to the existing flags:

```go
	cmd.Flags().StringVar(&nonImportablePath, "non-importable", "",
		"Sidecar from \"resolve tf\" whose resources should be written into state")
	cmd.Flags().StringVar(&previewJSONPath, "preview-json", "",
		"Output of \"pulumi preview --json\", the source of injected resource metadata")
```

At the top of `RunE`, before any file is read:

```go
		if nonImportablePath != "" && previewJSONPath == "" {
			return fmt.Errorf("--non-importable requires --preview-json: injected resources take " +
				"their URN, parent, provider and dependencies from the program, so a preview is " +
				"needed; produce one with \"pulumi preview --json > preview.json\"")
		}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/ -run TestPatchStateTf_NonImportableRequiresPreview -v`
Expected: PASS.

- [ ] **Step 5: Add provider loading**

Append to `cmd/patch_state_tf.go`:

```go
// loadProvidersForDigest loads Pulumi provider schemas for the Terraform
// providers a digest records. "digest tf" stores those addresses precisely so
// downstream commands can do this without a Terraform state file.
func loadProvidersForDigest(
	digest *pkg.ModuleMap,
) (map[providermap.TerraformProviderName]*pkg.ProviderWithMetadata, error) {
	if len(digest.Providers) == 0 {
		return nil, nil
	}
	names := make([]providermap.TerraformProviderName, 0, len(digest.Providers))
	for addr := range digest.Providers {
		names = append(names, providermap.TerraformProviderName(addr))
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })

	providers, err := pkg.PulumiProvidersForTerraformProviders(names, nil)
	if err != nil {
		return nil, fmt.Errorf("loading provider schemas for property name mapping: %w", err)
	}
	return providers, nil
}
```

Add `"sort"` and `"github.com/pulumi-proserv/pulumi-tool-import/pkg/providermap"` to the imports.

- [ ] **Step 6: Call injection after patching**

In `RunE`, immediately after the existing `patched, result, err = pkg.PatchState(...)` block and before the output is written:

```go
			var injectResult *pkg.InjectResult
			if nonImportablePath != "" {
				sidecar, err := pkg.LoadNonImportableFile(nonImportablePath)
				if err != nil {
					return err
				}

				previewData, err := os.ReadFile(previewJSONPath)
				if err != nil {
					return fmt.Errorf("reading preview JSON: %w", err)
				}
				preview, err := pkg.ParsePreviewJSON(previewData)
				if err != nil {
					return err
				}

				providers, err := loadProvidersForDigest(&digest)
				if err != nil {
					return err
				}

				patched, injectResult, err = pkg.InjectNonImportable(
					patched, sidecar, preview, providers, configSecrets)
				if err != nil {
					return err
				}
			}
```

And after the existing statistics block:

```go
			if injectResult != nil {
				fmt.Fprintf(os.Stderr, "  Injected:           %d resources\n", injectResult.Injected)
				fmt.Fprintf(os.Stderr, "  Secrets resolved:   %d\n", injectResult.SecretsResolved)
				fmt.Fprintf(os.Stderr, "\nVerify with: pulumi stack import --file %s && pulumi preview\n"+
					"A correct injection previews as zero operations. Do not use \"pulumi refresh\" "+
					"to check: it reports these resources unchanged even when their values are wrong.\n",
					outPath)
			}
```

- [ ] **Step 7: Run all tests**

Run: `go test ./... 2>&1 | tail -20`
Expected: PASS.

- [ ] **Step 8: Run lint and vet**

Run: `make lint vet` (or `golangci-lint run ./... && go vet ./...` if the Makefile has no such targets)
Expected: clean.

- [ ] **Step 9: Commit**

```bash
git add cmd/patch_state_tf.go cmd/patch_state_tf_inject_test.go
git commit -m "feat(patch-state): add --non-importable and --preview-json"
```

---

### Task 6: Stack mode — export, inject, import, verify, revert

**Files:**
- Create: `pkg/state_stack.go`
- Create: `pkg/state_stack_test.go`
- Modify: `cmd/patch_state_tf.go` (mode selection)

**Interfaces:**
- Consumes: `PreviewDigest.OpsByURN` (Task 1).
- Produces:
  - `type StackSession struct { … }` with `func NewStackSession(ctx context.Context, projectDir, stackName string) (*StackSession, error)`
  - `func (s *StackSession) Export(ctx context.Context) ([]byte, error)`
  - `func (s *StackSession) Import(ctx context.Context, state []byte) error`
  - `func (s *StackSession) PreviewJSON(ctx context.Context) (*PreviewDigest, error)`
  - `func CheckInjectedOps(preview *PreviewDigest, injectedURNs []string) []string`

`CheckInjectedOps` is pure and carries the verification rule, so it is unit-testable without a stack. It returns a human-readable complaint per URN that reports anything other than `same`; an empty slice means the injection verified.

- [ ] **Step 1: Write the failing test for the decision function**

Create `pkg/state_stack_test.go`:

```go
package pkg

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckInjectedOps_AllSame(t *testing.T) {
	t.Parallel()
	preview, err := ParsePreviewJSON([]byte(`{"steps": [
		{"op": "same", "urn": "urn:pulumi:dev::proj::aws:ec2/x:X::a"},
		{"op": "same", "urn": "urn:pulumi:dev::proj::aws:ec2/y:Y::b"}
	]}`))
	require.NoError(t, err)

	problems := CheckInjectedOps(preview, []string{
		"urn:pulumi:dev::proj::aws:ec2/x:X::a",
		"urn:pulumi:dev::proj::aws:ec2/y:Y::b",
	})
	assert.Empty(t, problems)
}

func TestCheckInjectedOps_ReplaceIsAProblem(t *testing.T) {
	t.Parallel()
	preview, err := ParsePreviewJSON([]byte(`{"steps": [
		{"op": "replace", "urn": "urn:pulumi:dev::proj::aws:ec2/x:X::a"}
	]}`))
	require.NoError(t, err)

	problems := CheckInjectedOps(preview, []string{"urn:pulumi:dev::proj::aws:ec2/x:X::a"})
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0], "replace")
	assert.Contains(t, problems[0], "X::a")
}

func TestCheckInjectedOps_MissingFromPreviewIsAProblem(t *testing.T) {
	t.Parallel()
	// A URN absent from the preview means the resource is not in the program's
	// graph at all — injection put something in state that nothing declares.
	preview, err := ParsePreviewJSON([]byte(`{"steps": []}`))
	require.NoError(t, err)

	problems := CheckInjectedOps(preview, []string{"urn:pulumi:dev::proj::aws:ec2/x:X::a"})
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0], "no step")
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./pkg/ -run TestCheckInjectedOps -v`
Expected: FAIL — `undefined: CheckInjectedOps`.

- [ ] **Step 3: Write the implementation**

Create `pkg/state_stack.go` (with the licence header):

```go
package pkg

import (
	"context"
	"fmt"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
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

// Export returns the stack's current deployment, as "pulumi stack export" would.
func (s *StackSession) Export(ctx context.Context) ([]byte, error) {
	dep, err := s.stack.Export(ctx)
	if err != nil {
		return nil, fmt.Errorf("exporting stack: %w", err)
	}
	return dep.Deployment, nil
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
```

Add `"encoding/json"` and `"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"` to the imports.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./pkg/ -run TestCheckInjectedOps -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Wire stack mode into the command**

In `cmd/patch_state_tf.go`, `--state` and `--out` are currently required. Make them required only in file mode: remove `cmd.MarkFlagRequired("state")` and `cmd.MarkFlagRequired("out")`, and validate at the top of `RunE`:

```go
		stackMode := statePath == "" && outPath == ""
		if !stackMode && (statePath == "" || outPath == "") {
			return fmt.Errorf("file mode needs both --state and --out; " +
				"omit both to operate on the stack directly via --project-dir and --stack")
		}
		if stackMode && (projectDir == "" || stack == "") {
			return fmt.Errorf("stack mode needs --project-dir and --stack")
		}
		if stackMode && previewJSONPath != "" {
			return fmt.Errorf("--preview-json applies to file mode only; " +
				"stack mode runs the preview itself")
		}
```

Then add the stack-mode branch, replacing the direct `os.ReadFile(statePath)`:

```go
			var session *pkg.StackSession
			var backupPath string
			var stateData []byte

			if stackMode {
				ctx := cmd.Context()
				session, err = pkg.NewStackSession(ctx, projectDir, stack)
				if err != nil {
					return err
				}
				stateData, err = session.Export(ctx)
				if err != nil {
					return err
				}

				// Write the backup before anything mutates the stack, so an
				// interrupted run leaves a one-line recovery rather than a
				// reconstruction job.
				backupPath = fmt.Sprintf("%s-%s.backup.json", stack, time.Now().UTC().Format("20060102-150405"))
				if err := os.WriteFile(backupPath, stateData, 0o600); err != nil {
					return fmt.Errorf("writing backup: %w", err)
				}
				fmt.Fprintf(os.Stderr, "Stack state backed up to %s\n", backupPath)
				fmt.Fprintf(os.Stderr, "  Recover with: pulumi stack import --file %s\n", backupPath)
			} else {
				stateData, err = os.ReadFile(statePath)
				if err != nil {
					return fmt.Errorf("reading state file: %w", err)
				}
			}
```

Replace the existing `--preview-json` read so stack mode previews itself:

```go
				var preview *pkg.PreviewDigest
				if stackMode {
					preview, err = session.PreviewJSON(cmd.Context())
				} else {
					previewData, readErr := os.ReadFile(previewJSONPath)
					if readErr != nil {
						return fmt.Errorf("reading preview JSON: %w", readErr)
					}
					preview, err = pkg.ParsePreviewJSON(previewData)
				}
				if err != nil {
					return err
				}
```

And after injection, in stack mode only, import and verify:

```go
			if stackMode {
				ctx := cmd.Context()
				if err := session.Import(ctx, patched); err != nil {
					return err
				}

				verify, err := session.PreviewJSON(ctx)
				if err != nil {
					return fmt.Errorf("verifying preview failed; state is imported but unverified. "+
						"Restore with: pulumi stack import --file %s\n%w", backupPath, err)
				}

				problems := pkg.CheckInjectedOps(verify, injectedURNs)
				if len(problems) > 0 {
					fmt.Fprintf(os.Stderr, "\nInjection did not verify:\n")
					for _, p := range problems {
						fmt.Fprintf(os.Stderr, "  %s\n", p)
					}
					fmt.Fprintf(os.Stderr, "Restoring %s\n", backupPath)
					if err := session.Import(ctx, stateData); err != nil {
						return fmt.Errorf("restoring backup failed; restore by hand with: "+
							"pulumi stack import --file %s\n%w", backupPath, err)
					}
					return fmt.Errorf("injection reverted: %d resource(s) did not preview as unchanged",
						len(problems))
				}
				fmt.Fprintf(os.Stderr, "\nVerified: all %d injected resource(s) preview as unchanged.\n",
					len(injectedURNs))
			}
```

`injectedURNs` comes from the injection result — add `URNs []string` to `pkg.InjectResult` in `pkg/state_injector.go`, appending each object's `urn` where `result.Injected++` happens, and use `injectResult.URNs` here. Add `"time"` to the command's imports.

- [ ] **Step 6: Run all tests, lint, and vet**

Run: `go test ./... && make lint vet`
Expected: PASS and clean.

- [ ] **Step 7: Commit**

```bash
git add pkg/state_stack.go pkg/state_stack_test.go pkg/state_injector.go cmd/patch_state_tf.go
git commit -m "feat(patch-state): stack mode with backup, preview verification and revert"
```

---

### Task 7: Documentation and changelog

**Files:**
- Modify: `docs/non-importable-resources.md`
- Modify: `README.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Document the workflow**

In `docs/non-importable-resources.md`, replace the hand-crafting guidance with both modes. Include verbatim:

```bash
# Stack mode — the tool runs the previews, backs up, imports and verifies.
pulumi plugin run import -- patch-state tf \
  --digest tf-digest.json \
  --fields data/aws-import-diff-fields.json \
  --config-dir ./terraform \
  --non-importable imports-ready.non-importable.json \
  --project-dir . --stack dev

# File mode — offline; you run the pulumi steps.
pulumi preview --json > preview.json
pulumi stack export > state.json
pulumi plugin run import -- patch-state tf \
  --state state.json --out injected.json \
  --digest tf-digest.json \
  --fields data/aws-import-diff-fields.json \
  --config-dir ./terraform \
  --non-importable imports-ready.non-importable.json \
  --preview-json preview.json
pulumi stack import --file injected.json
pulumi preview   # must report zero operations
```

State plainly, in prose: a correct injection previews as zero operations, and `pulumi refresh` must not be used as the check — it reports these resources unchanged even when their values are wrong, because their `Read` re-derives attributes from the resource ID.

- [ ] **Step 2: Add the flags to the README**

Add `--non-importable` and `--preview-json` to the `patch-state tf` flag list, and note that omitting `--state`/`--out` operates on the stack directly.

- [ ] **Step 3: Roll the changelog**

Add under `## [Unreleased]`:

```markdown
### Added

- `patch-state tf --non-importable <sidecar>` writes resources that cannot be
  imported into the stack's state, closing the loop opened in v0.2.0 when
  `resolve tf` began detecting them (#22).
- `patch-state tf` gains a stack mode: given `--project-dir` and `--stack` with
  no `--state`/`--out`, it exports the deployment, writes a timestamped backup,
  patches and injects, imports the result, and verifies it with
  `pulumi preview`, restoring the backup if any injected resource does not
  preview as unchanged.
```

Do not add an entry for `digest tf` — this work leaves it unchanged.

- [ ] **Step 4: Commit**

```bash
git add docs/non-importable-resources.md README.md CHANGELOG.md
git commit -m "docs: document non-importable state injection"
```

---

## Verification Before Claiming Completion

Run and paste the real output; do not summarize:

```bash
go test ./... 2>&1 | tail -20
make lint vet
```

Then the end-to-end run, which is the acceptance criterion. Per the project's AWS testing notes, use the CE demo account and never a customer account, and note that `env -u AWS_PROFILE` is required:

```bash
export PULUMI_ACCESS_TOKEN=$JDAVENPORT_PULUMI_CORP_PULUMI_ACCESS_TOKEN
esc run team-ce/aws/pulumi-ce -- env -u AWS_PROFILE aws sts get-caller-identity   # expect 052848974346
```

Stand up the v0.2.0 fixture (VPC, three route tables, VPN gateway with three route propagations, customer gateway, VPN connection with a connection route), run `patch-state tf --non-importable` in stack mode, and confirm `pulumi preview` reports **zero operations**. `tofu apply`/`destroy` need the user to run them; VPN connections take 3–5 minutes each way, so run them in the background rather than in the foreground.
