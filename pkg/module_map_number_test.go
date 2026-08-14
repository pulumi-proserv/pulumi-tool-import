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

// TestDigestPreservesLargeIntegers guards the whole chain that ends in Pulumi
// state. A float64 round trip turns an AWS account ID or a snapshot ID into
// scientific notation, which Pulumi's state parser rejects.
func TestDigestPreservesLargeIntegers(t *testing.T) {
	t.Parallel()

	// A 19-digit ID, beyond float64's exact integer range.
	attrsJSON := []byte(`{"id":1234567890123456789,"owner_id":52848974346}`)

	attrs, err := decodeAttrs(attrsJSON)
	require.NoError(t, err)

	id, ok := attrs["id"].(json.Number)
	require.True(t, ok, "numbers must decode as json.Number, got %T", attrs["id"])
	assert.Equal(t, "1234567890123456789", id.String())

	// ImportID is built with %v; json.Number is a string type, so this must
	// print the original digits rather than 1.2345678901234568e+18.
	assert.Equal(t, "1234567890123456789", formatImportID(attrs["id"]))
}
