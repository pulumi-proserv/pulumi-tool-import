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
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// sgRuleMatches reports whether a live security-group rule matches the CFN
// ingress/egress properties. Every property PRESENT in props must agree with
// the rule; absent properties do not constrain. Matching on protocol, ports,
// IPv4/IPv6 CIDR, referenced source/destination SG, and prefix list narrows the
// candidate set so a group with several rules resolves to exactly one.
func sgRuleMatches(rule ec2types.SecurityGroupRule, props map[string]interface{}, egress bool) bool {
	if aws.ToBool(rule.IsEgress) != egress {
		return false
	}
	if !strAgrees(rule.IpProtocol, props["IpProtocol"]) {
		return false
	}
	if !strAgrees(rule.CidrIpv4, firstNonNil(props["CidrIp"], props["CidrIpv4"])) {
		return false
	}
	if !strAgrees(rule.CidrIpv6, props["CidrIpv6"]) {
		return false
	}
	if !intAgrees(rule.FromPort, props["FromPort"]) {
		return false
	}
	if !intAgrees(rule.ToPort, props["ToPort"]) {
		return false
	}
	var refSG *string
	if rule.ReferencedGroupInfo != nil {
		refSG = rule.ReferencedGroupInfo.GroupId
	}
	if !strAgrees(refSG, firstNonNil(props["SourceSecurityGroupId"], props["DestinationSecurityGroupId"])) {
		return false
	}
	if !strAgrees(rule.PrefixListId, firstNonNil(props["SourcePrefixListId"], props["DestinationPrefixListId"])) {
		return false
	}
	return true
}

func firstNonNil(vals ...interface{}) interface{} {
	for _, v := range vals {
		if v != nil {
			return v
		}
	}
	return nil
}

// strAgrees is true when prop is absent (nil) or its string form equals the
// rule's value.
func strAgrees(ruleVal *string, prop interface{}) bool {
	if prop == nil {
		return true
	}
	return aws.ToString(ruleVal) == fmt.Sprintf("%v", prop)
}

// intAgrees is true when prop is absent (nil) or, parsed as a number, equals the
// rule's port. A CFN port arrives as a JSON number (float64) or a string.
func intAgrees(ruleVal *int32, prop interface{}) bool {
	if prop == nil {
		return true
	}
	want, ok := toIndex(prop) // reuses the numeric parser (float64/int/string)
	if !ok {
		return false
	}
	return int(aws.ToInt32(ruleVal)) == want
}
