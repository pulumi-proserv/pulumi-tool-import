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
