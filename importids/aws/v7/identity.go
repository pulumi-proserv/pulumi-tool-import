// Copyright 2016-2026, Pulumi Corporation.
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

package aws

import (
	"regexp"
	"strings"

	"github.com/pulumi-proserv/pulumi-tool-import/importids/catalog"
)

// Matches the destination's EventBridge import parser (Terraform AWS 6.69.1,
// internal/service/events/rule.go and internal/types/aws_region.go).
const eventBusARNPrefix = `arn:aws[\w-]*:events:[a-z]{2,4}(?:-[a-z]+)+-\d{1,2}:[0-9]{12}:event-bus/`

var eventBusARN = regexp.MustCompile(`^` + eventBusARNPrefix + `[0-9A-Za-z_.-]+$`)
var partnerEventBus = regexp.MustCompile(`^(?:` + eventBusARNPrefix + `)?aws\.partner(/[0-9A-Za-z_.-]+){2,}$`)

// Missing/null attributes can be supplied by a validated ID or an evidenced
// default. Present malformed or redacted attributes must not become identity text.
func identityAttribute(attrs map[string]interface{}, name string) (string, bool) {
	v := attrs[name]
	if v == nil {
		return "", true
	}
	s, ok := v.(string)
	return s, ok && s != "(sensitive)"
}

func identityARN(id string, service string, resourcePrefix string) bool {
	parts := strings.SplitN(id, ":", 6)
	return len(parts) == 6 && parts[0] == "arn" && parts[1] != "" &&
		parts[2] == service && parts[4] != "" &&
		(service != "cloudtrail" || parts[3] != "") &&
		strings.HasPrefix(parts[5], resourcePrefix) && len(parts[5]) > len(resourcePrefix)
}

func attachmentComposer(principalAttribute string) catalog.Composer {
	return func(attrs map[string]interface{}, stateID string) (string, bool) {
		principal, principalOK := identityAttribute(attrs, principalAttribute)
		policy, policyOK := identityAttribute(attrs, "policy_arn")
		if !principalOK || !policyOK {
			return "", false
		}
		// Split only the import form; generated/hyphenated state IDs are opaque.
		parts := strings.SplitN(stateID, "/", 2)
		if len(parts) == 2 && parts[0] != "" && identityARN(parts[1], "iam", "policy/") {
			if (principal != "" && principal != parts[0]) || (policy != "" && policy != parts[1]) {
				return "", false
			}
			principal, policy = parts[0], parts[1]
		}
		if principal == "" || strings.Contains(principal, "/") || !identityARN(policy, "iam", "policy/") {
			return "", false
		}
		return principal + "/" + policy, true
	}
}
