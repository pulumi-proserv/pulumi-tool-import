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
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pulumi/pulumi/pkg/v3/resource/stack"
	"github.com/pulumi/pulumi/pkg/v3/secrets"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/config"
)

// VerifyDeploymentIntegrity checks the snapshot's structural invariants.
func VerifyDeploymentIntegrity(stateData []byte) error {
	var untyped apitype.UntypedDeployment
	// Decoded without UseNumber, deliberately: this function only reads — the
	// caller keeps the original bytes, and nothing decoded here is written
	// back — so float64 rounding of large integers cannot corrupt anything
	// (issue #27's audit). Adding any write-back path invalidates this.
	if err := json.Unmarshal(stateData, &untyped); err != nil {
		return fmt.Errorf("parsing state for verification: %w", err)
	}

	var deployment apitype.DeploymentV3
	if err := json.Unmarshal(untyped.Deployment, &deployment); err != nil {
		return fmt.Errorf("parsing deployment for verification: %w", err)
	}

	for i, res := range deployment.Resources {
		isProvider := strings.HasPrefix(string(res.Type), "pulumi:providers:")
		if res.Custom && !isProvider && res.Provider == "" {
			return fmt.Errorf("resource %d (%s): empty provider reference", i, res.URN)
		}
	}

	snap, err := stack.DeserializeDeploymentV3(
		context.Background(), deployment, verificationSecretsProvider{})
	if err != nil {
		return fmt.Errorf("deserializing deployment for verification: %w", err)
	}

	if err := snap.VerifyIntegrity(); err != nil {
		return fmt.Errorf("state integrity check failed: %w", err)
	}
	return nil
}

type verificationSecretsProvider struct{}

func (verificationSecretsProvider) OfType(ty string, state json.RawMessage) (secrets.Manager, error) {
	return &passthroughSecretsManager{ty: ty, state: state}, nil
}

type passthroughSecretsManager struct {
	ty    string
	state json.RawMessage
}

func (m *passthroughSecretsManager) Type() string                { return m.ty }
func (m *passthroughSecretsManager) State() json.RawMessage      { return m.state }
func (m *passthroughSecretsManager) Encrypter() config.Encrypter { return config.NopEncrypter }
func (m *passthroughSecretsManager) Decrypter() config.Decrypter { return passthroughDecrypter{} }

type passthroughDecrypter struct{}

func (passthroughDecrypter) DecryptValue(_ context.Context, ciphertext string) (string, error) {
	quoted, err := json.Marshal(ciphertext)
	if err != nil {
		return "", err
	}
	return string(quoted), nil
}

func (d passthroughDecrypter) BatchDecrypt(ctx context.Context, ciphertexts []string) ([]string, error) {
	return config.DefaultBatchDecrypt(ctx, d, ciphertexts)
}
