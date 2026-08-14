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
