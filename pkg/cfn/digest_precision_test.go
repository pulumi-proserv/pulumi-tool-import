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
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildDigest_PreservesLargeIntegersFromTemplate(t *testing.T) {
	t.Parallel()

	sr := fakeStack{
		template: `{"Resources":{"Q":{"Type":"AWS::SQS::Queue",` +
			`"Properties":{"QueueName":"q","BigId":9007199254740993}}}}`,
		resources: []StackResource{
			{LogicalID: "Q", PhysicalID: "q", CfnType: "AWS::SQS::Queue"},
		},
	}
	digest, err := BuildDigest(context.Background(), "s", "us-east-1", sr, nil, nil)
	require.NoError(t, err)
	require.Len(t, digest.Resources, 1)

	out, err := json.Marshal(digest.Resources[0].Attributes)
	require.NoError(t, err)
	assert.Contains(t, string(out), "9007199254740993",
		"template properties must decode with UseNumber so digest attributes keep exact digits")
	assert.NotContains(t, string(out), "9007199254740992")
}
