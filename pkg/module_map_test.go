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
	"os"
	"path/filepath"
	"sort"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
	"github.com/pulumi/opentofu/addrs"
	"github.com/pulumi/opentofu/states"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestBuildModuleMap_WithoutEval(t *testing.T) {
	t.Parallel()
	tfDir, err := filepath.Abs(filepath.Join("testdata", "tf_indexed_modules"))
	require.NoError(t, err)

	config, err := LoadConfig(tfDir)
	require.NoError(t, err)

	rawState, err := LoadRawState(filepath.Join(tfDir, "terraform.tfstate"))
	require.NoError(t, err)

	// Build without eval (nil tofuCtx) — no pulumiProviders needed for URN
	// generation in this test since we just check structure.
	mm, err := BuildModuleMap(context.Background(), config, nil, rawState, nil, "test-stack", "test-project", nil)
	require.NoError(t, err)
	require.NotNil(t, mm)

	// Should have "pet[0]" and "pet[1]" entries.
	require.Contains(t, mm.Modules, "pet[0]")
	require.Contains(t, mm.Modules, "pet[1]")

	pet0 := mm.Modules["pet[0]"]
	assert.Equal(t, "module.pet[0]", pet0.TerraformPath)
	assert.Equal(t, "./modules/pet", pet0.Source)
	assert.Equal(t, "0", pet0.IndexKey)
	assert.Equal(t, "int", pet0.IndexType)

	pet1 := mm.Modules["pet[1]"]
	assert.Equal(t, "module.pet[1]", pet1.TerraformPath)
	assert.Equal(t, "1", pet1.IndexKey)

	// Resources should be populated (without provider mapping, URNs will be raw addresses).
	assert.Len(t, pet0.Resources, 1)
	assert.Equal(t, "managed", pet0.Resources[0].Mode)
	assert.Equal(t, "module.pet[0].random_pet.this", pet0.Resources[0].TranslatedURN) // falls back to address
	assert.Equal(t, "module.pet[0].random_pet.this", pet0.Resources[0].TerraformAddress)
	assert.Equal(t, "test-0-just-phoenix", pet0.Resources[0].ImportID)

	assert.Len(t, pet1.Resources, 1)
	assert.Equal(t, "managed", pet1.Resources[0].Mode)
	assert.Equal(t, "module.pet[1].random_pet.this", pet1.Resources[0].TerraformAddress)
	assert.Equal(t, "test-1-brief-jennet", pet1.Resources[0].ImportID)

	// Interface should be populated from config.
	require.NotNil(t, pet0.Interface)
	require.Len(t, pet0.Interface.Inputs, 1)
	assert.Equal(t, "prefix", pet0.Interface.Inputs[0].Name)
	assert.True(t, pet0.Interface.Inputs[0].Required)
	require.Len(t, pet0.Interface.Outputs, 1)
	assert.Equal(t, "name", pet0.Interface.Outputs[0].Name)

	// Without eval, evaluatedValue should be nil.
	assert.Nil(t, pet0.Interface.Inputs[0].EvaluatedValue)
}

func TestBuildModuleMap_WithEval(t *testing.T) {
	// NOT parallel — starts provider plugin processes via go-plugin.
	tfDir, err := filepath.Abs(filepath.Join("testdata", "tf_indexed_modules"))
	require.NoError(t, err)

	providerDir := filepath.Join(tfDir, ".terraform", "providers")
	if _, err := os.Stat(providerDir); os.IsNotExist(err) {
		t.Skip("skipping: .terraform/providers not found")
	}

	config, err := LoadConfig(tfDir)
	require.NoError(t, err)

	rawState, err := LoadRawState(filepath.Join(tfDir, "terraform.tfstate"))
	require.NoError(t, err)

	tofuCtx, cleanup, err := Evaluate(config, rawState, tfDir)
	require.NoError(t, err)
	defer cleanup()

	rootVars := BuildRootVariables(config, tfDir, nil)
	evalScopes, _ := BuildEvalScopes(context.Background(), tofuCtx, config, rawState, rootVars)

	mm, err := BuildModuleMap(context.Background(), config, evalScopes, rawState, nil, "test-stack", "test-project", nil)
	require.NoError(t, err)
	require.NotNil(t, mm)

	pet0 := mm.Modules["pet[0]"]
	require.NotNil(t, pet0)
	require.NotNil(t, pet0.Interface)
	require.Len(t, pet0.Interface.Inputs, 1)

	// With eval, evaluatedValue for "prefix" in pet[0] should be "test-0".
	assert.Equal(t, "test-0", pet0.Interface.Inputs[0].EvaluatedValue)

	pet1 := mm.Modules["pet[1]"]
	require.NotNil(t, pet1)
	require.NotNil(t, pet1.Interface)
	assert.Equal(t, "test-1", pet1.Interface.Inputs[0].EvaluatedValue)
}

func TestBuildModuleMap_Expression(t *testing.T) {
	t.Parallel()
	tfDir, err := filepath.Abs(filepath.Join("testdata", "tf_indexed_modules"))
	require.NoError(t, err)

	config, err := LoadConfig(tfDir)
	require.NoError(t, err)

	rawState, err := LoadRawState(filepath.Join(tfDir, "terraform.tfstate"))
	require.NoError(t, err)

	mm, err := BuildModuleMap(context.Background(), config, nil, rawState, nil, "test-stack", "test-project", nil)
	require.NoError(t, err)

	pet0 := mm.Modules["pet[0]"]
	require.NotNil(t, pet0)
	require.NotNil(t, pet0.Interface)
	require.Len(t, pet0.Interface.Inputs, 1)

	// The expression for "prefix" should be the call-site expression text.
	assert.Contains(t, pet0.Interface.Inputs[0].Expression, "test-${count.index}")
}

func TestWriteModuleMap(t *testing.T) {
	t.Parallel()
	mm := &ModuleMap{
		Modules: map[string]*ModuleMapEntry{
			"vpc": {
				TerraformPath: "module.vpc",
				Source:        "./modules/vpc",
				Resources: []ModuleResource{{
					Mode:             "managed",
					TranslatedURN:    "urn:pulumi:stack::project::aws:ec2/vpc:Vpc::main",
					TerraformAddress: "module.vpc.aws_vpc.main",
					ImportID:         "vpc-12345",
				}},
				Interface: &ModuleInterface{
					Inputs:  []ModuleInterfaceField{{Name: "cidr", Required: true}},
					Outputs: []ModuleInterfaceField{{Name: "id"}},
				},
			},
		},
		RootResources: []ModuleResource{
			{
				Mode:             "managed",
				TranslatedURN:    "urn:pulumi:stack::project::aws:s3/bucket:Bucket::example",
				TerraformAddress: "aws_s3_bucket.example",
				ImportID:         "my-bucket",
			},
			{
				Mode:             "data",
				TranslatedURN:    "",
				TerraformAddress: "data.terraform_remote_state.old",
				ImportID:         "",
				Attributes:       map[string]interface{}{"backend": "s3", "workspace": "prod"},
			},
		},
	}

	tmpDir := t.TempDir()
	outPath := filepath.Join(tmpDir, "module-map.json")

	err := WriteModuleMap(mm, outPath)
	require.NoError(t, err)

	// Read back and verify round-trip.
	data, err := os.ReadFile(outPath)
	require.NoError(t, err)

	var got ModuleMap
	require.NoError(t, json.Unmarshal(data, &got))

	require.Contains(t, got.Modules, "vpc")
	assert.Equal(t, "module.vpc", got.Modules["vpc"].TerraformPath)
	assert.Equal(t, "./modules/vpc", got.Modules["vpc"].Source)
	require.Len(t, got.Modules["vpc"].Resources, 1)
	assert.Equal(t, "urn:pulumi:stack::project::aws:ec2/vpc:Vpc::main", got.Modules["vpc"].Resources[0].TranslatedURN)
	assert.Equal(t, "module.vpc.aws_vpc.main", got.Modules["vpc"].Resources[0].TerraformAddress)
	assert.Equal(t, "vpc-12345", got.Modules["vpc"].Resources[0].ImportID)
	assert.Equal(t, "managed", got.Modules["vpc"].Resources[0].Mode)
	require.NotNil(t, got.Modules["vpc"].Interface)
	assert.Len(t, got.Modules["vpc"].Interface.Inputs, 1)
	assert.Equal(t, "cidr", got.Modules["vpc"].Interface.Inputs[0].Name)

	// Root resources round-trip.
	require.Len(t, got.RootResources, 2)
	assert.Equal(t, "managed", got.RootResources[0].Mode)
	assert.Equal(t, "aws_s3_bucket.example", got.RootResources[0].TerraformAddress)
	assert.Equal(t, "my-bucket", got.RootResources[0].ImportID)
	assert.Equal(t, "data", got.RootResources[1].Mode)
	assert.Equal(t, "", got.RootResources[1].TranslatedURN)
	assert.Equal(t, "data.terraform_remote_state.old", got.RootResources[1].TerraformAddress)
	require.NotNil(t, got.RootResources[1].Attributes)
	assert.Equal(t, "s3", got.RootResources[1].Attributes["backend"])
	assert.Equal(t, "prod", got.RootResources[1].Attributes["workspace"])
	assert.Nil(t, got.RootResources[0].Attributes) // this test constructs resources without attributes
}

func TestBuildModuleMap_RootResources(t *testing.T) {
	t.Parallel()
	tfDir, err := filepath.Abs(filepath.Join("testdata", "tf_indexed_modules"))
	require.NoError(t, err)

	config, err := LoadConfig(tfDir)
	require.NoError(t, err)

	rawState, err := LoadRawState(filepath.Join(tfDir, "terraform.tfstate"))
	require.NoError(t, err)

	// Add a root-level managed resource to the existing state.
	rootModule := rawState.RootModule()
	rootModule.SetResourceInstanceCurrent(
		addrs.ResourceInstance{
			Resource: addrs.Resource{
				Mode: addrs.ManagedResourceMode,
				Type: "aws_s3_bucket",
				Name: "example",
			},
			Key: addrs.NoKey,
		},
		&states.ResourceInstanceObjectSrc{
			AttrsJSON: []byte(`{"id":"my-bucket","bucket":"my-bucket"}`),
		},
		addrs.AbsProviderConfig{
			Provider: addrs.MustParseProviderSourceString("registry.opentofu.org/hashicorp/aws"),
		},
		nil,
	)

	// Add a root-level data source.
	rootModule.SetResourceInstanceCurrent(
		addrs.ResourceInstance{
			Resource: addrs.Resource{
				Mode: addrs.DataResourceMode,
				Type: "terraform_remote_state",
				Name: "old",
			},
			Key: addrs.NoKey,
		},
		&states.ResourceInstanceObjectSrc{
			AttrsJSON: []byte(`{"backend":"s3"}`),
		},
		addrs.AbsProviderConfig{
			Provider: addrs.MustParseProviderSourceString("terraform.io/builtin/terraform"),
		},
		nil,
	)

	mm, err := BuildModuleMap(context.Background(), config, nil, rawState, nil, "test-stack", "test-project", nil)
	require.NoError(t, err)
	require.NotNil(t, mm)

	// Module resources should still work.
	require.Contains(t, mm.Modules, "pet[0]")

	// Root resources should be populated.
	require.NotNil(t, mm.RootResources)
	require.Len(t, mm.RootResources, 2)

	// Sort by address for deterministic assertion.
	sort.Slice(mm.RootResources, func(i, j int) bool {
		return mm.RootResources[i].TerraformAddress < mm.RootResources[j].TerraformAddress
	})

	// Managed resource — URN falls back to raw address when pulumiProviders is nil.
	assert.Equal(t, "managed", mm.RootResources[0].Mode)
	assert.Equal(t, "aws_s3_bucket.example", mm.RootResources[0].TranslatedURN)
	assert.Equal(t, "aws_s3_bucket.example", mm.RootResources[0].TerraformAddress)
	assert.Equal(t, "my-bucket", mm.RootResources[0].ImportID)

	// Data source — URN should be empty.
	assert.Equal(t, "data", mm.RootResources[1].Mode)
	assert.Equal(t, "data.terraform_remote_state.old", mm.RootResources[1].TerraformAddress)
	assert.Equal(t, "", mm.RootResources[1].TranslatedURN)
	assert.Equal(t, "", mm.RootResources[1].ImportID) // no "id" attribute
	require.NotNil(t, mm.RootResources[1].Attributes)
	assert.Equal(t, "s3", mm.RootResources[1].Attributes["backend"])
}

func TestBuildModuleMap_DataSources(t *testing.T) {
	t.Parallel()
	tfDir, err := filepath.Abs(filepath.Join("testdata", "tf_indexed_modules"))
	require.NoError(t, err)

	config, err := LoadConfig(tfDir)
	require.NoError(t, err)

	rawState, err := LoadRawState(filepath.Join(tfDir, "terraform.tfstate"))
	require.NoError(t, err)

	// Add a data source inside module.pet[0].
	petModule := rawState.Module(addrs.RootModuleInstance.Child("pet", addrs.IntKey(0)))
	require.NotNil(t, petModule)

	petModule.SetResourceInstanceCurrent(
		addrs.ResourceInstance{
			Resource: addrs.Resource{
				Mode: addrs.DataResourceMode,
				Type: "aws_caller_identity",
				Name: "current",
			},
			Key: addrs.NoKey,
		},
		&states.ResourceInstanceObjectSrc{
			AttrsJSON: []byte(`{"account_id":"123456789","id":"123456789"}`),
		},
		addrs.AbsProviderConfig{
			Provider: addrs.MustParseProviderSourceString("registry.opentofu.org/hashicorp/aws"),
		},
		nil,
	)

	mm, err := BuildModuleMap(context.Background(), config, nil, rawState, nil, "test-stack", "test-project", nil)
	require.NoError(t, err)

	pet0 := mm.Modules["pet[0]"]
	require.NotNil(t, pet0)

	// Should have 2 resources: the managed random_pet and the data source.
	require.Len(t, pet0.Resources, 2)

	// Find the data source entry.
	var dataRes *ModuleResource
	for i := range pet0.Resources {
		if pet0.Resources[i].Mode == "data" {
			dataRes = &pet0.Resources[i]
			break
		}
	}
	require.NotNil(t, dataRes, "expected a data source in pet[0] resources")

	assert.Equal(t, "data", dataRes.Mode)
	assert.Equal(t, "module.pet[0].data.aws_caller_identity.current", dataRes.TerraformAddress)
	assert.Equal(t, "", dataRes.TranslatedURN)
	assert.Equal(t, "123456789", dataRes.ImportID)
	require.NotNil(t, dataRes.Attributes)
	assert.Equal(t, "123456789", dataRes.Attributes["account_id"])
	assert.Equal(t, "123456789", dataRes.Attributes["id"])

	// The managed resource should still be there.
	var managedRes *ModuleResource
	for i := range pet0.Resources {
		if pet0.Resources[i].Mode == "managed" {
			managedRes = &pet0.Resources[i]
			break
		}
	}
	require.NotNil(t, managedRes)
	assert.Equal(t, "managed", managedRes.Mode)
	assert.Equal(t, "module.pet[0].random_pet.this", managedRes.TerraformAddress)
}

func TestFlattenAddress(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		address   string
		attribute string
		expected  string
	}{
		{
			name:      "simple root resource",
			address:   "aws_db_instance.main",
			attribute: "password",
			expected:  "main_password",
		},
		{
			name:      "module resource with for_each",
			address:   `module.rds["mysvc"].aws_rds_cluster.main`,
			attribute: "master_password",
			expected:  "rds_mysvc_main_master_password",
		},
		{
			name:      "deep module with generic name this",
			address:   `module.console_secrets["mysvc-console-service-develop"].aws_secretsmanager_secret_version.this["mysvc-console-service-develop/ia_callback_client_oauth"]`,
			attribute: "secret_string",
			expected:  "console_secrets_mysvc_console_service_develop_ia_callback_client_oauth_secret_string",
		},
		{
			name:      "ssm parameter pattern with generic name",
			address:   `module.cdpa_param_stack_values_parameters["/develop/cdp-adapter"].aws_ssm_parameter.ssm_parameters["/develop/cdp-adapter/trusted_host_key"]`,
			attribute: "value",
			expected:  "cdpa_param_stack_values_parameters_develop_cdp_adapter_trusted_host_key_value",
		},
		{
			name:      "resource key matches module key exactly",
			address:   `module.secrets["my-key"].aws_secretsmanager_secret.this["my-key"]`,
			attribute: "secret_string",
			expected:  "secrets_my_key_secret_string",
		},
		{
			name:      "no module prefix",
			address:   "aws_ssm_parameter.db_password",
			attribute: "value",
			expected:  "db_password_value",
		},
		{
			name:      "nested modules",
			address:   `module.parent["p1"].module.child["c1"].aws_secret.this["c1/secret_val"]`,
			attribute: "value",
			expected:  "parent_p1_child_c1_secret_val_value",
		},
		// Real mysvc addresses that were too long
		{
			name:      "mysvc console secrets long key",
			address:   `module.console_secrets["mysvc-console-service-develop"].aws_secretsmanager_secret_version.this["mysvc-console-service-develop/console_client_secret"]`,
			attribute: "secret_string",
			expected:  "console_secrets_mysvc_console_service_develop_console_client_secret_secret_string",
		},
		{
			name:      "mysvc cdpa param stack",
			address:   `module.cdpa_param_stack_values_parameters["/develop/cdp-adapter"].aws_ssm_parameter.ssm_parameters["/develop/cdp-adapter/api_key"]`,
			attribute: "value",
			expected:  "cdpa_param_stack_values_parameters_develop_cdp_adapter_api_key_value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := flattenAddress(tt.address, tt.attribute)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFlattenAddress_MaxLength(t *testing.T) {
	t.Parallel()
	// All mysvc-style addresses should produce keys under 106 chars
	// (128 - len("mysvc-infrastructure") - 1 = 107, so <=106 is safe)
	longAddresses := []struct {
		address   string
		attribute string
	}{
		{`module.console_secrets["mysvc-console-service-develop"].aws_secretsmanager_secret_version.this["mysvc-console-service-develop/ia_callback_client_oauth"]`, "secret_string"},
		{`module.console_secrets["mysvc-console-service-develop"].aws_secretsmanager_secret_version.this["mysvc-console-service-develop/console_client_secret"]`, "secret_string"},
		{`module.cdpa_param_stack_values_parameters["/develop/cdp-adapter"].aws_ssm_parameter.ssm_parameters["/develop/cdp-adapter/trusted_host_key"]`, "value"},
		{`module.cdpa_param_stack_values_parameters["/develop/cdp-adapter"].aws_ssm_parameter.ssm_parameters["/develop/cdp-adapter/api_key"]`, "value"},
		{`module.cdpa_param_stack_values_parameters["/develop/cdp-adapter"].aws_ssm_parameter.ssm_parameters["/develop/cdp-adapter/certificate_password"]`, "value"},
	}

	projectName := "mysvc-infrastructure"
	maxKeyLen := 128 - len(projectName) - 1

	for _, la := range longAddresses {
		key := flattenAddress(la.address, la.attribute)
		assert.LessOrEqual(t, len(key), maxKeyLen,
			"key %q (%d chars) from %s exceeds max %d", key, len(key), la.address, maxKeyLen)
	}
}

func TestDiscoverSensitiveSecrets_Dedup(t *testing.T) {
	t.Parallel()

	// Build a state with two resources that would produce the same flattened key.
	state := states.NewState()
	rootModule := state.RootModule()

	rootModule.SetResourceInstanceCurrent(
		addrs.ResourceInstance{
			Resource: addrs.Resource{
				Mode: addrs.ManagedResourceMode,
				Type: "aws_db_instance",
				Name: "main",
			},
			Key: addrs.NoKey,
		},
		&states.ResourceInstanceObjectSrc{
			AttrsJSON: []byte(`{"id":"db1","password":"secret1"}`),
			AttrSensitivePaths: []cty.PathValueMarks{
				{Path: cty.GetAttrPath("password"), Marks: cty.NewValueMarks("sensitive")},
			},
		},
		addrs.AbsProviderConfig{
			Provider: addrs.MustParseProviderSourceString("registry.opentofu.org/hashicorp/aws"),
		},
		nil,
	)

	secrets, err := DiscoverSensitiveSecrets(state, "test-project")
	require.NoError(t, err)
	require.Len(t, secrets, 1)
	assert.Equal(t, "main_password", secrets[0].ConfigKey)
	assert.Equal(t, "secret1", secrets[0].Value)
}

func TestDiscoverSensitiveSecrets_MarksEntriesAsSecret(t *testing.T) {
	t.Parallel()

	state := states.NewState()
	rootModule := state.RootModule()

	rootModule.SetResourceInstanceCurrent(
		addrs.ResourceInstance{
			Resource: addrs.Resource{
				Mode: addrs.ManagedResourceMode,
				Type: "aws_secretsmanager_secret_version",
				Name: "my_secret",
			},
			Key: addrs.NoKey,
		},
		&states.ResourceInstanceObjectSrc{
			AttrsJSON: []byte(`{"id":"v1","secret_string":"hunter2"}`),
			AttrSensitivePaths: []cty.PathValueMarks{
				{Path: cty.GetAttrPath("secret_string"), Marks: cty.NewValueMarks("sensitive")},
			},
		},
		addrs.AbsProviderConfig{
			Provider: addrs.MustParseProviderSourceString("registry.opentofu.org/hashicorp/aws"),
		},
		nil,
	)

	entries, err := DiscoverSensitiveSecrets(state, "test-project")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "my_secret_secret_string", entries[0].ConfigKey)
	assert.Equal(t, "hunter2", entries[0].Value)
	assert.True(t, entries[0].Secret, "entries from DiscoverSensitiveSecrets should have Secret=true")
}

// TestDiscoverSensitiveSecrets_NullAttributeNotRedacted reproduces the
// aws_iot_certificate.ca_pem bug: a resource with one sensitive attribute
// AWS populated (certificate_pem) and one it left null (ca_pem), both marked
// sensitive by the provider schema and recorded in AttrSensitivePaths.
//
// Before the fix, redactSensitivePaths turned the null ca_pem into the
// literal "(sensitive)" placeholder even though DiscoverSensitiveSecrets —
// walking the very same AttrsJSON — skips nil values and never writes a
// stack config entry for it. redactedAttributeKeys then recorded a
// redactedAttributes entry pointing at that never-written config key, which
// is exactly what makes "patch-state --non-importable" hard-fail: it tries
// to resolve a config key that does not exist. This test asserts both
// halves of that failure mode are gone: no placeholder for the null
// attribute, and no dangling redactedAttributes entry for it.
func TestDiscoverSensitiveSecrets_NullAttributeNotRedacted(t *testing.T) {
	t.Parallel()

	const address = "aws_iot_certificate.cert"
	attrsJSON := []byte(`{"id":"cert-1","ca_pem":null,"certificate_pem":"-----BEGIN CERTIFICATE-----real-pem-value"}`)

	sensitivePaths := []cty.PathValueMarks{
		{Path: cty.GetAttrPath("ca_pem"), Marks: cty.NewValueMarks("sensitive")},
		{Path: cty.GetAttrPath("certificate_pem"), Marks: cty.NewValueMarks("sensitive")},
	}

	// Build the config side of the pipeline exactly as generate_module_map.go
	// does: DiscoverSensitiveSecrets walks state and decides what actually
	// gets written to stack config.
	state := states.NewState()
	rootModule := state.RootModule()
	rootModule.SetResourceInstanceCurrent(
		addrs.ResourceInstance{
			Resource: addrs.Resource{
				Mode: addrs.ManagedResourceMode,
				Type: "aws_iot_certificate",
				Name: "cert",
			},
			Key: addrs.NoKey,
		},
		&states.ResourceInstanceObjectSrc{
			AttrsJSON:          attrsJSON,
			AttrSensitivePaths: sensitivePaths,
		},
		addrs.AbsProviderConfig{
			Provider: addrs.MustParseProviderSourceString("registry.opentofu.org/hashicorp/aws"),
		},
		nil,
	)

	configEntries, err := DiscoverSensitiveSecrets(state, "test-project")
	require.NoError(t, err)
	require.Len(t, configEntries, 1, "only the populated attribute should produce a config entry")
	assert.Equal(t, "cert_certificate_pem", configEntries[0].ConfigKey)

	// Build the digest side exactly as BuildModuleMap does: decode the same
	// AttrsJSON, then redact using the same AttrSensitivePaths.
	attrs, err := decodeAttrs(attrsJSON)
	require.NoError(t, err)
	redactSensitivePaths(attrs, sensitivePaths)

	// Half 1: no "(sensitive)" placeholder for the null attribute.
	assert.Nil(t, attrs["ca_pem"], "null sensitive attribute must remain null in the digest, not become the redaction placeholder")
	assert.Equal(t, "(sensitive)", attrs["certificate_pem"], "populated sensitive attribute must still be redacted")

	// Half 2: no redactedAttributes entry pointing at a config key that was
	// never written.
	redacted := redactedAttributeKeys(address, attrs)
	assert.NotContains(t, redacted, "ca_pem",
		"must not record a redactedAttributes entry for a null attribute; DiscoverSensitiveSecrets never wrote a config key for it")
	require.Contains(t, redacted, "certificate_pem")

	// The redactedAttributes entry that does exist must point at a config
	// key DiscoverSensitiveSecrets actually created.
	configKeys := make(map[string]bool, len(configEntries))
	for _, e := range configEntries {
		configKeys[e.ConfigKey] = true
	}
	assert.True(t, configKeys[redacted["certificate_pem"]],
		"redactedAttributes must point at a config key DiscoverSensitiveSecrets wrote")
}

func TestConfigEntry_WorkspaceVarsSensitivity(t *testing.T) {
	t.Parallel()

	// Simulate workspace variables as they come from the TFC/Scalr API.
	vars := []struct {
		key       string
		value     string
		sensitive bool
	}{
		{"db_timeout", "30", false},                  // non-sensitive, non-empty → plain config
		{"api_key", "sk-abc123", true},               // sensitive, non-empty → secret (TFC behavior)
		{"redacted_password", "", true},              // sensitive, empty → skip (Scalr behavior)
		{"oauth_base_url", "https://auth.co", false}, // non-sensitive → plain config
	}

	var entries []ConfigEntry
	var skipped int
	for _, rv := range vars {
		if rv.value == "" {
			skipped++
			continue
		}
		entries = append(entries, ConfigEntry{
			ConfigKey: rv.key,
			Value:     rv.value,
			Secret:    rv.sensitive,
		})
	}

	require.Len(t, entries, 3)
	assert.Equal(t, 1, skipped, "should skip 1 redacted variable")

	// Non-sensitive vars should have Secret=false.
	assert.Equal(t, "db_timeout", entries[0].ConfigKey)
	assert.False(t, entries[0].Secret, "non-sensitive workspace var should be plain config")

	// Sensitive vars with values (TFC behavior) should have Secret=true.
	assert.Equal(t, "api_key", entries[1].ConfigKey)
	assert.True(t, entries[1].Secret, "sensitive workspace var with value should be secret")

	// Non-sensitive vars should have Secret=false.
	assert.Equal(t, "oauth_base_url", entries[2].ConfigKey)
	assert.False(t, entries[2].Secret, "non-sensitive workspace var should be plain config")
}

func TestCtyValueToInterface(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    cty.Value
		expected interface{}
	}{
		{"null", cty.NilVal, nil},
		{"string", cty.StringVal("hello"), "hello"},
		{"int_number", cty.NumberIntVal(42), int64(42)},
		{"float_number", cty.NumberFloatVal(3.14), 3.14},
		{"bool_true", cty.True, true},
		{"bool_false", cty.False, false},
		{"unknown", cty.UnknownVal(cty.String), nil},
		{
			"list",
			cty.ListVal([]cty.Value{cty.StringVal("a"), cty.StringVal("b")}),
			[]interface{}{"a", "b"},
		},
		{
			"map",
			cty.MapVal(map[string]cty.Value{"k": cty.StringVal("v")}),
			map[string]interface{}{"k": "v"},
		},
		{
			"object",
			cty.ObjectVal(map[string]cty.Value{
				"name": cty.StringVal("test"),
				"num":  cty.NumberIntVal(1),
			}),
			map[string]interface{}{"name": "test", "num": int64(1)},
		},
		{
			"tuple",
			cty.TupleVal([]cty.Value{cty.StringVal("x"), cty.NumberIntVal(2)}),
			[]interface{}{"x", int64(2)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ctyValueToInterface(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRawStateFromTfjson_DataSources(t *testing.T) {
	t.Parallel()

	tfjsonState := &tfjson.State{
		FormatVersion: "1.0",
		Values: &tfjson.StateValues{
			RootModule: &tfjson.StateModule{
				Resources: []*tfjson.StateResource{
					{
						Address:      "data.terraform_remote_state.old",
						Mode:         tfjson.DataResourceMode,
						Type:         "terraform_remote_state",
						Name:         "old",
						ProviderName: "terraform.io/builtin/terraform",
						AttributeValues: map[string]interface{}{
							"backend": "s3",
						},
					},
					{
						Address:      "aws_s3_bucket.example",
						Mode:         tfjson.ManagedResourceMode,
						Type:         "aws_s3_bucket",
						Name:         "example",
						ProviderName: "registry.opentofu.org/hashicorp/aws",
						AttributeValues: map[string]interface{}{
							"id":     "my-bucket",
							"bucket": "my-bucket",
						},
					},
				},
			},
		},
	}

	state := rawStateFromTfjson(tfjsonState)

	rootModule := state.RootModule()
	require.NotNil(t, rootModule)

	dataRes := rootModule.Resource(addrs.Resource{
		Mode: addrs.DataResourceMode,
		Type: "terraform_remote_state",
		Name: "old",
	})
	require.NotNil(t, dataRes, "expected data.terraform_remote_state.old in root module")

	managedRes := rootModule.Resource(addrs.Resource{
		Mode: addrs.ManagedResourceMode,
		Type: "aws_s3_bucket",
		Name: "example",
	})
	require.NotNil(t, managedRes, "expected aws_s3_bucket.example in root module")
}

// sensitiveResource is a helper for the sensitivity tests below.
func sensitiveResource(m *states.Module, typ, name, attrsJSON string, paths []cty.PathValueMarks) {
	m.SetResourceInstanceCurrent(
		addrs.ResourceInstance{
			Resource: addrs.Resource{Mode: addrs.ManagedResourceMode, Type: typ, Name: name},
			Key:      addrs.NoKey,
		},
		&states.ResourceInstanceObjectSrc{
			AttrsJSON:          []byte(attrsJSON),
			AttrSensitivePaths: paths,
		},
		addrs.AbsProviderConfig{
			Provider: addrs.MustParseProviderSourceString("registry.opentofu.org/hashicorp/aws"),
		},
		nil,
	)
}

// TestRedactSensitivePaths_NestedPathIsRedacted covers the depth gap. OpenTofu
// records nested sensitive paths — a NestingSet block with a sensitive
// attribute (aws_mq_broker's user[].password) yields a length-3 path — and
// skipping them left plaintext in the digest, which on this branch flows into
// PulumiOutputs and into the injected resource's state outputs unmarked.
func TestRedactSensitivePaths_NestedPathIsRedacted(t *testing.T) {
	t.Parallel()

	attrs := map[string]interface{}{
		"broker_name": "b",
		"user": []interface{}{
			map[string]interface{}{"username": "admin", "password": "NESTED-SECRET"},
		},
	}
	redactSensitivePaths(attrs, []cty.PathValueMarks{{
		Path: cty.Path{
			cty.GetAttrStep{Name: "user"},
			cty.IndexStep{Key: cty.NumberIntVal(0)},
			cty.GetAttrStep{Name: "password"},
		},
		Marks: cty.NewValueMarks("sensitive"),
	}})

	user := attrs["user"].([]interface{})[0].(map[string]interface{})
	assert.Equal(t, redactedPlaceholder, user["password"])
	assert.Equal(t, "admin", user["username"], "only the marked path may be redacted")
	assert.Equal(t, "b", attrs["broker_name"])
}

// TestRedactSensitivePaths_UnresolvableIndexRedactsEveryElement pins the
// direction the ambiguity is resolved. A SET index is the element value, not an
// ordinal, so it cannot address a slot in a decoded JSON array — over-redacting
// costs a placeholder the operator must supply, under-redacting writes a secret
// into state.
func TestRedactSensitivePaths_UnresolvableIndexRedactsEveryElement(t *testing.T) {
	t.Parallel()

	attrs := map[string]interface{}{
		"user": []interface{}{
			map[string]interface{}{"password": "A"},
			map[string]interface{}{"password": "B"},
		},
	}
	redactSensitivePaths(attrs, []cty.PathValueMarks{{
		Path: cty.Path{
			cty.GetAttrStep{Name: "user"},
			// A set index: the element value rather than an ordinal.
			cty.IndexStep{Key: cty.ObjectVal(map[string]cty.Value{"password": cty.StringVal("A")})},
			cty.GetAttrStep{Name: "password"},
		},
		Marks: cty.NewValueMarks("sensitive"),
	}})

	for i, elem := range attrs["user"].([]interface{}) {
		assert.Equal(t, redactedPlaceholder, elem.(map[string]interface{})["password"],
			"element %d must be redacted when the index cannot be resolved", i)
	}
}

// TestDiscoverSensitiveSecrets_LargeIntegerKeepsItsDigits guards the config
// value against the float64 round trip. The value is stringified with %v and
// written into stack config, and injection later resolves that key and writes
// it into state as the resource's real secret — so a plain decode turned a
// sensitive 1234567890123456789 into "1.2345678901234568e+18" there.
func TestDiscoverSensitiveSecrets_LargeIntegerKeepsItsDigits(t *testing.T) {
	t.Parallel()

	state := states.NewState()
	sensitiveResource(state.RootModule(), "aws_x", "big",
		`{"id":"1","token":1234567890123456789}`,
		[]cty.PathValueMarks{
			{Path: cty.GetAttrPath("token"), Marks: cty.NewValueMarks("sensitive")},
		})

	secrets, err := DiscoverSensitiveSecrets(state, "p")
	require.NoError(t, err)
	require.Len(t, secrets, 1)
	assert.Equal(t, "1234567890123456789", secrets[0].Value)
}

// TestDiscoverSensitiveSecrets_CollisionIsAnError pins the replacement for the
// old _2 suffixing. The suffixed key was written to config but nothing could
// read it back: the sidecar records the config key as a bare
// flattenAddress(address, attribute), so both colliding resources pointed at
// the un-suffixed key and the second silently resolved to the FIRST one's
// secret — a real secret written into the wrong resource's state.
func TestDiscoverSensitiveSecrets_CollisionIsAnError(t *testing.T) {
	t.Parallel()

	state := states.NewState()
	root := state.RootModule()
	// flattenAddress drops the resource type, so two resources named "this"
	// collide on "this_password".
	sensitiveResource(root, "aws_db_instance", "this", `{"id":"1","password":"SECRET-A"}`,
		[]cty.PathValueMarks{{Path: cty.GetAttrPath("password"), Marks: cty.NewValueMarks("sensitive")}})
	sensitiveResource(root, "aws_rds_cluster", "this", `{"id":"2","password":"SECRET-B"}`,
		[]cty.PathValueMarks{{Path: cty.GetAttrPath("password"), Marks: cty.NewValueMarks("sensitive")}})

	_, err := DiscoverSensitiveSecrets(state, "p")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collision")
}

// TestSensitivePathsFromTfjson covers the state format that had NO redaction at
// all. It is selected automatically — a "format_version" key is all
// DetectStateFormatBytes needs — so an operator passing "tofu show -json"
// output got plaintext secrets in the digest with nothing to indicate it.
func TestSensitivePathsFromTfjson(t *testing.T) {
	t.Parallel()

	paths := sensitivePathsFromTfjson([]byte(
		`{"password":true,"tags":false,"user":[{"password":true,"name":false}]}`))
	require.Len(t, paths, 2)

	attrs := map[string]interface{}{
		"password": "TOP-SECRET",
		"tags":     "public",
		"user": []interface{}{
			map[string]interface{}{"password": "NESTED-SECRET", "name": "admin"},
		},
	}
	redactSensitivePaths(attrs, paths)

	assert.Equal(t, redactedPlaceholder, attrs["password"])
	assert.Equal(t, "public", attrs["tags"], "a false leaf is not sensitive")
	user := attrs["user"].([]interface{})[0].(map[string]interface{})
	assert.Equal(t, redactedPlaceholder, user["password"])
	assert.Equal(t, "admin", user["name"])
}

func TestSensitivePathsFromTfjson_EmptyOrInvalid(t *testing.T) {
	t.Parallel()
	assert.Nil(t, sensitivePathsFromTfjson(nil))
	assert.Nil(t, sensitivePathsFromTfjson([]byte(`not json`)))
	assert.Empty(t, sensitivePathsFromTfjson([]byte(`{"a":false}`)))
}
