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

package pkg

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func minimalState(providerRef string) []byte {
	return []byte(fmt.Sprintf(`{
	  "version": 3,
	  "deployment": {
	    "manifest": {
	      "time": "2026-08-14T00:00:00Z",
	      "magic": "c9163ff21f1f2b0390dc48bdda47179718f772f507a7cebceca59ce1a7129029",
	      "version": "3.0.0"
	    },
	    "resources": [
	      {
	        "urn": "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev",
	        "type": "pulumi:pulumi:Stack"
	      },
	      {
	        "urn": "urn:pulumi:dev::proj::pulumi:providers:aws::default_7_24_0",
	        "type": "pulumi:providers:aws",
	        "custom": true,
	        "id": "9f4c2b1e-0000-4000-8000-000000000001"
	      },
	      {
	        "urn": "urn:pulumi:dev::proj::aws:ec2/routeTable:RouteTable::rt0",
	        "type": "aws:ec2/routeTable:RouteTable",
	        "custom": true,
	        "id": "rtb-1",
	        "parent": "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev",
	        "provider": %q
	      }
	    ]
	  }
	}`, providerRef))
}

const goodProviderRef = "urn:pulumi:dev::proj::pulumi:providers:aws::default_7_24_0::" +
	"9f4c2b1e-0000-4000-8000-000000000001"

func TestVerifyDeploymentIntegrity_Valid(t *testing.T) {
	t.Parallel()
	require.NoError(t, VerifyDeploymentIntegrity(minimalState(goodProviderRef)))
}

func TestVerifyDeploymentIntegrity_EmptyProviderRef(t *testing.T) {
	t.Parallel()
	err := VerifyDeploymentIntegrity(minimalState(""))
	require.Error(t, err)
}

func TestVerifyDeploymentIntegrity_UnknownProvider(t *testing.T) {
	t.Parallel()
	err := VerifyDeploymentIntegrity(minimalState(
		"urn:pulumi:dev::proj::pulumi:providers:aws::default_7_24_0::" +
			"00000000-dead-4000-8000-000000000000"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown provider")
}

func stateWithSecretsProvider(secretsProviderType, secretsProviderState string) []byte {
	stateField := ""
	if secretsProviderState != "" {
		stateField = fmt.Sprintf(`, "state": %s`, secretsProviderState)
	}
	return []byte(fmt.Sprintf(`{
	  "version": 3,
	  "deployment": {
	    "manifest": {
	      "time": "2026-08-14T00:00:00Z",
	      "magic": "c9163ff21f1f2b0390dc48bdda47179718f772f507a7cebceca59ce1a7129029",
	      "version": "3.0.0"
	    },
	    "secrets_providers": {
	      "type": %q%s
	    },
	    "resources": [
	      {
	        "urn": "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev",
	        "type": "pulumi:pulumi:Stack"
	      },
	      {
	        "urn": "urn:pulumi:dev::proj::pulumi:providers:aws::default_7_24_0",
	        "type": "pulumi:providers:aws",
	        "custom": true,
	        "id": "9f4c2b1e-0000-4000-8000-000000000001"
	      },
	      {
	        "urn": "urn:pulumi:dev::proj::aws:ec2/routeTable:RouteTable::rt0",
	        "type": "aws:ec2/routeTable:RouteTable",
	        "custom": true,
	        "id": "rtb-1",
	        "parent": "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev",
	        "provider": %q,
	        "inputs": {
	          "secretValue": {
	            "4dabf18193072939515e22adb298388d": "1b47061264138c4ac30d75fd1eb44270",
	            "ciphertext": "v1:not-a-real-ciphertext:this-is-opaque-to-verification=="
	          }
	        }
	      }
	    ]
	  }
	}`, secretsProviderType, stateField, goodProviderRef))
}

func TestVerifyDeploymentIntegrity_PassphraseSecretsProvider(t *testing.T) {
	t.Parallel()
	require.NoError(t, VerifyDeploymentIntegrity(
		stateWithSecretsProvider("passphrase", `{"salt": "not-a-real-salt=="}`)))
}

func TestVerifyDeploymentIntegrity_ServiceSecretsProvider(t *testing.T) {
	t.Parallel()
	require.NoError(t, VerifyDeploymentIntegrity(
		stateWithSecretsProvider("service", "")))
}

func TestVerifyDeploymentIntegrity_EncryptedSecretValueUnderNonB64Provider(t *testing.T) {
	t.Parallel()
	require.NoError(t, VerifyDeploymentIntegrity(
		stateWithSecretsProvider("passphrase", `{"salt": "not-a-real-salt=="}`)))
}
