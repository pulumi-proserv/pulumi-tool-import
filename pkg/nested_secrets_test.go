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

	"github.com/pulumi/opentofu/addrs"
	"github.com/pulumi/opentofu/states"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestTaggedPlaceholderRoundTrip(t *testing.T) {
	t.Parallel()

	tag := taggedPlaceholder("user[0].password")
	assert.Equal(t, "(sensitive:user[0].password)", tag)

	path, ok := placeholderPath(tag)
	require.True(t, ok)
	assert.Equal(t, "user[0].password", path)

	// The bare legacy placeholder carries no path.
	_, ok = placeholderPath(redactedPlaceholder)
	assert.False(t, ok)

	// Both forms are recognised as placeholders; real values are not.
	assert.True(t, isRedactedPlaceholder(redactedPlaceholder))
	assert.True(t, isRedactedPlaceholder(tag))
	assert.False(t, isRedactedPlaceholder("hunter2"))
	assert.False(t, isRedactedPlaceholder("(sensitivity training)"))
}

func TestFlattenAddressPath(t *testing.T) {
	t.Parallel()

	// Exact values are pinned: the key is a compatibility contract with
	// written digests.
	assert.Equal(t, "b_user_0_password", flattenAddressPath("aws_mq_broker.b", "user[0].password"))
	assert.Equal(t, "b_user_1_password", flattenAddressPath("aws_mq_broker.b", "user[1].password"))

	// A single-segment path must produce the same key flattenAddress always
	// has, so existing digests stay compatible.
	assert.Equal(t, flattenAddress("aws_x.n", "password"),
		flattenAddressPath("aws_x.n", "password"))
}

func nestedSecretState(t *testing.T) *states.State {
	t.Helper()
	state := states.NewState()
	state.RootModule().SetResourceInstanceCurrent(
		addrs.ResourceInstance{
			Resource: addrs.Resource{Mode: addrs.ManagedResourceMode, Type: "aws_mq_broker", Name: "b"},
			Key:      addrs.NoKey,
		},
		&states.ResourceInstanceObjectSrc{
			AttrsJSON: []byte(`{"id":"b1","broker_name":"b","user":[` +
				`{"username":"admin","password":"NESTED-SECRET"},` +
				`{"username":"second","password":"OTHER-SECRET"}]}`),
			AttrSensitivePaths: []cty.PathValueMarks{
				{
					Path: cty.Path{
						cty.GetAttrStep{Name: "user"},
						cty.IndexStep{Key: cty.NumberIntVal(0)},
						cty.GetAttrStep{Name: "password"},
					},
					Marks: cty.NewValueMarks("sensitive"),
				},
				{
					Path: cty.Path{
						cty.GetAttrStep{Name: "user"},
						cty.IndexStep{Key: cty.NumberIntVal(1)},
						cty.GetAttrStep{Name: "password"},
					},
					Marks: cty.NewValueMarks("sensitive"),
				},
			},
		},
		addrs.AbsProviderConfig{
			Provider: addrs.MustParseProviderSourceString("registry.opentofu.org/hashicorp/aws"),
		},
		nil,
	)
	return state
}

func TestNestedMarkedSecret_RedactedAndRecoverable(t *testing.T) {
	t.Parallel()

	state := nestedSecretState(t)

	resources, err := matchResources(t.Context(), state, nil, nil, "dev", "proj", nil)
	require.NoError(t, err)
	require.Len(t, resources, 1)
	attrs := resources[0].Attributes
	user := attrs["user"].([]interface{})
	assert.Equal(t, taggedPlaceholder("user[0].password"),
		user[0].(map[string]interface{})["password"])
	assert.Equal(t, taggedPlaceholder("user[1].password"),
		user[1].(map[string]interface{})["password"])
	assert.Equal(t, "admin", user[0].(map[string]interface{})["username"],
		"unmarked attributes must be left alone")

	// The sidecar's key map now covers the nested paths.
	keys := redactedAttributeKeys("aws_mq_broker.b", attrs)
	require.Len(t, keys, 2)
	assert.Equal(t, flattenAddressPath("aws_mq_broker.b", "user[0].password"),
		keys["user[0].password"])

	// Discovery writes each value to stack config under the same key.
	secrets, err := DiscoverSensitiveSecrets(state, "proj", nil)
	require.NoError(t, err)
	require.Len(t, secrets, 2)
	byKey := map[string]string{}
	for _, s := range secrets {
		require.True(t, s.Secret)
		byKey[s.ConfigKey] = s.Value
	}
	assert.Equal(t, "NESTED-SECRET", byKey[keys["user[0].password"]])
	assert.Equal(t, "OTHER-SECRET", byKey[keys["user[1].password"]])
}

func TestResolveOutputSecrets_ResolvesNestedTaggedPlaceholders(t *testing.T) {
	t.Parallel()

	key := flattenAddressPath("aws_mq_broker.b", "user[0].password")
	r := &NonImportableResource{
		Type: "aws:mq/broker:Broker", Name: "b", TerraformAddress: "aws_mq_broker.b",
		RedactedAttributes: map[string]string{"user[0].password": key},
	}
	outputs := map[string]interface{}{
		"brokerName": "b",
		"users": []interface{}{
			map[string]interface{}{
				"username": "admin",
				"password": taggedPlaceholder("user[0].password"),
			},
		},
	}
	resolved, err := resolveOutputSecrets(r, outputs, nil, map[string]string{key: "NESTED-SECRET"})
	require.NoError(t, err)
	assert.Equal(t, 1, resolved)

	pw := outputs["users"].([]interface{})[0].(map[string]interface{})["password"]
	env, ok := pw.(map[string]interface{})
	require.True(t, ok, "the resolved secret must carry the Pulumi secret envelope, got %T", pw)
	assert.Equal(t, secretSig, env[sigKey])
	assert.NotContains(t, env, "value")

	// And the backstop no longer objects.
	require.NoError(t, checkNoPlaceholders(r, "output", outputs, "outputs"))
}

func TestResolveOutputSecrets_MissingNestedKeyIsAnError(t *testing.T) {
	t.Parallel()

	r := &NonImportableResource{
		Type: "aws:mq/broker:Broker", Name: "b", TerraformAddress: "aws_mq_broker.b",
		RedactedAttributes: map[string]string{"user[0].password": "some_key"},
	}
	outputs := map[string]interface{}{
		"users": []interface{}{
			map[string]interface{}{"password": taggedPlaceholder("user[0].password")},
		},
	}
	_, err := resolveOutputSecrets(r, outputs, nil, map[string]string{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "some_key")
	assert.NotContains(t, err.Error(), "NESTED-SECRET")
}

func TestCheckNoPlaceholders_CatchesTaggedForm(t *testing.T) {
	t.Parallel()

	r := &NonImportableResource{Type: "aws:mq/broker:Broker", Name: "b"}
	err := checkNoPlaceholders(r, "output", map[string]interface{}{
		"nested": []interface{}{map[string]interface{}{
			"password": taggedPlaceholder("user[0].password"),
		}},
	}, "outputs")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nested[0].password")
}

func TestSchemaSensitiveLeaks_TaggedPlaceholderIsClean(t *testing.T) {
	t.Parallel()

	leaks := schemaSensitiveLeaks(map[string]interface{}{
		"tunnel": []interface{}{
			map[string]interface{}{"preshared_key": taggedPlaceholder("tunnel[0].preshared_key")},
		},
	}, sensitiveSchema())
	assert.Empty(t, leaks)
}

func TestRedactSchemaSensitive_LeavesTaggedPlaceholdersAlone(t *testing.T) {
	t.Parallel()

	attrs := map[string]interface{}{"password": taggedPlaceholder("password")}
	recovered := redactSchemaSensitive(attrs, sensitiveSchema())
	assert.Empty(t, recovered)
	assert.Equal(t, taggedPlaceholder("password"), attrs["password"])
}

func TestAttachRawStateDelta_DropsTaggedPlaceholderDeltas(t *testing.T) {
	t.Parallel()

	r := &NonImportableResource{
		Type: "aws:mq/broker:Broker", Name: "b", TerraformAddress: "aws_mq_broker.b",
		RawStateDelta: map[string]interface{}{
			"obj": map[string]interface{}{
				"ps": map[string]interface{}{
					"user": map[string]interface{}{
						"replace": map[string]interface{}{
							"raw": taggedPlaceholder("user[0].password"),
						},
					},
				},
			},
		},
	}
	obj := map[string]interface{}{"urn": "urn:pulumi:dev::p::aws:mq/broker:Broker::b"}
	outcome, note := attachRawStateDelta(r, obj, map[string]interface{}{})
	assert.Equal(t, deltaDroppedSensitive, outcome)
	assert.Contains(t, note, "aws_mq_broker.b")
	assert.Contains(t, note, taggedPlaceholderPrefix,
		"the note names the form actually matched, so the operator greps for the right string")
}

func TestDiscoverSensitiveSecrets_FansOutUnresolvableIndexes(t *testing.T) {
	t.Parallel()

	state := states.NewState()
	state.RootModule().SetResourceInstanceCurrent(
		addrs.ResourceInstance{
			Resource: addrs.Resource{Mode: addrs.ManagedResourceMode, Type: "aws_mq_broker", Name: "b"},
			Key:      addrs.NoKey,
		},
		&states.ResourceInstanceObjectSrc{
			AttrsJSON: []byte(`{"id":"b1","user":[` +
				`{"username":"a","password":"P0"},{"username":"b","password":"P1"}]}`),
			AttrSensitivePaths: []cty.PathValueMarks{{
				// An object index key cannot resolve against a JSON list, so
				// redaction redacts EVERY element's password; discovery must
				// record a key for each.
				Path: cty.Path{
					cty.GetAttrStep{Name: "user"},
					cty.IndexStep{Key: cty.ObjectVal(map[string]cty.Value{"username": cty.StringVal("a")})},
					cty.GetAttrStep{Name: "password"},
				},
				Marks: cty.NewValueMarks("sensitive"),
			}},
		},
		addrs.AbsProviderConfig{
			Provider: addrs.MustParseProviderSourceString("registry.opentofu.org/hashicorp/aws"),
		},
		nil,
	)

	secrets, err := DiscoverSensitiveSecrets(state, "proj", nil)
	require.NoError(t, err)
	require.Len(t, secrets, 2, "one config entry per fanned-out element")
	values := map[string]bool{}
	for _, s := range secrets {
		values[s.Value] = true
	}
	assert.True(t, values["P0"] && values["P1"])
}

// The drift-catching mirror: redaction's tagged paths must equal the leaves
// discovery resolves.
func TestRedactionAndDiscoveryAgreeOnLeaves(t *testing.T) {
	t.Parallel()

	marked := []cty.PathValueMarks{
		{Path: cty.Path{
			cty.GetAttrStep{Name: "user"},
			cty.IndexStep{Key: cty.NumberIntVal(0)},
			cty.GetAttrStep{Name: "password"},
		}, Marks: cty.NewValueMarks("sensitive")},
		{Path: cty.Path{
			cty.GetAttrStep{Name: "tags"},
			cty.IndexStep{Key: cty.StringVal("secret key")},
		}, Marks: cty.NewValueMarks("sensitive")},
	}
	mkAttrs := func() map[string]interface{} {
		return map[string]interface{}{
			"user": []interface{}{
				map[string]interface{}{"username": "a", "password": "P0"},
			},
			"tags": map[string]interface{}{"secret key": "V", "plain": "W"},
		}
	}

	// Discovery's view of the leaves.
	discovered := map[string]bool{}
	for _, pvm := range marked {
		for _, leaf := range concreteSensitiveLeaves(mkAttrs(), pvm.Path, nil) {
			discovered[leaf.path] = true
		}
	}

	// Redaction's view: collect the tagged paths it wrote.
	attrs := mkAttrs()
	redactSensitivePaths(attrs, marked)
	tagged := map[string]bool{}
	collectTaggedPlaceholders(attrs["user"], func(p string) { tagged[p] = true })
	collectTaggedPlaceholders(attrs["tags"], func(p string) { tagged[p] = true })

	assert.Equal(t, discovered, tagged,
		"a leaf redaction tags but discovery cannot resolve is a secret lost silently")
}
