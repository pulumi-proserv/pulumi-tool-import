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
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClassifyName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, logicalID, physicalID, cfnType string
		wantHashed, wantServer               bool
	}{
		{"cdk hashed policy", "TaskRoleDefaultPolicyDFEB0894", "AppStackTaskRoleDefaultPolicyDFEB0894", "AWS::IAM::Policy", true, false},
		{"cfn random role", "TaskRole30FC0FBB", "AppStack-TaskRole30FC0FBB-xQMUV6Ikl78Y", "AWS::IAM::Role", false, true},
		{"plain name", "ApiHandler", "app-dev-api", "AWS::Lambda::Function", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, hashed, server := ClassifyName(tc.logicalID, tc.physicalID, tc.cfnType)
			require.Equal(t, tc.wantHashed, hashed)
			require.Equal(t, tc.wantServer, server)
		})
	}
}
