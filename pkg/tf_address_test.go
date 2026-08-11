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

	"github.com/stretchr/testify/require"
)

func TestPulumiNameFromTerraformAddress(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		address      string
		resourceType string
		expected     string
	}{
		{
			name:         "root module resource",
			address:      "aws_s3_bucket.example",
			resourceType: "aws_s3_bucket",
			expected:     "example",
		},
		{
			name:         "single module resource named this",
			address:      "module.s3_bucket.aws_s3_bucket.this",
			resourceType: "aws_s3_bucket",
			expected:     "s3_bucket",
		},
		{
			name:         "single module resource not named this",
			address:      "module.s3_bucket.aws_s3_bucket.main",
			resourceType: "aws_s3_bucket",
			expected:     "s3_bucket_main",
		},
		{
			name:         "nested module resource",
			address:      "module.outer.module.inner.aws_s3_bucket.mybucket",
			resourceType: "aws_s3_bucket",
			expected:     "outer_inner_mybucket",
		},
		{
			name:         "nested module resource named this",
			address:      "module.outer.module.inner.aws_s3_bucket.this",
			resourceType: "aws_s3_bucket",
			expected:     "outer_inner",
		},
		{
			name:         "module with same name as resource",
			address:      "module.bucket.aws_s3_bucket.bucket",
			resourceType: "aws_s3_bucket",
			expected:     "bucket_bucket",
		},
		{
			name:         "module with module name",
			address:      "module.module.aws_s3_bucket.bucket",
			resourceType: "aws_s3_bucket",
			expected:     "module_bucket",
		},
		{
			name:         "root resource named this stays",
			address:      "aws_s3_bucket.this",
			resourceType: "aws_s3_bucket",
			expected:     "this",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := PulumiNameFromTerraformAddress(tc.address, tc.resourceType)
			require.Equal(t, tc.expected, result)
		})
	}
}
