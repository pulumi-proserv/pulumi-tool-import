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
	"testing"

	"github.com/pulumi-proserv/pulumi-tool-import/importids/catalog"
)

func TestCatalogAndComposers(t *testing.T) {
	c := Load()
	if c.Formats.PulumiVersion != "v7.48.0" || c.Formats.Version != "v6.66.0" {
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
		{"aws_api_gateway_method", map[string]interface{}{"rest_api_id": "api-1", "resource_id": "resource-1", "http_method": "GET"}, "api-1/resource-1/GET"},
		{"aws_security_group_rule", map[string]interface{}{"security_group_id": "sg-1", "type": "egress", "protocol": "-1", "from_port": 0, "to_port": 0, "self": true}, "sg-1_egress_-1_0_0_self"},
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
	// The newer upstream proves this template; retaining the old composer
	// would hide the fact that its implementation is no longer needed here.
	if c.Composers["aws_kinesis_stream"] != nil {
		t.Fatal("Kinesis must use the generated template")
	}
	got, err := catalog.Expand(c.Formats.Types["aws_kinesis_stream"].Template, map[string]interface{}{"name": "events"}, "arn:aws:kinesis:us-east-1:123:stream/events")
	if err != nil || got != "events" {
		t.Fatalf("Kinesis import ID: %q, %v", got, err)
	}
	delete(c.Composers, "aws_route")
	delete(c.Formats.Types, "aws_route")
	if Load().Composers["aws_route"] == nil || !Load().Formats.Types["aws_route"].Manual {
		t.Fatal("catalog loads share mutable state")
	}
}
