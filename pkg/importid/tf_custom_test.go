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

func TestTFCustomKinesisStream(t *testing.T) {
	c := TFCustom["aws_kinesis_stream"]
	require.NotNil(t, c)
	// The state ID is the stream ARN; the import ID is the bare name.
	id, ok := c(map[string]interface{}{"name": "my-stream"}, "arn:aws:kinesis:us-east-1:1:stream/my-stream")
	assert.True(t, ok)
	assert.Equal(t, "my-stream", id)
	_, ok = c(map[string]interface{}{}, "arn:aws:kinesis:us-east-1:1:stream/my-stream")
	assert.False(t, ok)
	_, ok = c(map[string]interface{}{"name": ""}, "arn")
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

// specsAgreementExceptions lists the types where Specs and the scraped table
// legitimately do not line up, with the reason. Anything not listed here is a
// hard failure: silently logging a disagreement is how a wrong composer
// survives a provider bump.
//
// Every reason below was checked against terraform-provider-aws v6.38.0.
var specsAgreementExceptions = map[string]string{
	// Specs is Custom only to split CFN's pipe-joined ScalingTargetId
	// (resourceId|scalableDimension|serviceNamespace); the value it composes,
	// parts[2]/parts[0]/parts[1], is exactly the scraped template. Not a
	// disagreement — see #80 for unifying the two vocabularies.
	"aws_appautoscaling_target": "Specs' Custom composer splits the CFN ScalingTargetId and yields the same {service_namespace}/{resource_id}/{scalable_dimension}",

	// KNOWN BUG in the CFN path, not in the table: composeScalingPolicy emits
	// name/namespace/resource/dimension, but the provider imports by
	// service-namespace/resource-id/scalable-dimension/policy-name
	// (website/docs/r/appautoscaling_policy.html.markdown, and
	// testAccPolicyImportStateIdFunc joins the attributes in that order).
	// The scraped template is the correct one. Fixing custom.go is out of
	// scope for the import-ID table work; tracked for the CFN path.
	"aws_appautoscaling_policy": "Specs' Custom composer puts the policy name first, which the provider's docs and import test contradict; the table is right and the CFN composer is a known bug",

	// Absent from the table because the provider's own SetId already produces
	// exactly what Specs composes, so in Terraform these are passthrough. The
	// CFN path still needs a composer because CloudFormation hands over the
	// pieces separately. Each state ID was read at v6.38.0:
	"aws_cloudwatch_log_group":                "id is the log group name (logs/group.go:134), which is what Specs' [logGroupName] composes",
	"aws_cloudwatch_metric_alarm":             "id is the alarm name (cloudwatch/metric_alarm.go:345), which is what Specs' [alarmName] composes",
	"aws_cloudwatch_event_bus":                "id is the event bus name (events/bus.go:154), which is what Specs' [name] composes",
	"aws_s3_bucket_policy":                    "id is the bucket name (s3/bucket_policy.go:76), which is what Specs' [bucket] composes",
	"aws_sqs_queue_policy":                    "id is the queue URL (sqs/attribute_funcs.go:61), which is what Specs' [queue] composes",
	"aws_transfer_user":                       "id is serverID/userName (transfer/user.go:300, separator \"/\"), which is what Specs' [server, user] composes",
	"aws_lb_listener_certificate":             "id is listenerARN_certificateARN (elbv2/listener_certificate.go:149, separator \"_\"), which is what Specs' [listener, certificate] composes",
	"aws_lambda_function_event_invoke_config": "id is functionName, or functionName:qualifier when a qualifier is set (lambda/function_event_invoke_config.go:113-116) — the shape Specs' [function, qualifier] composes",

	// Manual in the table, Classic in Specs. The table is manual because the
	// composition is a conditional the scraper's whitelist refuses, and
	// TFCustom carries the conditional composer; Specs' Classic join covers
	// only one branch of it.
	"aws_route":                   "the import ID picks whichever destination attribute is set; TFCustom[\"aws_route\"] handles the conditional, Specs' [routeTable, cidr] covers only the IPv4 branch",
	"aws_route_table_association": "the import ID is subnet-or-gateway/routeTable; TFCustom handles the conditional, Specs' [subnet, routeTable] covers only the subnet branch",
	"aws_lambda_permission":       "the import ID inserts :qualifier only when a qualifier is set; TFCustom handles the conditional, Specs' [function, statement] covers only the unqualified branch",

	// Manual for a different reason: the import helper is not a pure join.
	"aws_cognito_user_pool_client": "the import helper calls the Cognito API before composing (cognitoidp/user_pool_client_test.go:1476), so the whitelist refuses it; the value it returns is fmt.Sprintf(\"%s/%s\", user_pool_id, id), which agrees with Specs",
}

// Where the CFN-era Specs and the scraped table both speak about a type,
// they must agree; where Specs has a Custom composer, the table must be
// manual. A disagreement is a real finding about one of the two tables, so it
// fails unless it is listed in specsAgreementExceptions with a reason.
func TestSpecsAgreeWithTable(t *testing.T) {
	f := Embedded()
	// An exception that no longer suppresses anything is stale: the
	// disagreement it explains has been fixed, and the entry must go.
	used := map[string]bool{}
	except := func(tfType string) bool {
		_, ok := specsAgreementExceptions[tfType]
		if ok {
			used[tfType] = true
		}
		return ok
	}
	for tok, spec := range Specs {
		tfType, ok := specsTFType[tok]
		require.Truef(t, ok, "add %s to specsTFType", tok)
		e, inTable := f.Types[tfType]
		if spec.Custom != nil {
			if inTable && !e.Manual && !except(tfType) {
				t.Errorf("%s: Specs has a Custom composer but the table has template %q — "+
					"fix one of them, or add %s to specsAgreementExceptions with the reason", tfType, e.Template, tfType)
			}
			continue
		}
		if !inTable {
			// Specs composes it; the provider's tests say the state ID is
			// already the import ID. One of the two is wrong.
			if !except(tfType) {
				t.Errorf("%s: in Specs (%v joined by %q) but not in the table — "+
					"the provider's tests prove passthrough, so either Specs is wrong or this needs "+
					"an entry in specsAgreementExceptions with the reason", tfType, spec.Classic, spec.ClassicDelim)
			}
			continue
		}
		if e.Manual {
			if !except(tfType) {
				t.Errorf("%s: Specs composes it from %v but the table marks it manual (%s) — "+
					"reconcile them or add %s to specsAgreementExceptions with the reason",
					tfType, spec.Classic, e.Evidence, tfType)
			}
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
	for tfType := range specsAgreementExceptions {
		assert.Truef(t, used[tfType], "specsAgreementExceptions[%q] is stale: nothing disagrees any more, delete it", tfType)
	}
}
