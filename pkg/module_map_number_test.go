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
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDigestPreservesLargeIntegers(t *testing.T) {
	t.Parallel()

	attrsJSON := []byte(`{"id":1234567890123456789,"owner_id":52848974346}`)

	attrs, err := decodeAttrs(attrsJSON)
	require.NoError(t, err)

	id, ok := attrs["id"].(json.Number)
	require.True(t, ok, "numbers must decode as json.Number, got %T", attrs["id"])
	assert.Equal(t, "1234567890123456789", id.String())

	assert.Equal(t, "1234567890123456789", formatImportID(attrs["id"]))
}
