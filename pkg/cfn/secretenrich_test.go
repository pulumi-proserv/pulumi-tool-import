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
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeSecretReader struct {
	version string
	value   string
	err     error
}

func (f fakeSecretReader) GetCurrentSecret(_ context.Context, _ string) (string, string, error) {
	return f.version, f.value, f.err
}

func TestEnrichSecretsFromLive(t *testing.T) {
	t.Parallel()
	d := &StackDigest{Resources: []CfnResource{
		{
			LogicalID:  "iacallbacktoken",
			CfnType:    "AWS::SecretsManager::Secret",
			PhysicalID: "arn:aws:secretsmanager:us-east-1:1:secret:foo-abc",
			Attributes: map[string]interface{}{"SecretString": "template-value"},
		},
		{LogicalID: "bucket", CfnType: "AWS::S3::Bucket", PhysicalID: "b", Attributes: map[string]interface{}{}},
	}}

	require.NoError(t, EnrichSecretsFromLive(context.Background(), d,
		fakeSecretReader{version: "v123", value: "live-value"}))

	// The live current value overwrites the (possibly drifted) template value,
	// so a SecretVersion imported from live is zero-diff.
	require.Equal(t, "live-value", d.Resources[0].Attributes["SecretString"])
	// The version import ID (arn|versionId) is recorded for the companion SecretVersion.
	require.Equal(t, "arn:aws:secretsmanager:us-east-1:1:secret:foo-abc|v123",
		d.Resources[0].SecretVersionImportID)
	// Non-secret resources are untouched.
	require.Empty(t, d.Resources[1].SecretVersionImportID)
}

func TestEnrichSecretsFromLive_GeneratedSecret(t *testing.T) {
	t.Parallel()
	// A secret created via GenerateSecretString has NO SecretString in the template,
	// so nothing is extractable from the digest alone. Live enrichment populates it.
	d := &StackDigest{Resources: []CfnResource{{
		LogicalID:  "gen",
		CfnType:    "AWS::SecretsManager::Secret",
		PhysicalID: "arn:aws:secretsmanager:us-east-1:1:secret:gen-xyz",
		Attributes: map[string]interface{}{"Id": "arn:aws:secretsmanager:us-east-1:1:secret:gen-xyz"},
	}}}

	require.NoError(t, EnrichSecretsFromLive(context.Background(), d,
		fakeSecretReader{version: "v1", value: "generated-value"}))
	require.Equal(t, "generated-value", d.Resources[0].Attributes["SecretString"])
}

func TestEnrichSecretsFromLive_ReaderErrorLeavesTemplateValue(t *testing.T) {
	t.Parallel()
	// An inaccessible secret must not blow up the digest — enrichment is best-effort
	// and falls back to the template value.
	d := &StackDigest{Resources: []CfnResource{{
		LogicalID:  "s",
		CfnType:    "AWS::SecretsManager::Secret",
		PhysicalID: "arn:aws:secretsmanager:us-east-1:1:secret:s-abc",
		Attributes: map[string]interface{}{"SecretString": "template-value"},
	}}}

	require.NoError(t, EnrichSecretsFromLive(context.Background(), d,
		fakeSecretReader{err: errors.New("access denied")}))
	require.Equal(t, "template-value", d.Resources[0].Attributes["SecretString"])
	require.Empty(t, d.Resources[0].SecretVersionImportID)
}

func TestEnrichSecretsFromLive_SkipsSkipped(t *testing.T) {
	t.Parallel()
	d := &StackDigest{Resources: []CfnResource{{
		LogicalID: "s", CfnType: "AWS::SecretsManager::Secret", Skipped: true,
	}}}
	require.NoError(t, EnrichSecretsFromLive(context.Background(), d, fakeSecretReader{version: "v", value: "x"}))
	require.Empty(t, d.Resources[0].SecretVersionImportID)
}
