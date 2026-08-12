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

package batchimport

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResourceKeysFromDeployment(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"resources":[
		{"urn":"urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev","type":"pulumi:pulumi:Stack"},
		{"urn":"urn:pulumi:dev::proj::example:index:Comp$aws:s3/bucket:Bucket::comp-b1","type":"aws:s3/bucket:Bucket"},
		{"urn":"urn:pulumi:dev::proj::aws:ec2/vpc:Vpc::vpc-main","type":"aws:ec2/vpc:Vpc"}
	]}`)

	keys, err := resourceKeysFromDeployment(raw)
	require.NoError(t, err)

	assert.True(t, keys[ResourceKey{Type: "aws:s3/bucket:Bucket", Name: "comp-b1"}])
	assert.True(t, keys[ResourceKey{Type: "aws:ec2/vpc:Vpc", Name: "vpc-main"}])
	assert.True(t, keys[ResourceKey{Type: "pulumi:pulumi:Stack", Name: "proj-dev"}])
	assert.Len(t, keys, 3)
}

func TestResourceKeysFromDeployment_EmptyAndInvalid(t *testing.T) {
	t.Parallel()

	keys, err := resourceKeysFromDeployment([]byte(`{}`))
	require.NoError(t, err)
	assert.Empty(t, keys)

	keys, err = resourceKeysFromDeployment(nil)
	require.NoError(t, err)
	assert.Empty(t, keys)

	_, err = resourceKeysFromDeployment([]byte(`{"resources": "nope"}`))
	require.Error(t, err)
}
