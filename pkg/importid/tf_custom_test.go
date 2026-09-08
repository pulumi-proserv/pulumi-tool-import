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

func TestTFCustomRolePolicyAttachment(t *testing.T) {
	c := TFCustom["aws_iam_role_policy_attachment"]
	require.NotNil(t, c)
	id, ok := c(map[string]interface{}{"role": "r", "policy_arn": "arn:p"}, "")
	assert.True(t, ok)
	assert.Equal(t, "r/arn:p", id)
	id, ok = c(map[string]interface{}{"roles": []interface{}{"r2"}, "policy_arn": "arn:p"}, "")
	assert.True(t, ok)
	assert.Equal(t, "r2/arn:p", id)
	_, ok = c(map[string]interface{}{"policy_arn": "arn:p"}, "")
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
