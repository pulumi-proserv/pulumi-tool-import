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

import "regexp"

// cdkHashSuffix matches CDK's construct-path hash: 8 uppercase-hex chars ending
// a logical ID (e.g. ...DefaultPolicyDFEB0894).
var cdkHashSuffix = regexp.MustCompile(`[0-9A-F]{8}$`)

// cfnRandomSuffix matches CloudFormation's server-assigned suffix on a physical
// ID: a hyphen + 12-13 mixed-case alphanumerics (e.g. ...-xQMUV6Ikl78Y).
var cfnRandomSuffix = regexp.MustCompile(`-[0-9A-Za-z]{12,13}$`)

// ClassifyName decides how a resource's name is handled on migration.
//   - hashed: settable name carrying a CDK construct hash -> route to config.
//   - serverAssigned: CFN-generated name -> leave unset, import preserves it.
//
// Mutually exclusive; a server-assigned physical ID wins.
func ClassifyName(logicalID, physicalID, cfnType string) (derivedName string, hashed bool, serverAssigned bool) {
	if cfnRandomSuffix.MatchString(physicalID) {
		return physicalID, false, true
	}
	if cdkHashSuffix.MatchString(logicalID) {
		return logicalID, true, false
	}
	return physicalID, false, false
}
