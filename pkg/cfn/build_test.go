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

package cfn

import (
	"context"
	"os"
	"testing"

	"github.com/hexops/autogold/v2"
	"github.com/stretchr/testify/require"
)

type fakeStack struct {
	template  string
	resources []StackResource
}

func (f fakeStack) GetTemplate(_ context.Context, _ string) (string, error) { return f.template, nil }
func (f fakeStack) ListStackResources(_ context.Context, _ string) ([]StackResource, error) {
	return f.resources, nil
}
func (f fakeStack) GetExports(_ context.Context) (map[string]string, error) { return nil, nil }

func TestBuildDigest_Golden(t *testing.T) {
	t.Parallel()
	tmpl, err := os.ReadFile("testdata/ffs-min.template.json")
	require.NoError(t, err)
	sr := fakeStack{
		template: string(tmpl),
		resources: []StackResource{
			{LogicalID: "ApiPermission", PhysicalID: "ffs-dev-api-AllowApiGw", CfnType: "AWS::Lambda::Permission"},
			{LogicalID: "ApiHandler", PhysicalID: "ffs-dev-api", CfnType: "AWS::Lambda::Function"},
			{LogicalID: "MetaData", PhysicalID: "n/a", CfnType: "AWS::CDK::Metadata"},
		},
	}
	digest, err := BuildDigest(context.Background(), "ffs-dev", "us-east-1", sr, nil, nil)
	require.NoError(t, err)
	autogold.ExpectFile(t, digest)
}
