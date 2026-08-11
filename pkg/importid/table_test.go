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
	"testing"

	"github.com/hexops/autogold/v2"
)

// TestResolverTable exercises one representative case per pure-composition
// Pulumi type in Specs, locking the full import-ID table via a golden file.
func TestResolverTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		pulumiType string
		provider   string
		roles      map[Role]string
	}{
		{"aws:lambda/permission:Permission", "classic", map[Role]string{RoleFunction: "fn", RoleStatement: "stmt"}},
		{"aws:apigateway/resource:Resource", "classic", map[Role]string{RoleRestApi: "api", RoleID: "res"}},
		{"aws:apigateway/resource:Resource", "native", map[Role]string{RoleRestApi: "api", RoleID: "res"}},
		{"aws:apigateway/deployment:Deployment", "classic", map[Role]string{RoleRestApi: "api", RoleID: "dep"}},
		{"aws:apigateway/deployment:Deployment", "native", map[Role]string{RoleRestApi: "api", RoleID: "dep"}},
		{"aws:apigateway/method:Method", "classic", map[Role]string{RoleRestApi: "api", RoleResource: "res", RoleHTTP: "GET"}},
		{"aws:apigateway/usagePlanKey:UsagePlanKey", "classic", map[Role]string{RoleUsagePlan: "up", RoleKey: "key"}},
		{"aws:apigateway/stage:Stage", "classic", map[Role]string{RoleRestApi: "api", RoleStage: "prod"}},
		{"aws:apigateway/authorizer:Authorizer", "classic", map[Role]string{RoleRestApi: "api", RoleID: "auth"}},
		{"aws:cognito/userPoolClient:UserPoolClient", "classic", map[Role]string{RoleUserPool: "pool", RoleID: "client"}},
		{"aws:ec2/routeTableAssociation:RouteTableAssociation", "classic", map[Role]string{RoleSubnet: "sub", RoleRouteTbl: "rtb"}},
		{"aws:transfer/user:User", "classic", map[Role]string{RoleServer: "s", RoleUser: "u"}},
		{"aws:transfer/server:Server", "classic", map[Role]string{RoleTransferID: "a/s-1"}},
		{"aws:ecs/service:Service", "classic", map[Role]string{RoleEcsID: "cluster/svc"}},
		{"aws:lb/listenerCertificate:ListenerCertificate", "classic", map[Role]string{RoleListener: "lsnr", RoleCert: "cert"}},
		{"aws:lambda/functionEventInvokeConfig:FunctionEventInvokeConfig", "classic", map[Role]string{RoleFunction: "fn", RoleQualifier: "1"}},
		{"aws:lambda/layerVersionPermission:LayerVersionPermission", "classic", map[Role]string{RoleLayerArn: "arn:l:1:2"}},
		{"aws:route53/record:Record", "classic", map[Role]string{RoleHostZone: "Z1", RoleName: "a.example.com", RoleType: "A"}},
		{"aws:appautoscaling/policy:Policy", "classic", map[Role]string{RoleName: "cpu", RoleScalingTargetID: "svc|rid|dim"}},
		{"aws:appautoscaling/target:Target", "classic", map[Role]string{RoleScalingTargetID: "svc|rid|dim"}},
		{"aws:s3/bucketPolicy:BucketPolicy", "classic", map[Role]string{RoleBucket: "bkt"}},
		{"aws:sqs/queuePolicy:QueuePolicy", "classic", map[Role]string{RoleQueue: "q-url"}},
		{"aws:ec2/route:Route", "classic", map[Role]string{RoleRouteTbl: "rtb", RoleCidr: "10.0.0.0/16"}},
		{"aws:cloudwatch/logGroup:LogGroup", "classic", map[Role]string{RoleLogGroupName: "/aws/lambda/fn"}},
		{"aws:cloudwatch/metricAlarm:MetricAlarm", "classic", map[Role]string{RoleAlarmName: "cpu-high"}},
		{"aws:cloudwatch/eventRule:EventRule", "classic", map[Role]string{RoleName: "rule"}},
		{"aws:cloudwatch/eventBus:EventBus", "classic", map[Role]string{RoleName: "bus"}},
		{"aws:ec2/volumeAttachment:VolumeAttachment", "classic", map[Role]string{RoleDevice: "/dev/sdh", RoleVolume: "vol-1", RoleInstance: "i-1"}},
	}
	result := map[string]string{}
	for _, tc := range cases {
		id, handled, err := Compose(tc.pulumiType, tc.provider, func(r Role) string { return tc.roles[r] })
		if err != nil || !handled {
			t.Fatalf("%s (%s): handled=%v err=%v", tc.pulumiType, tc.provider, handled, err)
		}
		key := tc.pulumiType
		if tc.provider == "native" {
			key += " (native)"
		}
		result[key] = id
	}
	autogold.ExpectFile(t, result)
}
