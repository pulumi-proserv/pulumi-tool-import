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

// composeScalingPolicy composes the import ID for aws:appautoscaling/policy:Policy.
func composeScalingPolicy(get func(Role) string, _ string) (string, error) {
	name := get(RoleName)
	parts := strings.Split(get(RoleScalingTargetID), "|")
	if name == "" || len(parts) != 3 {
		return "", fmt.Errorf("scaling policy needs name + 3-part target id")
	}
	return name + "/" + parts[2] + "/" + parts[0] + "/" + parts[1], nil
}

// composeScalableTarget composes the import ID for aws:appautoscaling/target:Target.
func composeScalableTarget(get func(Role) string, _ string) (string, error) {
	parts := strings.Split(get(RoleScalingTargetID), "|")
	if len(parts) != 3 {
		return "", fmt.Errorf("scalable target needs 3-part id")
	}
	return parts[2] + "/" + parts[0] + "/" + parts[1], nil
}

// composeEcsService composes the import ID for aws:ecs/service:Service.
func composeEcsService(get func(Role) string, _ string) (string, error) {
	parts := strings.Split(get(RoleEcsID), "/")
	if len(parts) <= 1 {
		return "", fmt.Errorf("ecs service id has no cluster segment")
	}
	return strings.Join(parts[1:], "/"), nil
}

// composeTransferServer composes the import ID for aws:transfer/server:Server.
func composeTransferServer(get func(Role) string, _ string) (string, error) {
	parts := strings.Split(get(RoleTransferID), "/")
	if len(parts) <= 1 {
		return "", fmt.Errorf("transfer server id malformed")
	}
	return parts[1], nil
}

// composeLayerVersionPermission composes the import ID for
// aws:lambda/layerVersionPermission:LayerVersionPermission.
func composeLayerVersionPermission(get func(Role) string, _ string) (string, error) {
	parts := strings.Split(get(RoleLayerArn), ":")
	if len(parts) <= 1 {
		return "", fmt.Errorf("layer version arn malformed")
	}
	return strings.Join(parts[:len(parts)-1], ":") + "," + parts[len(parts)-1], nil
}

// composeRoute53 composes the import ID for aws:route53/record:Record.
func composeRoute53(get func(Role) string, _ string) (string, error) {
	hz, name, typ := get(RoleHostZone), get(RoleName), get(RoleType)
	if hz == "" || name == "" || typ == "" {
		return "", fmt.Errorf("route53 record needs hostedZone, name, type")
	}
	id := hz + "_" + name + "_" + typ
	if sid := get(RoleSetID); sid != "" {
		id += "_" + sid
	}
	return id, nil
}
