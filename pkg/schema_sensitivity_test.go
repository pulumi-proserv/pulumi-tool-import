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
	"testing"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg/providermap"
	"github.com/pulumi/opentofu/addrs"
	"github.com/pulumi/opentofu/states"
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge/info"
	shim "github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfshim"
	shimschema "github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfshim/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sensitiveSchema: a resource with a top-level sensitive attribute and a
// nested block carrying another, which is the shape that made nested
// redaction gaps invisible.
func sensitiveSchema() shim.SchemaMap {
	tunnel := (&shimschema.Resource{
		Schema: shimschema.SchemaMap{
			"inside_cidr":   (&shimschema.Schema{Type: shim.TypeString, Optional: true}).Shim(),
			"preshared_key": (&shimschema.Schema{Type: shim.TypeString, Optional: true, Sensitive: true}).Shim(),
		},
	}).Shim()

	return shimschema.SchemaMap{
		"id":       (&shimschema.Schema{Type: shim.TypeString, Computed: true}).Shim(),
		"name":     (&shimschema.Schema{Type: shim.TypeString, Optional: true}).Shim(),
		"password": (&shimschema.Schema{Type: shim.TypeString, Optional: true, Sensitive: true}).Shim(),
		"tunnel":   (&shimschema.Schema{Type: shim.TypeList, Optional: true, Elem: tunnel}).Shim(),
	}
}

// The failure that shipped: redaction ran but had nothing to redact, because
// the state carried no sensitive paths for this format. The value is in the
// clear and every check driven by those same paths agrees it is fine.
func TestSchemaSensitiveLeaks_UnredactedTopLevelValueIsReported(t *testing.T) {
	t.Parallel()

	leaks := schemaSensitiveLeaks(map[string]interface{}{
		"id":       "vpn-123",
		"name":     "primary",
		"password": "hunter2",
	}, sensitiveSchema())

	require.Len(t, leaks, 1)
	assert.Equal(t, "password", leaks[0])
	assert.NotContains(t, leaks[0], "hunter2", "a leak report must not repeat the secret it found")
}

func TestSchemaSensitiveLeaks_RedactedValueIsClean(t *testing.T) {
	t.Parallel()

	leaks := schemaSensitiveLeaks(map[string]interface{}{
		"id":       "vpn-123",
		"password": redactedPlaceholder,
	}, sensitiveSchema())

	assert.Empty(t, leaks)
}

// A sensitive attribute Terraform holds no value for is not a leak: there is
// nothing to disclose, and reporting it would fail digests that are fine.
func TestSchemaSensitiveLeaks_NullAndAbsentValuesAreClean(t *testing.T) {
	t.Parallel()

	assert.Empty(t, schemaSensitiveLeaks(map[string]interface{}{
		"id":       "vpn-123",
		"password": nil,
	}, sensitiveSchema()))

	assert.Empty(t, schemaSensitiveLeaks(map[string]interface{}{
		"id": "vpn-123",
	}, sensitiveSchema()))
}

// Nested depth is the case the state-driven redaction missed for every
// resource until this branch fixed it, so the cross-check has to reach it too.
func TestSchemaSensitiveLeaks_UnredactedNestedValueIsReported(t *testing.T) {
	t.Parallel()

	leaks := schemaSensitiveLeaks(map[string]interface{}{
		"id": "vpn-123",
		"tunnel": []interface{}{
			map[string]interface{}{"inside_cidr": "169.254.0.0/30", "preshared_key": redactedPlaceholder},
			map[string]interface{}{"inside_cidr": "169.254.0.4/30", "preshared_key": "s3cret"},
		},
	}, sensitiveSchema())

	require.Len(t, leaks, 1)
	assert.Equal(t, "tunnel[1].preshared_key", leaks[0])
}

// MaxItems=1 blocks arrive as a bare map rather than a list of one.
func TestSchemaSensitiveLeaks_SingletonBlockIsWalked(t *testing.T) {
	t.Parallel()

	leaks := schemaSensitiveLeaks(map[string]interface{}{
		"tunnel": map[string]interface{}{"preshared_key": "s3cret"},
	}, sensitiveSchema())

	require.Len(t, leaks, 1)
	assert.Equal(t, "tunnel.preshared_key", leaks[0])
}

// With no bridged schema there is no second opinion to consult. Silence here
// means "could not check", which is why the caller reports that separately
// rather than treating it as a pass.
func TestSchemaSensitiveLeaks_NoSchemaReportsNothing(t *testing.T) {
	t.Parallel()

	assert.Empty(t, schemaSensitiveLeaks(map[string]interface{}{"password": "hunter2"}, nil))
}

func TestSchemaSensitiveLeaks_ReportsEveryLeakSorted(t *testing.T) {
	t.Parallel()

	leaks := schemaSensitiveLeaks(map[string]interface{}{
		"password": "hunter2",
		"tunnel":   []interface{}{map[string]interface{}{"preshared_key": "s3cret"}},
	}, sensitiveSchema())

	assert.Equal(t, []string{"password", "tunnel[0].preshared_key"}, leaks)
}

// The point of the schema being a second source: an attribute it marks
// sensitive that the state does not mark is redacted anyway, and — because the
// config key is derived from the address and attribute name, never from the
// marks — it is still recoverable afterwards. Failing the digest is not the
// only safe answer, only the last resort.
func TestRedactSchemaSensitive_RedactsWhatTheStateDidNotMark(t *testing.T) {
	t.Parallel()

	attrs := map[string]interface{}{
		"id":       "vpn-123",
		"name":     "primary",
		"password": "hunter2",
	}
	recovered := redactSchemaSensitive(attrs, sensitiveSchema())

	assert.Equal(t, redactedPlaceholder, attrs["password"])
	assert.Equal(t, "primary", attrs["name"], "a non-sensitive attribute must be left alone")
	assert.Equal(t, map[string]string{"password": "hunter2"}, recovered,
		"the real value must be handed back so discovery can put it in stack config")
}

func TestRedactSchemaSensitive_LeavesAlreadyRedactedAndValuelessAttributes(t *testing.T) {
	t.Parallel()

	attrs := map[string]interface{}{
		"password": redactedPlaceholder,
		"name":     "primary",
	}
	assert.Empty(t, redactSchemaSensitive(attrs, sensitiveSchema()),
		"an already-redacted attribute has no real value left to recover")

	attrs = map[string]interface{}{"password": nil}
	assert.Empty(t, redactSchemaSensitive(attrs, sensitiveSchema()))
	assert.Nil(t, attrs["password"], "a null sensitive attribute must not become a placeholder")
}

func TestRedactSchemaSensitive_NoSchemaChangesNothing(t *testing.T) {
	t.Parallel()

	attrs := map[string]interface{}{"password": "hunter2"}
	assert.Empty(t, redactSchemaSensitive(attrs, nil))
	assert.Equal(t, "hunter2", attrs["password"])
}

// Nested recovery is not wired: redactedAttributeKeys, DiscoverSensitiveSecrets
// and the resolvers in state_injector.go are all top-level only. So the
// backstop must still report a nested leak rather than redact one it cannot
// recover.
func TestRedactSchemaSensitive_DoesNotTouchNestedAttributes(t *testing.T) {
	t.Parallel()

	attrs := map[string]interface{}{
		"tunnel": []interface{}{map[string]interface{}{"preshared_key": "s3cret"}},
	}
	assert.Empty(t, redactSchemaSensitive(attrs, sensitiveSchema()))

	leaks := schemaSensitiveLeaks(attrs, sensitiveSchema())
	require.Len(t, leaks, 1)
	assert.Equal(t, "tunnel[0].preshared_key", leaks[0])
}

// After schema-driven redaction the backstop has nothing left to report for a
// top-level attribute: the two together are what turn a digest that used to
// fail into one that succeeds with the secret in config.
func TestRedactSchemaSensitive_ClearsTheTopLevelLeak(t *testing.T) {
	t.Parallel()

	attrs := map[string]interface{}{"password": "hunter2"}
	redactSchemaSensitive(attrs, sensitiveSchema())
	assert.Empty(t, schemaSensitiveLeaks(attrs, sensitiveSchema()))
}

// sensitiveProviders bridges sensitiveSchema() as the aws provider's
// aws_vpn_connection, so discovery and redaction see the same schema the real
// pipeline would.
func sensitiveProviders() map[providermap.TerraformProviderName]*ProviderWithMetadata {
	shimProv := &shimschema.Provider{
		ResourcesMap: shimschema.ResourceMap{
			"aws_vpn_connection": (&shimschema.Resource{
				Schema: shimschema.SchemaMap{
					"id":       (&shimschema.Schema{Type: shim.TypeString, Computed: true}).Shim(),
					"name":     (&shimschema.Schema{Type: shim.TypeString, Optional: true}).Shim(),
					"password": (&shimschema.Schema{Type: shim.TypeString, Optional: true, Sensitive: true}).Shim(),
				},
			}).Shim(),
		},
	}
	return map[providermap.TerraformProviderName]*ProviderWithMetadata{
		"registry.opentofu.org/hashicorp/aws": {
			Provider: &info.Provider{
				Name:      "aws",
				P:         shimProv.Shim(),
				Resources: map[string]*info.Resource{"aws_vpn_connection": {}},
			},
			TerraformAddress: "registry.opentofu.org/hashicorp/aws",
		},
	}
}

func stateWithUnmarkedSecret(t *testing.T) *states.State {
	t.Helper()
	state := states.NewState()
	state.RootModule().SetResourceInstanceCurrent(
		addrs.ResourceInstance{
			Resource: addrs.Resource{
				Mode: addrs.ManagedResourceMode,
				Type: "aws_vpn_connection",
				Name: "primary",
			},
			Key: addrs.NoKey,
		},
		&states.ResourceInstanceObjectSrc{
			// No AttrSensitivePaths at all — the "tofu show -json" shape, and
			// the reason a single-source redaction leaked.
			AttrsJSON: []byte(`{"id":"vpn-1","name":"primary","password":"hunter2"}`),
		},
		addrs.AbsProviderConfig{
			Provider: addrs.MustParseProviderSourceString("registry.opentofu.org/hashicorp/aws"),
		},
		nil,
	)
	return state
}

// The loop that has to close: with the state carrying no marks at all, the
// schema alone both redacts the value out of the digest AND puts it in stack
// config under the key injection will look for. Either half alone is a bug —
// redaction without discovery strands an unresolvable placeholder, discovery
// without redaction leaves the secret in the digest.
func TestSchemaSensitivity_UnmarkedSecretIsRedactedAndStillRecoverable(t *testing.T) {
	t.Parallel()

	state := stateWithUnmarkedSecret(t)
	providers := sensitiveProviders()

	resources, err := matchResources(t.Context(), state, nil, providers, "dev", "proj", nil)
	require.NoError(t, err, "an unmarked but schema-sensitive attribute must not fail the digest")
	require.Len(t, resources, 1)
	assert.Equal(t, redactedPlaceholder, resources[0].Attributes["password"],
		"the digest must not carry the real value")
	assert.Equal(t, "primary", resources[0].Attributes["name"])

	secrets, err := DiscoverSensitiveSecrets(state, "proj", providers)
	require.NoError(t, err)
	require.Len(t, secrets, 1, "the redacted value must be recoverable from stack config")
	assert.Equal(t, "hunter2", secrets[0].Value)
	assert.True(t, secrets[0].Secret)

	// The key injection looks for is derived from the address and attribute
	// name, which is exactly why the missing marks cost nothing here.
	assert.Equal(t, flattenAddress("aws_vpn_connection.primary", "password"), secrets[0].ConfigKey)
}

// Without a bridged schema there is no second source, so the old behaviour
// stands: nothing is redacted and nothing is discovered. Worth pinning so the
// dependence on having a schema stays visible.
func TestSchemaSensitivity_NoProviderLeavesTheOldBehaviour(t *testing.T) {
	t.Parallel()

	state := stateWithUnmarkedSecret(t)

	resources, err := matchResources(t.Context(), state, nil, nil, "dev", "proj", nil)
	require.NoError(t, err)
	require.Len(t, resources, 1)
	assert.Equal(t, "hunter2", resources[0].Attributes["password"])

	secrets, err := DiscoverSensitiveSecrets(state, "proj", nil)
	require.NoError(t, err)
	assert.Empty(t, secrets)
}
