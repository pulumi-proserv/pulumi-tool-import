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
	"aws_route":                   composeTFRoute,
	"aws_security_group_rule":     composeTFSecurityGroupRule,
	"aws_ecs_service":             composeTFEcsService,
	"aws_route_table_association": composeTFRouteTableAssociation,
	"aws_lambda_permission":       composeTFLambdaPermission,
	"aws_kinesis_stream":          composeTFKinesisStream,
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

// An association attaches a route table to either a subnet or a gateway; the
// import ID is TARGET/ROUTETABLEID for whichever one is set. The table marks
// this manual because that choice is a conditional, not a join.
func composeTFRouteTableAssociation(attrs map[string]interface{}, _ string) (string, bool) {
	rtb := str(attrs, "route_table_id")
	if rtb == "" {
		return "", false
	}
	target := str(attrs, "subnet_id")
	if target == "" {
		target = str(attrs, "gateway_id")
	}
	if target == "" {
		return "", false
	}
	return target + "/" + rtb, true
}

// A Kinesis stream's state ID is its ARN — the create sets it from
// StreamDescription.StreamARN (internal/service/kinesis/stream.go:215) — but
// it imports by the bare stream name ("import Kinesis Streams using the
// `name`", website/docs/r/kinesis_stream.html.markdown), which is what the
// provider's own import steps pass as ImportStateId. Without this composer
// the ARN would be handed to Pulumi as the import ID.
//
// This is a composer rather than a scraped template because the steps spell
// their ID as a variable (ImportStateId: rName), so the scraper can see that
// the type diverges but not what it composes.
func composeTFKinesisStream(attrs map[string]interface{}, _ string) (string, bool) {
	name := str(attrs, "name")
	if name == "" {
		return "", false
	}
	return name, true
}

// A permission on an aliased or versioned function imports as
// FUNCTIONNAME:QUALIFIER/STATEMENTID; without a qualifier the function name
// stands alone. Manual in the table for the same reason: a conditional.
func composeTFLambdaPermission(attrs map[string]interface{}, _ string) (string, bool) {
	fn, stmt := str(attrs, "function_name"), str(attrs, "statement_id")
	if fn == "" || stmt == "" {
		return "", false
	}
	if q := str(attrs, "qualifier"); q != "" {
		return fn + ":" + q + "/" + stmt, true
	}
	return fn + "/" + stmt, true
}
