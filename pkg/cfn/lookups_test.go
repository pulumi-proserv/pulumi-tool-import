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
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeLookups struct {
	sgRule, eip, igw string

	// recorder fields captured from the arguments the fake methods received,
	// so tests can assert routing.
	gotEgress      *bool
	gotSGProps     map[string]interface{}
	gotEIPPublicIP string
	gotIGW         string
}

func (f *fakeLookups) SecurityGroupRuleID(_ context.Context, egress bool, props map[string]interface{}) (string, error) {
	f.gotEgress = &egress
	f.gotSGProps = props
	return f.sgRule, nil
}

func (f *fakeLookups) EIPAllocationID(_ context.Context, publicIP string) (string, error) {
	f.gotEIPPublicIP = publicIP
	return f.eip, nil
}

func (f *fakeLookups) InternetGatewayAttachment(_ context.Context, igwID string) (string, error) {
	f.gotIGW = igwID
	return f.igw, nil
}

func TestLookupImportID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("non-lookup type", func(t *testing.T) {
		lk := &fakeLookups{}
		_, isLookup, err := LookupImportID(ctx, "AWS::Lambda::Function", nil, lk)
		require.NoError(t, err)
		require.False(t, isLookup)
	})

	t.Run("security group ingress routes egress=false", func(t *testing.T) {
		lk := &fakeLookups{sgRule: "sgr-ingress"}
		attrs := map[string]interface{}{"GroupId": "sg-1"}
		id, isLookup, err := LookupImportID(ctx, "AWS::EC2::SecurityGroupIngress", attrs, lk)
		require.NoError(t, err)
		require.True(t, isLookup)
		require.Equal(t, "sgr-ingress", id)
		require.NotNil(t, lk.gotEgress)
		require.False(t, *lk.gotEgress, "SecurityGroupIngress must call SecurityGroupRuleID with egress=false")
		require.Equal(t, attrs, lk.gotSGProps)
	})

	t.Run("security group egress routes egress=true", func(t *testing.T) {
		lk := &fakeLookups{sgRule: "sgr-egress"}
		attrs := map[string]interface{}{"GroupId": "sg-1"}
		id, isLookup, err := LookupImportID(ctx, "AWS::EC2::SecurityGroupEgress", attrs, lk)
		require.NoError(t, err)
		require.True(t, isLookup)
		require.Equal(t, "sgr-egress", id)
		require.NotNil(t, lk.gotEgress)
		require.True(t, *lk.gotEgress, "SecurityGroupEgress must call SecurityGroupRuleID with egress=true")
		require.Equal(t, attrs, lk.gotSGProps)
	})

	t.Run("EIP routes PublicIp attribute", func(t *testing.T) {
		lk := &fakeLookups{eip: "eipalloc-1"}
		id, isLookup, err := LookupImportID(ctx, "AWS::EC2::EIP", map[string]interface{}{"PublicIp": "1.2.3.4"}, lk)
		require.NoError(t, err)
		require.True(t, isLookup)
		require.Equal(t, "eipalloc-1", id)
		require.Equal(t, "1.2.3.4", lk.gotEIPPublicIP)
	})

	t.Run("VPCGatewayAttachment routes InternetGatewayId attribute", func(t *testing.T) {
		lk := &fakeLookups{igw: "igw-1:vpc-1"}
		id, isLookup, err := LookupImportID(ctx, "AWS::EC2::VPCGatewayAttachment", map[string]interface{}{"InternetGatewayId": "igw-1"}, lk)
		require.NoError(t, err)
		require.True(t, isLookup)
		require.Equal(t, "igw-1:vpc-1", id)
		require.Equal(t, "igw-1", lk.gotIGW)
	})
}
