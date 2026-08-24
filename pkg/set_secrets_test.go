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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSecretMapping(t *testing.T) {
	t.Parallel()

	t.Run("simple resource", func(t *testing.T) {
		m, err := ParseSecretMapping("dbPassword=aws_ssm_parameter.db_pass:value")
		require.NoError(t, err)
		assert.Equal(t, "dbPassword", m.ConfigKey)
		assert.Equal(t, "aws_ssm_parameter.db_pass", m.TerraformAddress)
		assert.Equal(t, "value", m.Attribute)
	})

	t.Run("module resource with index key", func(t *testing.T) {
		m, err := ParseSecretMapping(`apiKey=module.params["/prod/app"].aws_ssm_parameter.params["/prod/app/api_key"]:value`)
		require.NoError(t, err)
		assert.Equal(t, "apiKey", m.ConfigKey)
		assert.Equal(t, `module.params["/prod/app"].aws_ssm_parameter.params["/prod/app/api_key"]`, m.TerraformAddress)
		assert.Equal(t, "value", m.Attribute)
	})

	t.Run("secrets manager", func(t *testing.T) {
		m, err := ParseSecretMapping(`mySecret=aws_secretsmanager_secret_version.my_secret["key"]:secret_string`)
		require.NoError(t, err)
		assert.Equal(t, "mySecret", m.ConfigKey)
		assert.Equal(t, `aws_secretsmanager_secret_version.my_secret["key"]`, m.TerraformAddress)
		assert.Equal(t, "secret_string", m.Attribute)
	})

	t.Run("missing equals", func(t *testing.T) {
		_, err := ParseSecretMapping("noequals")
		assert.Error(t, err)
	})

	t.Run("missing colon", func(t *testing.T) {
		_, err := ParseSecretMapping("key=address_no_colon")
		assert.Error(t, err)
	})
}

func TestExtractSecretValues_PreservesLargeIntegers(t *testing.T) {
	t.Parallel()

	state := []byte(`{"resources":[{"type":"aws_x","name":"n","mode":"managed","instances":[
		{"attributes":{"secret_number":1234567890123456789}}]}]}`)
	cm, err := extractSecretValues(state, []SecretMapping{{
		TerraformAddress: "aws_x.n", Attribute: "secret_number", ConfigKey: "k",
	}})
	require.NoError(t, err)
	assert.Equal(t, "1234567890123456789", cm["k"].Value)
	assert.True(t, cm["k"].Secret)
}

func TestExtractSecretValues_IntegerIndexKeyRendersAsBracketZero(t *testing.T) {
	t.Parallel()

	state := []byte(`{"resources":[{"type":"aws_x","name":"n","mode":"managed","instances":[
		{"index_key":0,"attributes":{"password":"hunter2"}}]}]}`)
	cm, err := extractSecretValues(state, []SecretMapping{{
		TerraformAddress: "aws_x.n[0]", Attribute: "password", ConfigKey: "k",
	}})
	require.NoError(t, err)
	assert.Equal(t, "hunter2", cm["k"].Value)
}

func TestExtractSecretValues_AddressForms(t *testing.T) {
	t.Parallel()

	state := []byte(`{"resources":[
		{"type":"aws_x","name":"n","module":"module.foo","mode":"managed","instances":[
			{"attributes":{"password":"in-module"}}]},
		{"type":"aws_x","name":"d","mode":"data","instances":[
			{"attributes":{"password":"in-data"}}]},
		{"type":"aws_x","name":"k","mode":"managed","instances":[
			{"index_key":"key","attributes":{"password":"by-key"}}]}
	]}`)

	cases := []struct{ addr, want string }{
		{"module.foo.aws_x.n", "in-module"},
		{"data.aws_x.d", "in-data"},
		{`aws_x.k["key"]`, "by-key"},
	}
	for _, tc := range cases {
		cm, err := extractSecretValues(state, []SecretMapping{{
			TerraformAddress: tc.addr, Attribute: "password", ConfigKey: "k",
		}})
		require.NoError(t, err, tc.addr)
		assert.Equal(t, tc.want, cm["k"].Value, tc.addr)
	}
}

func TestExtractSecretValues_RejectsCompositeValues(t *testing.T) {
	t.Parallel()

	state := []byte(`{"resources":[{"type":"aws_x","name":"n","mode":"managed","instances":[
		{"attributes":{"master_user_secret":[{"secret_arn":"arn:x"}]}}]}]}`)
	_, err := extractSecretValues(state, []SecretMapping{{
		TerraformAddress: "aws_x.n", Attribute: "master_user_secret", ConfigKey: "k",
	}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "master_user_secret")
	assert.NotContains(t, err.Error(), "arn:x", "the error must not echo the value")
}

func TestExtractSecretValues_RejectsTrailingData(t *testing.T) {
	t.Parallel()

	state := []byte(`{"resources":[]}{"resources":[]}`)
	_, err := extractSecretValues(state, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "trailing")
}
