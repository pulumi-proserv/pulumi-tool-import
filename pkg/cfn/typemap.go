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

// typeMap maps a CloudFormation resource type to its aws-classic Pulumi type
// token. Curated from the pulumi-tool-importer port-target table plus the CDK
// field-report resource families. Unknown types return "" — the caller leaves
// PulumiType empty and the import skeleton supplies the type in that case.
var typeMap = map[string]string{
	"AWS::Lambda::Permission":                          "aws:lambda/permission:Permission",
	"AWS::Lambda::Function":                            "aws:lambda/function:Function",
	"AWS::Lambda::EventInvokeConfig":                   "aws:lambda/functionEventInvokeConfig:FunctionEventInvokeConfig",
	"AWS::Lambda::LayerVersionPermission":              "aws:lambda/layerVersionPermission:LayerVersionPermission",
	"AWS::ApiGateway::RestApi":                         "aws:apigateway/restApi:RestApi",
	"AWS::ApiGateway::Resource":                        "aws:apigateway/resource:Resource",
	"AWS::ApiGateway::Method":                          "aws:apigateway/method:Method",
	"AWS::ApiGateway::Deployment":                      "aws:apigateway/deployment:Deployment",
	"AWS::ApiGateway::Stage":                           "aws:apigateway/stage:Stage",
	"AWS::ApiGateway::Authorizer":                      "aws:apigateway/authorizer:Authorizer",
	"AWS::ApiGateway::UsagePlanKey":                    "aws:apigateway/usagePlanKey:UsagePlanKey",
	"AWS::IAM::Role":                                   "aws:iam/role:Role",
	"AWS::IAM::ManagedPolicy":                          "aws:iam/policy:Policy",
	"AWS::ECS::Service":                                "aws:ecs/service:Service",
	"AWS::ECS::Cluster":                                "aws:ecs/cluster:Cluster",
	"AWS::ECS::TaskDefinition":                         "aws:ecs/taskDefinition:TaskDefinition",
	"AWS::S3::Bucket":                                  "aws:s3/bucket:Bucket",
	"AWS::S3::BucketPolicy":                            "aws:s3/bucketPolicy:BucketPolicy",
	"AWS::Logs::LogGroup":                              "aws:cloudwatch/logGroup:LogGroup",
	"AWS::CloudWatch::Alarm":                           "aws:cloudwatch/metricAlarm:MetricAlarm",
	"AWS::Events::Rule":                                "aws:cloudwatch/eventRule:EventRule",
	"AWS::Events::EventBus":                            "aws:cloudwatch/eventBus:EventBus",
	"AWS::EC2::EIP":                                    "aws:ec2/eip:Eip",
	"AWS::EC2::SecurityGroup":                          "aws:ec2/securityGroup:SecurityGroup",
	"AWS::EC2::SecurityGroupIngress":                   "aws:vpc/securityGroupIngressRule:SecurityGroupIngressRule",
	"AWS::EC2::SecurityGroupEgress":                    "aws:vpc/securityGroupEgressRule:SecurityGroupEgressRule",
	"AWS::EC2::VPCGatewayAttachment":                   "aws:ec2/internetGatewayAttachment:InternetGatewayAttachment",
	"AWS::EC2::SubnetRouteTableAssociation":            "aws:ec2/routeTableAssociation:RouteTableAssociation",
	"AWS::EC2::Route":                                  "aws:ec2/route:Route",
	"AWS::EC2::VolumeAttachment":                       "aws:ec2/volumeAttachment:VolumeAttachment",
	"AWS::Route53::RecordSet":                          "aws:route53/record:Record",
	"AWS::SQS::Queue":                                  "aws:sqs/queue:Queue",
	"AWS::SQS::QueuePolicy":                            "aws:sqs/queuePolicy:QueuePolicy",
	"AWS::SNS::Topic":                                  "aws:sns/topic:Topic",
	"AWS::Cognito::UserPoolClient":                     "aws:cognito/userPoolClient:UserPoolClient",
	"AWS::Transfer::Server":                            "aws:transfer/server:Server",
	"AWS::Transfer::User":                              "aws:transfer/user:User",
	"AWS::ElasticLoadBalancingV2::ListenerCertificate": "aws:lb/listenerCertificate:ListenerCertificate",
	"AWS::ApplicationAutoScaling::ScalingPolicy":       "aws:appautoscaling/policy:Policy",
	"AWS::ApplicationAutoScaling::ScalableTarget":      "aws:appautoscaling/target:Target",
	"AWS::RDS::DBInstance":                             "aws:rds/instance:Instance",
	"AWS::DynamoDB::Table":                             "aws:dynamodb/table:Table",
}

// PulumiType returns the aws-classic Pulumi type token for a CFN type, or ""
// if the type is not in the curated map.
func PulumiType(cfnType string) string { return typeMap[cfnType] }
