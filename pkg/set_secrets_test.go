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
