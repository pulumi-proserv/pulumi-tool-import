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

// StackDigest is the agent-safe representation of a deployed CloudFormation
// stack — the CFN analog of tf-digest's ModuleMap. The raw stack/template is
// never read directly by the migration agent.
type StackDigest struct {
	StackName string        `json:"stackName"`
	Region    string        `json:"region"`
	Resources []CfnResource `json:"resources"`
	// NoEchoParameters are template parameters marked NoEcho. Their values are
	// masked by CloudFormation and cannot be extracted — they must be set as
	// stack-config secrets manually. Surfaced here as a warning.
	NoEchoParameters []string `json:"noEchoParameters,omitempty"`
}

// CfnResource is one resource in the deployed stack. ImportID is set ONLY for
// the AWS-lookup types (pre-resolved because they need live AWS); pure types
// are composed later by `resolve cfn` from Attributes.
type CfnResource struct {
	LogicalID  string `json:"logicalId"`
	CfnType    string `json:"cfnType"`
	PulumiType string `json:"pulumiType,omitempty"`
	PhysicalID string `json:"physicalId,omitempty"`
	// Region is the stack's region (a CloudFormation stack is single-region), set
	// on every resource so region-scoped consumers (e.g. Lambda code download in
	// patch-state cfn) can read it per-resource without a separate flag.
	Region         string `json:"region,omitempty"`
	ImportID       string `json:"importId,omitempty"`       // pre-resolved (lookup types only)
	NativeImportID string `json:"nativeImportId,omitempty"` // aws-native composite ID (API Gateway family)
	// SecretVersionImportID is set only for AWS::SecretsManager::Secret after live
	// enrichment: the import ID (arn|versionId) for the companion aws:secretsmanager/
	// secretVersion the agent must author (the version is not a CFN resource).
	SecretVersionImportID string                 `json:"secretVersionImportId,omitempty"`
	Attributes            map[string]interface{} `json:"attributes,omitempty"`
	DerivedName           string                 `json:"derivedName,omitempty"`
	CdkHashedName         bool                   `json:"cdkHashedName,omitempty"`
	ServerAssigned        bool                   `json:"serverAssigned,omitempty"`
	Skipped               bool                   `json:"skipped,omitempty"`
	SkipReason            string                 `json:"skipReason,omitempty"`
}
