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
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStackDigest_JSONRoundTrip(t *testing.T) {
	t.Parallel()
	d := StackDigest{StackName: "ffs-dev", Region: "us-east-1", Resources: []CfnResource{{
		LogicalID: "MigrateFn", CfnType: "AWS::Lambda::Function", PhysicalID: "ffs-dev-migrate",
		PulumiType: "aws:lambda/function:Function",
		Attributes: map[string]interface{}{"FunctionName": "ffs-dev-migrate"},
	}}}
	data, err := json.Marshal(d)
	require.NoError(t, err)
	var got StackDigest
	require.NoError(t, json.Unmarshal(data, &got))
	require.Equal(t, d, got)
}
