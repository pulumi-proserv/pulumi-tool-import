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

package importid

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExpandTemplate(t *testing.T) {
	attrs := map[string]interface{}{"rule": "r1", "target_id": "t1", "port": 443}
	got, err := Expand("{rule}/{target_id}", attrs, "r1-t1")
	require.NoError(t, err)
	assert.Equal(t, "r1/t1", got)

	got, err = Expand("{rest_api_id}/{id}", map[string]interface{}{"rest_api_id": "api"}, "res1")
	require.NoError(t, err)
	assert.Equal(t, "api/res1", got)

	got, err = Expand("{rule}:{port}", attrs, "")
	require.NoError(t, err)
	assert.Equal(t, "r1:443", got)
}

func TestExpandTemplateErrors(t *testing.T) {
	_, err := Expand("{rule}/{missing}", map[string]interface{}{"rule": "r1"}, "")
	require.ErrorContains(t, err, `attribute "missing" absent from state`)

	_, err = Expand("{rule}/{empty}", map[string]interface{}{"rule": "r1", "empty": ""}, "")
	require.ErrorContains(t, err, `attribute "empty" is empty`)

	_, err = Expand("{secret}", map[string]interface{}{"secret": "(sensitive)"}, "")
	require.ErrorContains(t, err, `attribute "secret" is sensitive`)
}

func TestFormatsValidate(t *testing.T) {
	bad := &Formats{Types: map[string]FormatEntry{
		"aws_a": {Template: "{x}", Manual: true, Evidence: "e"},
	}}
	require.ErrorContains(t, bad.Validate(), "aws_a: template and manual are exclusive")

	bad = &Formats{Types: map[string]FormatEntry{
		"aws_b": {Template: "{x}/{}", Evidence: "e"},
	}}
	require.ErrorContains(t, bad.Validate(), "aws_b: empty placeholder")

	bad = &Formats{Types: map[string]FormatEntry{
		"aws_c": {Evidence: "e"},
	}}
	require.ErrorContains(t, bad.Validate(), "aws_c: neither template nor manual")

	bad = &Formats{Types: map[string]FormatEntry{
		"aws_d": {Template: "{x}"},
	}}
	require.ErrorContains(t, bad.Validate(), "aws_d: missing evidence")
}

func TestEmbeddedTableIsValid(t *testing.T) {
	f := Embedded()
	require.NoError(t, f.Validate())
	assert.Equal(t, "hashicorp/aws", f.Provider)
	assert.NotEmpty(t, f.Version)
}
