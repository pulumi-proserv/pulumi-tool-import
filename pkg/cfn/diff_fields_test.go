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
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiffFieldsHasApiGatewayIntegrationDefaults(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("../../data/aws-import-diff-fields.json")
	require.NoError(t, err)

	var doc struct {
		Fields map[string]struct {
			NotRead map[string]struct {
				Default interface{} `json:"default"`
			} `json:"not_read"`
		} `json:"fields"`
	}
	require.NoError(t, json.Unmarshal(data, &doc))

	integration, ok := doc.Fields["aws:apigateway/integration:Integration"]
	require.True(t, ok, "expected aws:apigateway/integration:Integration entry in fields")

	_, ok = integration.NotRead["passthroughBehavior"]
	require.True(t, ok, "expected not_read.passthroughBehavior on aws:apigateway/integration:Integration")
}
