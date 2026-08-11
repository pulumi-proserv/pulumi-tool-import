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

import "context"

// SecretReader fetches the live current (AWSCURRENT) version of a Secrets Manager
// secret: its version id and its string value.
type SecretReader interface {
	GetCurrentSecret(ctx context.Context, secretID string) (versionID, value string, err error)
}

// EnrichSecretsFromLive augments each owned AWS::SecretsManager::Secret in the
// digest with live data, so the migration is zero-diff:
//
//   - Attributes["SecretString"] is set to the LIVE current value. CloudFormation
//     stores only the template value; if the secret was rotated since deploy (or was
//     created via GenerateSecretString and has no template value at all), the live
//     value is what an imported aws:secretsmanager/secretVersion reads back. Using
//     the template value would make that version replace.
//   - SecretVersionImportID is set to "<arn>|<versionId>" — the import ID for the
//     companion SecretVersion resource, which is NOT a CloudFormation resource and so
//     never appears in the stack's resource list. The agent authors the version and
//     imports it with this ID.
//
// Enrichment is best-effort: a secret the reader cannot access is left with its
// template value and no version ID, never failing the whole digest. A nil reader
// is a no-op.
func EnrichSecretsFromLive(ctx context.Context, d *StackDigest, sr SecretReader) error {
	if sr == nil {
		return nil
	}
	for i := range d.Resources {
		r := &d.Resources[i]
		if r.Skipped || r.CfnType != "AWS::SecretsManager::Secret" || r.PhysicalID == "" {
			continue
		}
		versionID, value, err := sr.GetCurrentSecret(ctx, r.PhysicalID)
		if err != nil || versionID == "" {
			continue // best-effort: fall back to the template value
		}
		if r.Attributes == nil {
			r.Attributes = map[string]interface{}{}
		}
		r.Attributes["SecretString"] = value
		r.SecretVersionImportID = r.PhysicalID + "|" + versionID
	}
	return nil
}
