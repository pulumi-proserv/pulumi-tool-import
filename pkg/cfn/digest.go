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
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// CurrentStackDigestFormatVersion is stamped into every CFN digest this
// build writes. It is independent of the TF digest's version
// (pkg.CurrentDigestFormatVersion) despite the mirrored shape. Bump it on a
// change a consumer built before the change would half-read rather than
// reject — and note the gate only exists in builds from the release that
// introduced it, so a bump must wait until gated builds are the installed
// floor.
const CurrentStackDigestFormatVersion = 1

// StackDigest is the agent-safe representation of a deployed CloudFormation
// stack — the CFN analog of tf-digest's ModuleMap. The raw stack/template is
// never read directly by the migration agent.
type StackDigest struct {
	// FormatVersion is the digest file format: stamped by WriteStackDigest on
	// write, read back as the file declared on load. LoadStackDigest refuses
	// a version newer than it knows; 0 (absent) predates the field.
	FormatVersion int           `json:"digestFormatVersion,omitempty"`
	StackName     string        `json:"stackName"`
	Region        string        `json:"region"`
	Resources     []CfnResource `json:"resources"`
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

// WriteStackDigest stamps the current format version and writes the digest.
func WriteStackDigest(d *StackDigest, path string) error {
	d.FormatVersion = CurrentStackDigestFormatVersion
	data, err := json.MarshalIndent(d, "", "    ")
	if err != nil {
		return fmt.Errorf("marshaling digest: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing digest: %w", err)
	}
	return nil
}

// LoadStackDigest reads a cfn-digest.json with UseNumber (the digest's values
// end up in state) and refuses a format version newer than this build knows.
func LoadStackDigest(path string) (*StackDigest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading digest: %w", err)
	}
	var d StackDigest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("parsing digest %s: %w", path, err)
	}
	if d.FormatVersion > CurrentStackDigestFormatVersion {
		return nil, fmt.Errorf(
			"digest %s has format version %d, but this build reads at most version %d; "+
				"re-run \"digest cfn\" with this build, or upgrade the tool",
			path, d.FormatVersion, CurrentStackDigestFormatVersion)
	}
	return &d, nil
}
