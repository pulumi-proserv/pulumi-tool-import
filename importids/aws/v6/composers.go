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

package aws

import (
	"fmt"
	"strings"

	"github.com/pulumi-proserv/pulumi-tool-import/importids/catalog"
)

func composers() map[string]catalog.Composer {
	return map[string]catalog.Composer{
		"aws_route":                   composeRoute,
		"aws_security_group_rule":     composeSecurityGroupRule,
		"aws_ecs_service":             composeECSService,
		"aws_route_table_association": composeRouteTableAssociation,
		"aws_lambda_permission":       composeLambdaPermission,
		"aws_kinesis_stream":          composeKinesisStream,
	}
}

func str(attrs map[string]interface{}, key string) string {
	if attrs[key] == nil {
		return ""
	}
	return fmt.Sprint(attrs[key])
}

func composeRoute(attrs map[string]interface{}, _ string) (string, bool) {
	rtb := str(attrs, "route_table_id")
	if rtb == "" {
		return "", false
	}
	for _, key := range []string{"destination_cidr_block", "destination_ipv6_cidr_block", "destination_prefix_list_id"} {
		if dest := str(attrs, key); dest != "" {
			return rtb + "_" + dest, true
		}
	}
	return "", false
}

func composeSecurityGroupRule(attrs map[string]interface{}, _ string) (string, bool) {
	sg := str(attrs, "security_group_id")
	if sg == "" {
		return "", false
	}
	var source string
	if self, _ := attrs["self"].(bool); self {
		source = "self"
	} else if cidrs, ok := attrs["cidr_blocks"].([]interface{}); ok && len(cidrs) > 0 {
		parts := make([]string, len(cidrs))
		for i, cidr := range cidrs {
			parts[i] = fmt.Sprint(cidr)
		}
		source = strings.Join(parts, "_")
	} else {
		source = sg
	}
	return sg + "_" + str(attrs, "type") + "_" + str(attrs, "protocol") + "_" + str(attrs, "from_port") + "_" + str(attrs, "to_port") + "_" + source, true
}

func composeECSService(attrs map[string]interface{}, _ string) (string, bool) {
	cluster, name := str(attrs, "cluster"), str(attrs, "name")
	if cluster == "" || name == "" {
		return "", false
	}
	if strings.HasPrefix(cluster, "arn:") {
		parts := strings.Split(cluster, "/")
		cluster = parts[len(parts)-1]
	}
	return cluster + "/" + name, true
}

func composeRouteTableAssociation(attrs map[string]interface{}, _ string) (string, bool) {
	rtb, target := str(attrs, "route_table_id"), str(attrs, "subnet_id")
	if target == "" {
		target = str(attrs, "gateway_id")
	}
	if rtb == "" || target == "" {
		return "", false
	}
	return target + "/" + rtb, true
}

func composeLambdaPermission(attrs map[string]interface{}, _ string) (string, bool) {
	function, statement := str(attrs, "function_name"), str(attrs, "statement_id")
	if function == "" || statement == "" {
		return "", false
	}
	if qualifier := str(attrs, "qualifier"); qualifier != "" {
		function += ":" + qualifier
	}
	return function + "/" + statement, true
}

func composeKinesisStream(attrs map[string]interface{}, _ string) (string, bool) {
	name := str(attrs, "name")
	return name, name != ""
}
