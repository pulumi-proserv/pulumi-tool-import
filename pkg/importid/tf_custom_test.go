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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTFCustomRoute(t *testing.T) {
	c := TFCustom["aws_route"]
	require.NotNil(t, c)
	id, ok := c(map[string]interface{}{"route_table_id": "rtb-1", "destination_cidr_block": "10.0.0.0/16"}, "r-x")
	assert.True(t, ok)
	assert.Equal(t, "rtb-1_10.0.0.0/16", id)
	id, ok = c(map[string]interface{}{"route_table_id": "rtb-1", "destination_ipv6_cidr_block": "::/0"}, "r-x")
	assert.True(t, ok)
	assert.Equal(t, "rtb-1_::/0", id)
	id, ok = c(map[string]interface{}{"route_table_id": "rtb-1", "destination_prefix_list_id": "pl-1"}, "r-x")
	assert.True(t, ok)
	assert.Equal(t, "rtb-1_pl-1", id)
	_, ok = c(map[string]interface{}{"route_table_id": "rtb-1"}, "r-x")
	assert.False(t, ok)
}

func TestTFCustomSecurityGroupRule(t *testing.T) {
	c := TFCustom["aws_security_group_rule"]
	require.NotNil(t, c)
	id, ok := c(map[string]interface{}{
		"security_group_id": "sg-1", "type": "ingress", "protocol": "tcp",
		"from_port": 443, "to_port": 443, "cidr_blocks": []interface{}{"10.0.0.0/8", "10.1.0.0/16"},
	}, "sgrule-1")
	assert.True(t, ok)
	assert.Equal(t, "sg-1_ingress_tcp_443_443_10.0.0.0/8_10.1.0.0/16", id)
	id, ok = c(map[string]interface{}{
		"security_group_id": "sg-1", "type": "egress", "protocol": "-1",
		"from_port": 0, "to_port": 0, "self": true,
	}, "sgrule-1")
	assert.True(t, ok)
	assert.Equal(t, "sg-1_egress_-1_0_0_self", id)
}

func TestTFCustomEcsService(t *testing.T) {
	c := TFCustom["aws_ecs_service"]
	require.NotNil(t, c)
	id, ok := c(map[string]interface{}{"cluster": "arn:aws:ecs:us-east-1:1:cluster/c1", "name": "svc"}, "")
	assert.True(t, ok)
	assert.Equal(t, "c1/svc", id)
	id, ok = c(map[string]interface{}{"cluster": "c1", "name": "svc"}, "")
	assert.True(t, ok)
	assert.Equal(t, "c1/svc", id)
}

func TestTFCustomRouteTableAssociation(t *testing.T) {
	c := TFCustom["aws_route_table_association"]
	require.NotNil(t, c)
	id, ok := c(map[string]interface{}{"subnet_id": "subnet-1", "route_table_id": "rtb-1"}, "rtbassoc-1")
	assert.True(t, ok)
	assert.Equal(t, "subnet-1/rtb-1", id)
	// A gateway association has no subnet_id; the old switch composed
	// "/rtb-1" for this shape.
	id, ok = c(map[string]interface{}{"gateway_id": "igw-1", "route_table_id": "rtb-1"}, "rtbassoc-1")
	assert.True(t, ok)
	assert.Equal(t, "igw-1/rtb-1", id)
	_, ok = c(map[string]interface{}{"route_table_id": "rtb-1"}, "rtbassoc-1")
	assert.False(t, ok)
}

func TestTFCustomLambdaPermission(t *testing.T) {
	c := TFCustom["aws_lambda_permission"]
	require.NotNil(t, c)
	id, ok := c(map[string]interface{}{"function_name": "fn", "statement_id": "s1"}, "")
	assert.True(t, ok)
	assert.Equal(t, "fn/s1", id)
	// A qualified permission; the old switch dropped the qualifier.
	id, ok = c(map[string]interface{}{"function_name": "fn", "qualifier": "live", "statement_id": "s1"}, "")
	assert.True(t, ok)
	assert.Equal(t, "fn:live/s1", id)
	_, ok = c(map[string]interface{}{"function_name": "fn"}, "")
	assert.False(t, ok)
}

// Every custom composer's type must be a manual entry in the table, so the
// scrape can flag a hand-written composer whose type gains a provable
// template. Runs against the embedded table.
func TestTFCustomTypesAreManualInTable(t *testing.T) {
	f := Embedded()
	for typ := range TFCustom {
		e, ok := f.Types[typ]
		assert.Truef(t, ok, "%s has a custom composer but no table entry", typ)
		assert.Truef(t, e.Manual, "%s has a custom composer but the table has a template %q — delete the composer or fix the scrape", typ, e.Template)
	}
}

// tfRoleNames is the Terraform projection of the Role vocabulary Specs uses
// for CFN, so the two tables can be compared until #80 unifies them.
var tfRoleNames = map[Role]string{
	RoleFunction: "function_name", RoleStatement: "statement_id", RoleRestApi: "rest_api_id",
	RoleID: "id", RoleResource: "resource_id", RoleHTTP: "http_method", RoleUsagePlan: "usage_plan_id",
	RoleKey: "key_id", RoleUserPool: "user_pool_id", RoleSubnet: "subnet_id", RoleRouteTbl: "route_table_id",
	RoleServer: "server_id", RoleUser: "user_name", RoleQualifier: "qualifier", RoleListener: "listener_arn",
	RoleCert: "certificate_arn", RoleBucket: "bucket", RoleQueue: "queue_url", RoleHostZone: "zone_id",
	RoleName: "name", RoleType: "type", RoleSetID: "set_identifier", RoleStage: "stage_name",
	RoleCidr: "destination_cidr_block", RoleDevice: "device_name", RoleVolume: "volume_id",
	RoleInstance: "instance_id", RoleLogGroupName: "name", RoleAlarmName: "alarm_name",
}

// specsTFType maps the Pulumi tokens Specs is keyed by to Terraform types.
var specsTFType = map[string]string{
	"aws:lambda/permission:Permission":                               "aws_lambda_permission",
	"aws:apigateway/resource:Resource":                               "aws_api_gateway_resource",
	"aws:apigateway/deployment:Deployment":                           "aws_api_gateway_deployment",
	"aws:apigateway/method:Method":                                   "aws_api_gateway_method",
	"aws:apigateway/usagePlanKey:UsagePlanKey":                       "aws_api_gateway_usage_plan_key",
	"aws:apigateway/stage:Stage":                                     "aws_api_gateway_stage",
	"aws:apigateway/authorizer:Authorizer":                           "aws_api_gateway_authorizer",
	"aws:cognito/userPoolClient:UserPoolClient":                      "aws_cognito_user_pool_client",
	"aws:ec2/routeTableAssociation:RouteTableAssociation":            "aws_route_table_association",
	"aws:transfer/user:User":                                         "aws_transfer_user",
	"aws:lambda/functionEventInvokeConfig:FunctionEventInvokeConfig": "aws_lambda_function_event_invoke_config",
	"aws:lb/listenerCertificate:ListenerCertificate":                 "aws_lb_listener_certificate",
	"aws:s3/bucketPolicy:BucketPolicy":                               "aws_s3_bucket_policy",
	"aws:sqs/queuePolicy:QueuePolicy":                                "aws_sqs_queue_policy",
	"aws:route53/record:Record":                                      "aws_route53_record",
	"aws:appautoscaling/policy:Policy":                               "aws_appautoscaling_policy",
	"aws:appautoscaling/target:Target":                               "aws_appautoscaling_target",
	"aws:ecs/service:Service":                                        "aws_ecs_service",
	"aws:transfer/server:Server":                                     "aws_transfer_server",
	"aws:lambda/layerVersionPermission:LayerVersionPermission":       "aws_lambda_layer_version_permission",
	"aws:ec2/route:Route":                                            "aws_route",
	"aws:cloudwatch/logGroup:LogGroup":                               "aws_cloudwatch_log_group",
	"aws:cloudwatch/metricAlarm:MetricAlarm":                         "aws_cloudwatch_metric_alarm",
	"aws:cloudwatch/eventRule:EventRule":                             "aws_cloudwatch_event_rule",
	"aws:cloudwatch/eventBus:EventBus":                               "aws_cloudwatch_event_bus",
	"aws:ec2/volumeAttachment:VolumeAttachment":                      "aws_volume_attachment",
}

// specsAliasTemplate records, per type, a scraped template that names the same
// values as Specs under different attribute names — not a disagreement to fix.
//
// aws_api_gateway_usage_plan_key: the provider's importer sets both key_id and
// the resource id from the same usagePlanKeyId
// (internal/service/apigateway/usage_plan_key.go:42-44), so {usage_plan_id}/{id}
// and Specs' {usage_plan_id}/{key_id} always expand identically. The provider's
// test is the authority for the table; unifying the two vocabularies is #80.
var specsAliasTemplate = map[string]string{
	"aws_api_gateway_usage_plan_key": "{usage_plan_id}/{id}",
}

// Where the CFN-era Specs and the scraped table both speak about a type,
// they must agree; where Specs has a Custom composer, the table must be
// manual. A disagreement is a real finding about one of the two tables.
func TestSpecsAgreeWithTable(t *testing.T) {
	f := Embedded()
	for tok, spec := range Specs {
		tfType, ok := specsTFType[tok]
		require.Truef(t, ok, "add %s to specsTFType", tok)
		e, inTable := f.Types[tfType]
		if spec.Custom != nil {
			if inTable {
				assert.Truef(t, e.Manual, "%s: Specs has a Custom composer but the table has template %q", tfType, e.Template)
			}
			continue
		}
		if !inTable {
			// Specs composes it; the provider's tests say passthrough. Either
			// the state ID already has this shape (fine) or Specs is wrong.
			t.Logf("NOTE %s: in Specs (%v joined by %q) but not in the table", tfType, spec.Classic, spec.ClassicDelim)
			continue
		}
		parts := make([]string, len(spec.Classic))
		for i, r := range spec.Classic {
			parts[i] = "{" + tfRoleNames[r] + "}"
		}
		want := strings.Join(parts, spec.ClassicDelim)
		if e.Template != "" && e.Template != specsAliasTemplate[tfType] {
			assert.Equalf(t, want, e.Template, "%s: Specs and the scraped table disagree", tfType)
		}
	}
}
