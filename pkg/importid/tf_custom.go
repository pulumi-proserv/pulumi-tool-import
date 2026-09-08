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

package importid

import (
	"fmt"
	"strings"
)

// TFComposer builds an import ID from a resource's Terraform state
// attributes and state ID. ok=false means the attributes cannot support the
// format; the caller reports the type's documented shape instead.
type TFComposer func(attrs map[string]interface{}, stateID string) (id string, ok bool)

// TFCustom holds the Terraform-side composers for types whose import ID is
// not a pure join of attributes. Each is a manual entry in
// aws-import-id-formats.json; TestTFCustomTypesAreManualInTable enforces it.
var TFCustom = map[string]TFComposer{
	"aws_route":                      composeTFRoute,
	"aws_security_group_rule":        composeTFSecurityGroupRule,
	"aws_ecs_service":                composeTFEcsService,
	"aws_iam_role_policy_attachment": composeTFRolePolicyAttachment,
}

func str(attrs map[string]interface{}, k string) string {
	v, ok := attrs[k]
	if !ok || v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

// Exactly one destination attribute is set on a route; the import ID is
// ROUTETABLEID_DESTINATION.
func composeTFRoute(attrs map[string]interface{}, _ string) (string, bool) {
	rtb := str(attrs, "route_table_id")
	if rtb == "" {
		return "", false
	}
	for _, k := range []string{"destination_cidr_block", "destination_ipv6_cidr_block", "destination_prefix_list_id"} {
		if d := str(attrs, k); d != "" {
			return rtb + "_" + d, true
		}
	}
	return "", false
}

func composeTFSecurityGroupRule(attrs map[string]interface{}, _ string) (string, bool) {
	sg := str(attrs, "security_group_id")
	if sg == "" {
		return "", false
	}
	var src string
	if self, _ := attrs["self"].(bool); self {
		src = "self"
	} else if cidrs, ok := attrs["cidr_blocks"].([]interface{}); ok && len(cidrs) > 0 {
		parts := make([]string, len(cidrs))
		for i, c := range cidrs {
			parts[i] = fmt.Sprintf("%v", c)
		}
		src = strings.Join(parts, "_")
	} else {
		src = sg
	}
	return sg + "_" + str(attrs, "type") + "_" + str(attrs, "protocol") + "_" +
		str(attrs, "from_port") + "_" + str(attrs, "to_port") + "_" + src, true
}

func composeTFEcsService(attrs map[string]interface{}, _ string) (string, bool) {
	cluster, name := str(attrs, "cluster"), str(attrs, "name")
	if cluster == "" || name == "" {
		return "", false
	}
	if strings.Contains(cluster, "arn:") {
		parts := strings.Split(cluster, "/")
		cluster = parts[len(parts)-1]
	}
	return cluster + "/" + name, true
}

func composeTFRolePolicyAttachment(attrs map[string]interface{}, _ string) (string, bool) {
	role := str(attrs, "role")
	if role == "" {
		if roles, ok := attrs["roles"].([]interface{}); ok && len(roles) > 0 {
			role = fmt.Sprintf("%v", roles[0])
		}
	}
	arn := str(attrs, "policy_arn")
	if role == "" || arn == "" {
		return "", false
	}
	return role + "/" + arn, true
}
