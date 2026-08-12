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

import "testing"

func TestCustomComposers(t *testing.T) {
	t.Parallel()
	// ScalingPolicy: ScalingTargetId "a|b|c" + PolicyName "cpu" -> cpu/c/a/b
	got, err := composeScalingPolicy(role(map[Role]string{RoleName: "cpu", RoleScalingTargetID: "svc|rid|dim"}), "classic")
	must(t, got, err, "cpu/dim/svc/rid")

	// ScalableTarget: Id "a|b|c" -> c/a/b
	got, err = composeScalableTarget(role(map[Role]string{RoleScalingTargetID: "svc|rid|dim"}), "classic")
	must(t, got, err, "dim/svc/rid")

	// ECS Service: Id "cluster/svc" -> svc
	got, err = composeEcsService(role(map[Role]string{RoleEcsID: "arn:cluster/svc"}), "classic")
	must(t, got, err, "svc")

	// Transfer Server: Id "a/s-1" -> s-1
	got, err = composeTransferServer(role(map[Role]string{RoleTransferID: "a/s-1"}), "classic")
	must(t, got, err, "s-1")

	// LayerVersionPermission: arn a:b:c:2 -> a:b:c,2
	got, err = composeLayerVersionPermission(role(map[Role]string{RoleLayerArn: "arn:l:1:2"}), "classic")
	must(t, got, err, "arn:l:1,2")

	// Route53: Z1 + a.example.com + A -> Z1_a.example.com_A
	got, err = composeRoute53(role(map[Role]string{RoleHostZone: "Z1", RoleName: "a.example.com", RoleType: "A"}), "classic")
	must(t, got, err, "Z1_a.example.com_A")
}

func role(m map[Role]string) func(Role) string { return func(r Role) string { return m[r] } }
func must(t *testing.T, got string, err error, want string) {
	t.Helper()
	if err != nil || got != want {
		t.Fatalf("got %q err=%v; want %q", got, err, want)
	}
}
