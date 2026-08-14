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
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
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
		context.Background(), deployment, stack.Base64SecretsProvider{})
	if err != nil {
		return fmt.Errorf("deserializing deployment for verification: %w", err)
	}

	if err := snap.VerifyIntegrity(); err != nil {
		return fmt.Errorf("state integrity check failed: %w", err)
	}
	return nil
}
