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

// VerifyDeploymentIntegrity runs the engine's own snapshot integrity check over
// an exported deployment, so structural mistakes are caught before the file is
// written or imported rather than by the CLI afterwards.
//
// It rejects a resource missing its URN or type, a "custom: false" resource
// carrying an ID, a provider reference that does not parse or that names a
// provider absent from the snapshot, and a parent or dependency that is missing
// or ordered after the resource that refers to it.
func VerifyDeploymentIntegrity(stateData []byte) error {
	var untyped apitype.UntypedDeployment
	if err := json.Unmarshal(stateData, &untyped); err != nil {
		return fmt.Errorf("parsing state for verification: %w", err)
	}

	var deployment apitype.DeploymentV3
	if err := json.Unmarshal(untyped.Deployment, &deployment); err != nil {
		return fmt.Errorf("parsing deployment for verification: %w", err)
	}

	// Check for empty provider references before deserialization, since the
	// engine's VerifyIntegrity may not catch this case.
	for i, res := range deployment.Resources {
		// Only custom resources (that are not providers themselves) need a provider reference.
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

// verificationSecretsProvider is a secrets.Provider used ONLY to let
// stack.DeserializeDeploymentV3 run for the purposes of VerifyDeploymentIntegrity.
//
// Real stacks never use the "b64" provider that stack.Base64SecretsProvider
// supports: a local file backend records "passphrase" and Pulumi Cloud
// records "service" in the deployment's secrets_providers block. Passing
// Base64SecretsProvider here means deserialization fails on every real
// deployment with "no known secrets provider for type ...", even though
// VerifyIntegrity never needs a decrypted value.
//
// VerifyDeploymentIntegrity is a purely structural check: it inspects URNs,
// resource types, custom/id consistency, provider-reference resolution, and
// parent/dependency ordering. It never reads a property's plaintext value.
// So the manager handed to the deserializer only needs to let ciphertext
// pass through so deserialization can build the in-memory PropertyMap; it
// does not need to actually decrypt anything, and correctly must not
// require the real passphrase, service credentials, or any other secret
// material just to run an offline structural check.
//
// verificationSecretsProvider therefore accepts any provider type recorded
// in the deployment and returns a manager whose Decrypter passes ciphertext
// through opaquely instead of attempting to decrypt it (see
// passthroughDecrypter below for why it is not a plain no-op). Its Encrypter
// is config.NopEncrypter: verification never re-serializes a deployment, so
// nothing ever calls it. Do not reuse this type anywhere a decrypted value
// is actually consulted or where state gets re-serialized/persisted: the
// "plaintext" it produces for a secret is really just its original
// ciphertext, wrapped so it deserializes cleanly.
type verificationSecretsProvider struct{}

func (verificationSecretsProvider) OfType(ty string, state json.RawMessage) (secrets.Manager, error) {
	return &passthroughSecretsManager{ty: ty, state: state}, nil
}

// passthroughSecretsManager is the secrets.Manager returned by
// verificationSecretsProvider. See that type's doc comment for why
// pass-through (non-)decryption is safe here and nowhere else.
type passthroughSecretsManager struct {
	ty    string
	state json.RawMessage
}

func (m *passthroughSecretsManager) Type() string                { return m.ty }
func (m *passthroughSecretsManager) State() json.RawMessage      { return m.state }
func (m *passthroughSecretsManager) Encrypter() config.Encrypter { return config.NopEncrypter }
func (m *passthroughSecretsManager) Decrypter() config.Decrypter { return passthroughDecrypter{} }

// passthroughDecrypter implements config.Decrypter for verification. It
// deliberately does NOT hand ciphertext back unchanged the way
// config.NopDecrypter does: the engine always round-trips a secret's
// plaintext through JSON before encrypting it (see
// stack.SerializeResource/SerializeProperties), and after "decrypting"
// json.Unmarshal's the result back into a property value. Real ciphertext
// (e.g. a passphrase-encrypted base64 blob) is not itself valid JSON, so
// handing it back verbatim would make deserialization fail on exactly the
// production deployments this type exists to unblock.
//
// Instead, passthroughDecrypter wraps the ciphertext as an opaque JSON
// string, so it deserializes into a resource.PropertyValue holding the
// (still-encrypted, never-inspected) ciphertext as its content. That is
// exactly what VerifyDeploymentIntegrity needs: a structurally valid
// property value it will never read.
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
