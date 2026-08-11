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

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

type cfnStackReader struct{ c *cloudformation.Client }
type ccReader struct{ c *cloudcontrol.Client }
type awsLookups struct{ ec2 *ec2.Client }
type smReader struct{ c *secretsmanager.Client }

// NewAWSClients builds real AWS SDK v2 adapters for the given region.
func NewAWSClients(ctx context.Context, region string) (StackReader, CloudControlReader, Lookups, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load aws config: %w", err)
	}
	return &cfnStackReader{c: cloudformation.NewFromConfig(cfg)},
		&ccReader{c: cloudcontrol.NewFromConfig(cfg)},
		&awsLookups{ec2: ec2.NewFromConfig(cfg)},
		nil
}

// NewSecretReader builds a Secrets Manager adapter for live secret enrichment.
func NewSecretReader(ctx context.Context, region string) (SecretReader, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return &smReader{c: secretsmanager.NewFromConfig(cfg)}, nil
}

func (r *smReader) GetCurrentSecret(ctx context.Context, secretID string) (string, string, error) {
	out, err := r.c.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId:     aws.String(secretID),
		VersionStage: aws.String("AWSCURRENT"),
	})
	if err != nil {
		return "", "", fmt.Errorf("get secret value %s: %w", secretID, err)
	}
	return aws.ToString(out.VersionId), aws.ToString(out.SecretString), nil
}

func (s *cfnStackReader) GetTemplate(ctx context.Context, stackName string) (string, error) {
	out, err := s.c.GetTemplate(ctx, &cloudformation.GetTemplateInput{StackName: &stackName})
	if err != nil {
		return "", fmt.Errorf("get template %s: %w", stackName, err)
	}
	return aws.ToString(out.TemplateBody), nil
}

func (s *cfnStackReader) ListStackResources(ctx context.Context, stackName string) ([]StackResource, error) {
	var res []StackResource
	p := cloudformation.NewListStackResourcesPaginator(s.c, &cloudformation.ListStackResourcesInput{StackName: &stackName})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list stack resources: %w", err)
		}
		for _, r := range page.StackResourceSummaries {
			res = append(res, StackResource{
				LogicalID:  aws.ToString(r.LogicalResourceId),
				PhysicalID: aws.ToString(r.PhysicalResourceId),
				CfnType:    aws.ToString(r.ResourceType),
			})
		}
	}
	return res, nil
}

func (s *cfnStackReader) GetExports(ctx context.Context) (map[string]string, error) {
	exports := map[string]string{}
	p := cloudformation.NewListExportsPaginator(s.c, &cloudformation.ListExportsInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list exports: %w", err)
		}
		for _, e := range page.Exports {
			exports[aws.ToString(e.Name)] = aws.ToString(e.Value)
		}
	}
	return exports, nil
}

func (r *ccReader) GetResource(ctx context.Context, typeName, id string) (map[string]interface{}, error) {
	out, err := r.c.GetResource(ctx, &cloudcontrol.GetResourceInput{TypeName: &typeName, Identifier: &id})
	if err != nil {
		return nil, fmt.Errorf("cloudcontrol get resource %s/%s: %w", typeName, id, err)
	}
	if out.ResourceDescription == nil || out.ResourceDescription.Properties == nil {
		return nil, nil
	}
	var props map[string]interface{}
	if err := json.Unmarshal([]byte(aws.ToString(out.ResourceDescription.Properties)), &props); err != nil {
		return nil, fmt.Errorf("unmarshal cloudcontrol properties: %w", err)
	}
	return props, nil
}

func (l *awsLookups) SecurityGroupRuleID(ctx context.Context, egress bool, props map[string]interface{}) (string, error) {
	groupID := fmt.Sprintf("%v", props["GroupId"])
	if groupID == "" || groupID == "<nil>" {
		return "", fmt.Errorf("security group rule: missing GroupId")
	}
	out, err := l.ec2.DescribeSecurityGroupRules(ctx, &ec2.DescribeSecurityGroupRulesInput{
		Filters: []ec2types.Filter{{Name: aws.String("group-id"), Values: []string{groupID}}},
	})
	if err != nil {
		return "", fmt.Errorf("describe security group rules: %w", err)
	}
	var matches []string
	for _, rule := range out.SecurityGroupRules {
		if sgRuleMatches(rule, props, egress) {
			matches = append(matches, aws.ToString(rule.SecurityGroupRuleId))
		}
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("expected exactly one matching SG rule for group %s (egress=%v), got %d", groupID, egress, len(matches))
	}
	return matches[0], nil
}

func (l *awsLookups) EIPAllocationID(ctx context.Context, publicIP string) (string, error) {
	out, err := l.ec2.DescribeAddresses(ctx, &ec2.DescribeAddressesInput{
		Filters: []ec2types.Filter{{Name: aws.String("public-ip"), Values: []string{publicIP}}},
	})
	if err != nil {
		return "", fmt.Errorf("describe addresses: %w", err)
	}
	if len(out.Addresses) != 1 {
		return "", fmt.Errorf("expected exactly one EIP for %s, got %d", publicIP, len(out.Addresses))
	}
	return aws.ToString(out.Addresses[0].AllocationId), nil
}

func (l *awsLookups) InternetGatewayAttachment(ctx context.Context, igwID string) (string, error) {
	out, err := l.ec2.DescribeInternetGateways(ctx, &ec2.DescribeInternetGatewaysInput{
		InternetGatewayIds: []string{igwID},
	})
	if err != nil {
		return "", fmt.Errorf("describe internet gateways: %w", err)
	}
	if len(out.InternetGateways) != 1 || len(out.InternetGateways[0].Attachments) == 0 {
		return "", fmt.Errorf("no attachment found for igw %s", igwID)
	}
	return igwID + ":" + aws.ToString(out.InternetGateways[0].Attachments[0].VpcId), nil
}
