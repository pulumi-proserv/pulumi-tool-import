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

//go:build e2e

package e2e

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/iot"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/vpclattice"
)

const fixtureRegion = "us-west-2"

const secondaryRegion = "us-east-1"

const fixtureManagedByTag = "pulumi-tool-import-e2e"

var terraformDoneStates = map[string]bool{
	"deleted":  true,
	"deleting": true,
}

func verifyFixtureResourcesGone(t *testing.T, ctx context.Context, ids fixtureResourceIDs) {
	t.Helper()

	cfg, err := loadRegionalAWSConfig(ctx)
	if err != nil {
		t.Errorf("verifyFixtureResourcesGone: loading AWS config for region %s: %v — could not verify "+
			"teardown; check the account by hand", fixtureRegion, err)
		return
	}

	ec2Client := ec2.NewFromConfig(cfg)
	iotClient := iot.NewFromConfig(cfg)
	vpcLatticeClient := vpclattice.NewFromConfig(cfg)
	lambdaClient := lambda.NewFromConfig(cfg)
	iamClient := iam.NewFromConfig(cfg)

	if ids.vpnConnectionID != "" {
		checkVPNConnectionGone(t, ctx, ec2Client, ids.vpnConnectionID)
	}
	if ids.vpnGatewayID != "" {
		checkVPNGatewayGone(t, ctx, ec2Client, ids.vpnGatewayID)
	}
	if ids.customerGatewayID != "" {
		checkCustomerGatewayGone(t, ctx, ec2Client, ids.customerGatewayID)
	}
	if ids.iotCertificateID != "" {
		checkIoTCertificateGone(t, ctx, iotClient, ids.iotCertificateID)
	}
	for _, id := range ids.eachIoTCertificateIDs {
		checkIoTCertificateGone(t, ctx, iotClient, id)
	}
	if ids.moduleIoTCertificateID != "" {
		checkIoTCertificateGone(t, ctx, iotClient, ids.moduleIoTCertificateID)
	}

	if ids.eastIoTCertificateID != "" {
		eastCfg, err := loadAWSConfigForRegion(ctx, secondaryRegion)
		if err != nil {
			t.Errorf("verifyFixtureResourcesGone: loading AWS config for region %s: %v — could not "+
				"verify the aliased provider's resources; check %s by hand", secondaryRegion, err, secondaryRegion)
		} else {
			checkIoTCertificateGone(t, ctx, iot.NewFromConfig(eastCfg), ids.eastIoTCertificateID)
		}
	}
	if ids.targetGroupID != "" {
		checkTargetGroupGone(t, ctx, vpcLatticeClient, ids.targetGroupID)
	}
	if ids.lambdaFunctionName != "" {
		checkLambdaFunctionGone(t, ctx, lambdaClient, ids.lambdaFunctionName)
	}
	if ids.iamRoleName != "" {
		checkIAMRoleGone(t, ctx, iamClient, ids.iamRoleName)
	}

	checkNoTaggedVPCsRemain(t, ctx, ec2Client)
}

func loadRegionalAWSConfig(ctx context.Context) (aws.Config, error) {
	return loadAWSConfigForRegion(ctx, fixtureRegion)
}

func loadAWSConfigForRegion(ctx context.Context, region string) (aws.Config, error) {
	if prev, ok := os.LookupEnv("AWS_PROFILE"); ok {
		os.Unsetenv("AWS_PROFILE")
		defer os.Setenv("AWS_PROFILE", prev)
	}
	return awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
}

func checkVPNConnectionGone(t *testing.T, ctx context.Context, c *ec2.Client, id string) {
	t.Helper()
	out, err := c.DescribeVpnConnections(ctx, &ec2.DescribeVpnConnectionsInput{VpnConnectionIds: []string{id}})
	if err != nil {
		if isNotFoundErr(err) {
			return
		}
		t.Errorf("verifying VPN connection %s is gone: %v", id, err)
		return
	}
	for _, conn := range out.VpnConnections {
		state := string(conn.State)
		if !terraformDoneStates[strings.ToLower(state)] {
			t.Errorf("ORPHANED: VPN connection %s still exists in AWS (state=%q) after teardown — "+
				"this resource bills; delete it by hand", id, state)
		}
	}
}

func checkVPNGatewayGone(t *testing.T, ctx context.Context, c *ec2.Client, id string) {
	t.Helper()
	out, err := c.DescribeVpnGateways(ctx, &ec2.DescribeVpnGatewaysInput{VpnGatewayIds: []string{id}})
	if err != nil {
		if isNotFoundErr(err) {
			return
		}
		t.Errorf("verifying VPN gateway %s is gone: %v", id, err)
		return
	}
	for _, gw := range out.VpnGateways {
		state := string(gw.State)
		if !terraformDoneStates[strings.ToLower(state)] {
			t.Errorf("ORPHANED: VPN gateway %s still exists in AWS (state=%q) after teardown — "+
				"delete it by hand", id, state)
		}
	}
}

func checkCustomerGatewayGone(t *testing.T, ctx context.Context, c *ec2.Client, id string) {
	t.Helper()
	out, err := c.DescribeCustomerGateways(ctx, &ec2.DescribeCustomerGatewaysInput{CustomerGatewayIds: []string{id}})
	if err != nil {
		if isNotFoundErr(err) {
			return
		}
		t.Errorf("verifying customer gateway %s is gone: %v", id, err)
		return
	}
	for _, gw := range out.CustomerGateways {
		state := aws.ToString(gw.State)
		if !terraformDoneStates[strings.ToLower(state)] {
			t.Errorf("ORPHANED: customer gateway %s still exists in AWS (state=%q) after teardown — "+
				"delete it by hand", id, state)
		}
	}
}

func checkIoTCertificateGone(t *testing.T, ctx context.Context, c *iot.Client, id string) {
	t.Helper()
	_, err := c.DescribeCertificate(ctx, &iot.DescribeCertificateInput{CertificateId: aws.String(id)})
	if err == nil {
		t.Errorf("ORPHANED: IoT certificate %s still exists in AWS after teardown — delete it by hand", id)
		return
	}
	if !isNotFoundErr(err) {
		t.Errorf("verifying IoT certificate %s is gone: %v", id, err)
	}
}

func checkTargetGroupGone(t *testing.T, ctx context.Context, c *vpclattice.Client, id string) {
	t.Helper()
	out, err := c.GetTargetGroup(ctx, &vpclattice.GetTargetGroupInput{TargetGroupIdentifier: aws.String(id)})
	if err != nil {
		if isNotFoundErr(err) {
			return
		}
		t.Errorf("verifying VPC Lattice target group %s is gone: %v", id, err)
		return
	}
	status := string(out.Status)
	if status != "DELETE_IN_PROGRESS" && status != "DELETED" {
		t.Errorf("ORPHANED: VPC Lattice target group %s still exists in AWS (status=%q) after "+
			"teardown — delete it by hand", id, status)
	}
}

func checkLambdaFunctionGone(t *testing.T, ctx context.Context, c *lambda.Client, name string) {
	t.Helper()
	_, err := c.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(name)})
	if err == nil {
		t.Errorf("ORPHANED: Lambda function %s still exists in AWS after teardown — delete it by hand", name)
		return
	}
	if !isNotFoundErr(err) {
		t.Errorf("verifying Lambda function %s is gone: %v", name, err)
	}
}

func checkIAMRoleGone(t *testing.T, ctx context.Context, c *iam.Client, name string) {
	t.Helper()
	_, err := c.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(name)})
	if err == nil {
		t.Errorf("ORPHANED: IAM role %s still exists in AWS after teardown — delete it by hand", name)
		return
	}
	if !isNotFoundErr(err) {
		t.Errorf("verifying IAM role %s is gone: %v", name, err)
	}
}

func checkNoTaggedVPCsRemain(t *testing.T, ctx context.Context, c *ec2.Client) {
	t.Helper()
	out, err := c.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("tag:ManagedBy"), Values: []string{fixtureManagedByTag}},
		},
	})
	if err != nil {
		t.Errorf("scanning for leftover VPCs tagged ManagedBy=%s in %s: %v",
			fixtureManagedByTag, fixtureRegion, err)
		return
	}
	var leftover []string
	for _, vpc := range out.Vpcs {
		if string(vpc.State) == "" || strings.ToLower(string(vpc.State)) != "deleted" {
			leftover = append(leftover, aws.ToString(vpc.VpcId))
		}
	}
	if len(leftover) > 0 {
		t.Errorf("ORPHANED: %d VPC(s) tagged ManagedBy=%s still exist in %s after teardown: %s — "+
			"delete them by hand (and everything still attached to them)",
			len(leftover), fixtureManagedByTag, fixtureRegion, strings.Join(leftover, ", "))
	}
}
