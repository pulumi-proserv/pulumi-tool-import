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

// fixtureRegion is the AWS region testdata/tf/main.tf's "provider \"aws\""
// block hard-codes. It is duplicated here, as a constant, rather than
// parsed out of main.tf, so this file has no HCL-parsing dependency — but
// it must be kept in sync with that file by hand.
//
// Pinning it explicitly matters: the ESC-brokered credentials this test
// runs under default to us-east-1, not us-west-2. During the incident that
// prompted this file, every ad-hoc orphan check run by hand against the
// default region silently queried the wrong region and reported "clean"
// while a VPN connection was actually running in us-west-2. Every AWS call
// below passes fixtureRegion explicitly so this test cannot repeat that
// mistake.
const fixtureRegion = "us-west-2"

// secondaryRegion is the region testdata/tf/main.tf's aliased
// 'provider "aws" { alias = "east" }' block hard-codes. Same
// keep-in-sync-by-hand caveat as fixtureRegion.
//
// A second region is not merely more surface to sweep — it is a second way
// for the sweep to report a false "clean". A client pinned to fixtureRegion
// asking about a us-east-1 certificate is told it does not exist, which is
// indistinguishable from "torn down". That happened on 2026-08-17: a DNS
// failure broke teardown, and a fixtureRegion-only sweep missed the
// certificate left behind in us-east-1.
const secondaryRegion = "us-east-1"

// fixtureManagedByTag is the tag value testdata/tf/main.tf's local.tags
// sets on every taggable resource it creates ("ManagedBy" key). Used as an
// independent, ID-free way to find leftover VPCs — independent because it
// does not depend on this run's Terraform state having recorded the VPC's
// ID before whatever failed.
const fixtureManagedByTag = "pulumi-tool-import-e2e"

// terraformDoneStates are the states EC2's VPN/gateway "State" fields use
// to mean "gone" without having actually disappeared from a Describe call
// yet — EC2 keeps a deleted VPN connection/gateway/customer gateway
// describable for a while after deletion, with its state set to one of
// these rather than removing it outright.
var terraformDoneStates = map[string]bool{
	"deleted":  true,
	"deleting": true,
}

// verifyFixtureResourcesGone is the second line of defense this test's
// teardown needed and never had: previously, the cleanup checked only
// "tofu destroy"'s exit code, and a destroy that exits zero while leaving
// resources behind — or one that never ran at all, e.g. because tfDir has
// no state — was indistinguishable from success. This queries AWS
// directly (never Terraform state, which is exactly what a broken destroy
// would leave looking clean) for the resource types this fixture is known
// to create, and t.Errors, by name, anything still around.
//
// The IDs it looks up are passed in, NOT read here: they must be captured
// before "tofu destroy" runs, because destroy removes each resource from
// state as it destroys it and the caller then deletes the state directory
// outright. Reading them here instead — which this function used to do —
// found an empty state on every successful teardown, skipped every
// ID-gated check below, and left only the tag scan running. Taking them as
// a parameter makes that ordering the caller's explicit responsibility
// rather than an invisible precondition.
//
// Note this is still "trusting state" only to the extent of learning an ID
// to double check independently, never to conclude something is gone.
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

	// The aliased provider's resources live in another region, and a client
	// pinned to fixtureRegion would report them as absent — which reads as
	// "cleaned up" and is exactly the false negative this file exists to
	// prevent. On 2026-08-17 a failed teardown left a certificate in
	// us-east-1 that a fixtureRegion-only sweep did not see.
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

	// Independent of whatever IDs state did or didn't have recorded (in
	// particular: covers a run where "tofu apply" failed before the VPC
	// itself finished, or where state was never written at all), tag-scan
	// for any VPC this fixture could have created.
	checkNoTaggedVPCsRemain(t, ctx, ec2Client)
}

// loadRegionalAWSConfig loads an AWS SDK config pinned to fixtureRegion,
// with the same AWS_PROFILE trap sanitizedEnv (helpers.go) works around for
// subprocess calls: a shell-exported AWS_PROFILE shadows the ESC-brokered
// credentials this test otherwise gets from its environment, so it is
// unset here for the duration of the SDK's own credential resolution
// (which reads os.Environ() directly, in-process, unlike the subprocess
// calls elsewhere in this test).
func loadRegionalAWSConfig(ctx context.Context) (aws.Config, error) {
	return loadAWSConfigForRegion(ctx, fixtureRegion)
}

// loadAWSConfigForRegion is loadRegionalAWSConfig for an arbitrary region,
// needed since the fixture gained an aliased provider in secondaryRegion.
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

// checkNoTaggedVPCsRemain scans for any VPC tagged ManagedBy=<fixtureManagedByTag>
// still present in fixtureRegion, independent of any ID captured from
// Terraform state. This is the catch-all: it finds a leftover VPC even in a
// run where state was never written, or was written but never reached the
// point of recording the VPC's ID.
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
