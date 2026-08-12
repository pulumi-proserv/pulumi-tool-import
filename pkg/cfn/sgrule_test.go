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

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/require"
)

func TestSgRuleMatches(t *testing.T) {
	t.Parallel()
	rule := ec2types.SecurityGroupRule{
		IsEgress:   aws.Bool(false),
		IpProtocol: aws.String("tcp"),
		FromPort:   aws.Int32(443),
		ToPort:     aws.Int32(443),
		CidrIpv4:   aws.String("10.0.0.0/16"),
	}

	// Full match (JSON numbers arrive as float64).
	require.True(t, sgRuleMatches(rule, map[string]interface{}{
		"IpProtocol": "tcp", "FromPort": float64(443), "ToPort": float64(443), "CidrIp": "10.0.0.0/16",
	}, false))

	// A subset of properties still matches (absent props don't constrain).
	require.True(t, sgRuleMatches(rule, map[string]interface{}{"IpProtocol": "tcp"}, false))

	// Port narrows: 80 must NOT match a 443 rule (this is the key improvement).
	require.False(t, sgRuleMatches(rule, map[string]interface{}{"FromPort": float64(80)}, false))

	// Egress flag must match.
	require.False(t, sgRuleMatches(rule, map[string]interface{}{}, true))

	// CIDR narrows.
	require.False(t, sgRuleMatches(rule, map[string]interface{}{"CidrIp": "0.0.0.0/0"}, false))

	// Referenced source SG.
	refRule := ec2types.SecurityGroupRule{
		IsEgress:            aws.Bool(false),
		ReferencedGroupInfo: &ec2types.ReferencedSecurityGroup{GroupId: aws.String("sg-abc")},
	}
	require.True(t, sgRuleMatches(refRule, map[string]interface{}{"SourceSecurityGroupId": "sg-abc"}, false))
	require.False(t, sgRuleMatches(refRule, map[string]interface{}{"SourceSecurityGroupId": "sg-xyz"}, false))

	// IPv6.
	v6 := ec2types.SecurityGroupRule{IsEgress: aws.Bool(true), CidrIpv6: aws.String("::/0")}
	require.True(t, sgRuleMatches(v6, map[string]interface{}{"CidrIpv6": "::/0"}, true))
	require.False(t, sgRuleMatches(v6, map[string]interface{}{"CidrIpv6": "2001:db8::/32"}, true))
}
