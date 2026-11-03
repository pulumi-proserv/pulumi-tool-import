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

package pkg

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// These inputs model older Terraform state, rather than assuming the source
// provider used the same state-ID format as the destination catalog's provider.
func TestHistoricalAWSImportIDs(t *testing.T) {
	t.Parallel()
	const policy = "arn:aws:iam::aws:policy/SecurityAudit"
	const trail = "arn:aws:cloudtrail:us-west-2:123456789012:trail/a-trail"
	cases := []struct {
		name, tfType, pulumiType, stateID, want string
		attrs                                   map[string]interface{}
		unverified                              int
	}{
		{"user", "aws_iam_user_policy_attachment", "aws:iam/userPolicyAttachment:UserPolicyAttachment", "a-user-20220101", "a-user/" + policy, map[string]interface{}{"user": "a-user", "policy_arn": policy}, 0},
		{"group", "aws_iam_group_policy_attachment", "aws:iam/groupPolicyAttachment:GroupPolicyAttachment", "a-group-20220101", "a-group/" + policy, map[string]interface{}{"group": "a-group", "policy_arn": policy}, 0},
		{"event-missing-bus", "aws_cloudwatch_event_target", "aws:cloudwatch/eventTarget:EventTarget", "a-rule-a-target", "a-rule/a-target", map[string]interface{}{"rule": "a-rule", "target_id": "a-target"}, 0},
		{"event-missing-target", "aws_cloudwatch_event_target", "aws:cloudwatch/eventTarget:EventTarget", "a-rule-a-target", "a-rule-a-target", map[string]interface{}{"rule": "a-rule"}, 1},
		{"trail-name", "aws_cloudtrail", "aws:cloudtrail/trail:Trail", "a-trail", trail, map[string]interface{}{"arn": trail}, 0},
		{"trail-arn", "aws_cloudtrail", "aws:cloudtrail/trail:Trail", trail, trail, nil, 0},
		{"trail-missing-arn", "aws_cloudtrail", "aws:cloudtrail/trail:Trail", "a-trail", "a-trail", nil, 1},
		{"trail-redacted-arn", "aws_cloudtrail", "aws:cloudtrail/trail:Trail", "a-trail", "a-trail", map[string]interface{}{"arn": "(sensitive)"}, 1},
		{"attachment-missing-policy", "aws_iam_user_policy_attachment", "aws:iam/userPolicyAttachment:UserPolicyAttachment", "a-user-20220101", "a-user-20220101", map[string]interface{}{"user": "a-user"}, 1},
		{"uncatalogued", "aws_unknown", "aws:unknown:Unknown", "old-id", "old-id", nil, 1},
	}
	for _, version := range []string{"6.83.4", "7.48.0", "7.51.1"} {
		for _, tc := range cases {
			t.Run(version+"/"+tc.name, func(t *testing.T) {
				digest := &ModuleMap{RootResources: []ModuleResource{{Mode: "managed", TerraformAddress: tc.tfType + ".example", ImportID: tc.stateID, Attributes: tc.attrs}}}
				file := &ImportFile{Resources: []ImportEntry{{Type: tc.pulumiType, Name: "example", ID: tc.stateID, Version: version}}}
				result := TranslateImportIDsForProviders(file, digest, nil)
				assert.Equal(t, tc.want, file.Resources[0].ID)
				assert.Equal(t, tc.unverified, result.Unverified)
				if tc.stateID != tc.want {
					assert.Equal(t, 1, result.Translated)
				} else {
					assert.Zero(t, result.Translated)
				}
				if tc.unverified > 0 && tc.name != "uncatalogued" {
					assert.Contains(t, result.Notes[len(result.Notes)-1], "cannot compose import ID")
				}
			})
		}
	}
}
