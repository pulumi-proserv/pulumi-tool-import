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
)

func TestParseURN(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		urn      string
		expected ResourceKey
		ok       bool
	}{
		{
			name:     "bare resource",
			urn:      "urn:pulumi:dev::proj::aws:s3/bucket:Bucket::my-bucket",
			expected: ResourceKey{Type: "aws:s3/bucket:Bucket", Name: "my-bucket"},
			ok:       true,
		},
		{
			name:     "component child takes the leaf type",
			urn:      "urn:pulumi:dev::proj::example:index:Vpc$aws:ec2/vpc:Vpc::vpc-main",
			expected: ResourceKey{Type: "aws:ec2/vpc:Vpc", Name: "vpc-main"},
			ok:       true,
		},
		{
			name:     "nested components take the final leaf type",
			urn:      "urn:pulumi:dev::proj::a:index:A$b:index:B$aws:s3/bucket:Bucket::b1",
			expected: ResourceKey{Type: "aws:s3/bucket:Bucket", Name: "b1"},
			ok:       true,
		},
		{
			name:     "name containing brackets",
			urn:      `urn:pulumi:dev::proj::aws:ec2/subnet:Subnet::vpc-public[0]`,
			expected: ResourceKey{Type: "aws:ec2/subnet:Subnet", Name: "vpc-public[0]"},
			ok:       true,
		},
		{
			name: "stack resource is still parseable",
			urn:  "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev",
			expected: ResourceKey{
				Type: "pulumi:pulumi:Stack",
				Name: "proj-dev",
			},
			ok: true,
		},
		{
			name:     "dollar sign inside resource name",
			urn:      "urn:pulumi:dev::proj::aws:s3/bucket:Bucket::my$bucket",
			expected: ResourceKey{Type: "aws:s3/bucket:Bucket", Name: "my$bucket"},
			ok:       true,
		},
		{name: "trailing dollar sign in typePath", urn: "urn:pulumi:dev::proj::aws:s3/bucket:Bucket$::b1", ok: false},
		{name: "too few segments", urn: "urn:pulumi:dev::proj", ok: false},
		{name: "empty", urn: "", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := ParseURN(tt.urn)
			assert.Equal(t, tt.ok, ok)
			if tt.ok {
				assert.Equal(t, tt.expected, got)
			}
		})
	}
}
