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
	"fmt"
)

// Lookups performs live AWS calls needed to resolve import IDs for CFN
// resource types whose ID cannot be derived from template attributes alone.
type Lookups interface {
	SecurityGroupRuleID(ctx context.Context, egress bool, props map[string]interface{}) (string, error)
	EIPAllocationID(ctx context.Context, publicIP string) (string, error)
	InternetGatewayAttachment(ctx context.Context, igwID string) (string, error)
}

func str(attrs map[string]interface{}, key string) string {
	if v, ok := attrs[key]; ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

// LookupImportID resolves the import ID for the CFN types whose ID needs a live
// AWS call. isLookupType is false for every other type (composed later from
// attributes via the shared spec core).
//
// Note: CFN "AWS::IAM::Policy" (an inline policy) is resolved in the digest
// builder (see inlineRolePolicyImportID), not here — its RoleName:PolicyName
// import ID comes from resolved template attributes with no AWS call. Only the
// single-role case is auto-resolved; a policy bound to multiple roles or to
// users/groups is left for manual handling.
func LookupImportID(ctx context.Context, cfnType string, attrs map[string]interface{}, lk Lookups) (string, bool, error) {
	switch cfnType {
	case "AWS::EC2::SecurityGroupIngress":
		id, err := lk.SecurityGroupRuleID(ctx, false, attrs)
		return id, true, err
	case "AWS::EC2::SecurityGroupEgress":
		id, err := lk.SecurityGroupRuleID(ctx, true, attrs)
		return id, true, err
	case "AWS::EC2::EIP":
		id, err := lk.EIPAllocationID(ctx, str(attrs, "PublicIp"))
		return id, true, err
	case "AWS::EC2::VPCGatewayAttachment":
		id, err := lk.InternetGatewayAttachment(ctx, str(attrs, "InternetGatewayId"))
		return id, true, err
	}
	return "", false, nil
}
