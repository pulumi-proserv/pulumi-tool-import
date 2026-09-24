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

import "testing"

func TestCatalogAndComposers(t *testing.T) {
	c := Load()
	if c.Formats.PulumiVersion != "v6.83.4" || c.Formats.Version != "v5.100.0" {
		t.Fatal("wrong generation pin")
	}
	for typ := range c.Composers {
		if !c.Formats.Types[typ].Manual {
			t.Errorf("%s must remain manual while it has a composer", typ)
		}
	}
	for _, tc := range []struct {
		typ   string
		attrs map[string]interface{}
		want  string
	}{
		{"aws_route", map[string]interface{}{"route_table_id": "rtb-1", "destination_cidr_block": "0.0.0.0/0"}, "rtb-1_0.0.0.0/0"},
		{"aws_route", map[string]interface{}{"route_table_id": "rtb-1", "destination_ipv6_cidr_block": "::/0"}, "rtb-1_::/0"},
		{"aws_route", map[string]interface{}{"route_table_id": "rtb-1", "destination_prefix_list_id": "pl-1"}, "rtb-1_pl-1"},
		{"aws_route_table_association", map[string]interface{}{"route_table_id": "rtb-1", "subnet_id": "subnet-1"}, "subnet-1/rtb-1"},
		{"aws_route_table_association", map[string]interface{}{"route_table_id": "rtb-1", "gateway_id": "igw-1"}, "igw-1/rtb-1"},
		{"aws_ecs_service", map[string]interface{}{"cluster": "arn:aws:ecs:us-east-1:123:cluster/prod", "name": "api"}, "prod/api"},
		{"aws_lambda_permission", map[string]interface{}{"function_name": "fn", "statement_id": "stmt", "qualifier": "live"}, "fn:live/stmt"},
		{"aws_lambda_permission", map[string]interface{}{"function_name": "fn", "statement_id": "stmt"}, "fn/stmt"},
		{"aws_kinesis_stream", map[string]interface{}{"name": "events"}, "events"},
		{"aws_security_group_rule", map[string]interface{}{"security_group_id": "sg-1", "type": "ingress", "protocol": "tcp", "from_port": 443, "to_port": 443, "cidr_blocks": []interface{}{"10.0.0.0/8", "10.1.0.0/16"}}, "sg-1_ingress_tcp_443_443_10.0.0.0/8_10.1.0.0/16"},
	} {
		t.Run(tc.typ+tc.want, func(t *testing.T) {
			got, ok := c.Composers[tc.typ](tc.attrs, "opaque")
			if !ok || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, ok, tc.want)
			}
		})
	}
	for typ, compose := range c.Composers {
		if _, ok := compose(nil, "opaque"); ok {
			t.Errorf("%s must reject missing attributes", typ)
		}
	}
	delete(c.Composers, "aws_route")
	delete(c.Formats.Types, "aws_route")
	if Load().Composers["aws_route"] == nil || !Load().Formats.Types["aws_route"].Manual {
		t.Fatal("catalog loads share mutable state")
	}
}
