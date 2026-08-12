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

// Role is a source-agnostic logical field name. Per-source adapters map roles
// to their own attribute names (TF: function_name; CFN: FunctionName).
type Role string

const (
	RoleFunction  Role = "function"
	RoleStatement Role = "statement"
	RoleRestApi   Role = "restApi"
	RoleID        Role = "id"
	RoleResource  Role = "resource"
	RoleHTTP      Role = "httpMethod"
	RoleUsagePlan Role = "usagePlan"
	RoleKey       Role = "key"
	RoleUserPool  Role = "userPool"
	RoleSubnet    Role = "subnet"
	RoleRouteTbl  Role = "routeTable"
	RoleServer    Role = "server"
	RoleUser      Role = "user"
	RoleQualifier Role = "qualifier"
	RoleListener  Role = "listener"
	RoleCert      Role = "certificate"
	RoleBucket    Role = "bucket"
	RoleQueue     Role = "queue"
	RoleHostZone  Role = "hostedZone"
	RoleName      Role = "name"
	RoleType      Role = "recordType"
	RoleSetID     Role = "setIdentifier"
	RoleStage     Role = "stage"

	RoleScalingTargetID Role = "scalingTargetId"
	RoleEcsID           Role = "ecsId"
	RoleTransferID      Role = "transferId"
	RoleLayerArn        Role = "layerArn"

	RoleCidr         Role = "cidr"
	RoleDevice       Role = "device"
	RoleVolume       Role = "volume"
	RoleInstance     Role = "instance"
	RoleLogGroupName Role = "logGroupName"
	RoleAlarmName    Role = "alarmName"
)

// IDSpec describes how to compose an import ID for a Pulumi type. Classic is
// the aws-classic format; Native (optional) is the aws-native format for the
// API Gateway family. Custom overrides both for reorder/split cases.
type IDSpec struct {
	Classic      []Role
	ClassicDelim string
	Native       []Role
	NativeDelim  string
	Custom       func(get func(Role) string, provider string) (string, error)
}

// Specs is keyed by Pulumi type token. Only pure-composition types appear here;
// AWS-lookup types are pre-resolved in the digest step.
var Specs = map[string]IDSpec{
	"aws:lambda/permission:Permission":         {Classic: []Role{RoleFunction, RoleStatement}, ClassicDelim: "/"},
	"aws:apigateway/resource:Resource":         {Classic: []Role{RoleRestApi, RoleID}, ClassicDelim: "/", Native: []Role{RoleRestApi, RoleID}, NativeDelim: "|"},
	"aws:apigateway/deployment:Deployment":     {Classic: []Role{RoleRestApi, RoleID}, ClassicDelim: "/", Native: []Role{RoleID, RoleRestApi}, NativeDelim: "|"}, // native reversed
	"aws:apigateway/method:Method":             {Classic: []Role{RoleRestApi, RoleResource, RoleHTTP}, ClassicDelim: "/", Native: []Role{RoleRestApi, RoleResource, RoleHTTP}, NativeDelim: "|"},
	"aws:apigateway/usagePlanKey:UsagePlanKey": {Classic: []Role{RoleUsagePlan, RoleKey}, ClassicDelim: "/", Native: []Role{RoleUsagePlan, RoleKey}, NativeDelim: "|"},
	"aws:apigateway/stage:Stage":               {Native: []Role{RoleRestApi, RoleStage}, NativeDelim: "|", Classic: []Role{RoleRestApi, RoleStage}, ClassicDelim: "/"},
	// Authorizer import ID is RestApiId/<authorizer-id>, where the authorizer id
	// is the resource's physical id (exposed as CFN "Id") — there is no
	// "AuthorizerId" template property. Same shape as Resource/Deployment.
	"aws:apigateway/authorizer:Authorizer":                           {Native: []Role{RoleRestApi, RoleID}, NativeDelim: "|", Classic: []Role{RoleRestApi, RoleID}, ClassicDelim: "/"},
	"aws:cognito/userPoolClient:UserPoolClient":                      {Classic: []Role{RoleUserPool, RoleID}, ClassicDelim: "/"},
	"aws:ec2/routeTableAssociation:RouteTableAssociation":            {Classic: []Role{RoleSubnet, RoleRouteTbl}, ClassicDelim: "/"},
	"aws:transfer/user:User":                                         {Classic: []Role{RoleServer, RoleUser}, ClassicDelim: "/"},
	"aws:lambda/functionEventInvokeConfig:FunctionEventInvokeConfig": {Classic: []Role{RoleFunction, RoleQualifier}, ClassicDelim: ":"},
	"aws:lb/listenerCertificate:ListenerCertificate":                 {Classic: []Role{RoleListener, RoleCert}, ClassicDelim: "_"},
	"aws:s3/bucketPolicy:BucketPolicy":                               {Classic: []Role{RoleBucket}, ClassicDelim: ""},
	"aws:sqs/queuePolicy:QueuePolicy":                                {Classic: []Role{RoleQueue}, ClassicDelim: ""},
	"aws:route53/record:Record":                                      {Custom: composeRoute53},
	"aws:appautoscaling/policy:Policy":                               {Custom: composeScalingPolicy},
	"aws:appautoscaling/target:Target":                               {Custom: composeScalableTarget},
	"aws:ecs/service:Service":                                        {Custom: composeEcsService},
	"aws:transfer/server:Server":                                     {Custom: composeTransferServer},
	"aws:lambda/layerVersionPermission:LayerVersionPermission":       {Custom: composeLayerVersionPermission},
	"aws:ec2/route:Route":                                            {Classic: []Role{RoleRouteTbl, RoleCidr}, ClassicDelim: "_"},

	"aws:cloudwatch/logGroup:LogGroup":          {Classic: []Role{RoleLogGroupName}, ClassicDelim: ""},
	"aws:cloudwatch/metricAlarm:MetricAlarm":    {Classic: []Role{RoleAlarmName}, ClassicDelim: ""},
	"aws:cloudwatch/eventRule:EventRule":        {Classic: []Role{RoleName}, ClassicDelim: ""}, // default event bus; a custom bus needs busName/ruleName (manual)
	"aws:cloudwatch/eventBus:EventBus":          {Classic: []Role{RoleName}, ClassicDelim: ""},
	"aws:ec2/volumeAttachment:VolumeAttachment": {Classic: []Role{RoleDevice, RoleVolume, RoleInstance}, ClassicDelim: ":"},
}

// Compose builds the import ID for a Pulumi type. Returns handled=false when the
// type is not in Specs (caller uses a pre-resolved ID or physical id).
func Compose(pulumiType, provider string, get func(Role) string) (id string, handled bool, err error) {
	spec, ok := Specs[pulumiType]
	if !ok {
		return "", false, nil
	}
	if spec.Custom != nil {
		id, err = spec.Custom(get, provider)
		return id, true, err
	}
	parts := spec.Classic
	delim := spec.ClassicDelim
	if provider == "native" && spec.Native != nil {
		parts = spec.Native
		delim = spec.NativeDelim
	}
	vals := make([]string, 0, len(parts))
	for _, r := range parts {
		v := get(r)
		if v == "" {
			return "", true, fmt.Errorf("%s: missing role %q", pulumiType, r)
		}
		vals = append(vals, v)
	}
	return strings.Join(vals, delim), true, nil
}
