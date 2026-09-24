// Copyright 2016-2026, Pulumi Corporation.
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

package importid

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelectAWSByPulumiMajor(t *testing.T) {
	for _, tc := range []struct{ requested, pulumi, upstream string }{
		{"v6.83.2", "v6.83.4", "v5.100.0"},
		{"7.24.0", "v7.48.0", "v6.66.0"},
	} {
		c, err := ForAWSVersion(tc.requested, nil)
		require.NoError(t, err)
		assert.Equal(t, tc.pulumi, c.Formats.PulumiVersion)
		assert.Equal(t, tc.upstream, c.Formats.Version)
		assert.NotEmpty(t, c.Composers)
		assert.Empty(t, VersionWarning(tc.requested, c))
	}
	for _, version := range []string{"", "invalid", "5.0.0", "8.0.0"} {
		_, err := ForAWSVersion(version, nil)
		assert.Error(t, err)
	}
}

func TestAWSVersionEquivalentAddresses(t *testing.T) {
	for _, addr := range []string{"registry.terraform.io/hashicorp/aws", "registry.opentofu.org/hashicorp/aws", "hashicorp/aws"} {
		v, err := AWSVersion(map[string]string{addr: "aws@v6.83.4"})
		require.NoError(t, err)
		assert.Equal(t, "v6.83.4", v)
	}
	for _, providers := range []map[string]string{
		nil,
		{"registry.terraform.io/hashicorp/aws": "dynamic@6.66.0"},
		{"registry.terraform.io/hashicorp/aws": "aws@"},
		{"example.com/hashicorp/aws": "aws@v7.48.0"},
		{"registry.terraform.io/hashicorp/aws": "aws@v6.83.4", "registry.opentofu.org/hashicorp/aws": "aws@v7.48.0"},
	} {
		_, err := AWSVersion(providers)
		assert.Error(t, err)
	}
}

func TestCatalogOverrideCannotCrossMajors(t *testing.T) {
	v6, err := ForAWSVersion("6.83.4", nil)
	require.NoError(t, err)
	_, err = ForAWSVersion("7.48.0", v6.Formats)
	require.ErrorContains(t, err, "must declare pulumiVersion in major 7")
	v7, err := ForAWSVersion("7.48.0", nil)
	require.NoError(t, err)
	v7.Formats.Types["aws_test"] = FormatEntry{Template: "{id}/override", Evidence: "test"}
	override, err := ForAWSVersion("7.24.0", v7.Formats)
	require.NoError(t, err)
	assert.NotNil(t, override.Composers["aws_route"])
	fresh, err := ForAWSVersion("7.24.0", nil)
	require.NoError(t, err)
	assert.NotContains(t, fresh.Formats.Types, "aws_test")
	assert.Contains(t, VersionWarning("7.99.0", fresh), "v7.48.0")
	assert.Contains(t, VersionWarning("7.99.0", fresh), "update-import-id-formats")
	assert.NotNil(t, v6.Composers["aws_kinesis_stream"])
	assert.Nil(t, fresh.Composers["aws_kinesis_stream"])
	assert.NotNil(t, fresh.Composers["aws_api_gateway_method"])
	assert.Nil(t, v6.Composers["aws_api_gateway_method"])
}
