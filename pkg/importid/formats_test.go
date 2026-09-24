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

	aws6 "github.com/pulumi-proserv/pulumi-tool-import/importids/aws/v6"
	aws7 "github.com/pulumi-proserv/pulumi-tool-import/importids/aws/v7"
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

	// Like its siblings, this error names the placeholder that failed.
	_, err = Expand("{rule}/{id}", map[string]interface{}{"rule": "r1"}, "")
	require.ErrorContains(t, err, `placeholder "id" has no state id`)
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

	// A table for the wrong provider (or a mis-edited Provider field) must be
	// rejected rather than silently applied to aws resources.
	wrongProvider := &Formats{Provider: "hashicorp/azurerm", PulumiVersion: "v7.48.0", Version: "v6.66.0", Types: map[string]FormatEntry{}}
	_, err := ForAWSVersion("7.48.0", wrongProvider)
	require.ErrorContains(t, err, `override provider "hashicorp/azurerm"`)
}

func TestEmbeddedTableIsValid(t *testing.T) {
	f := aws7.Load().Formats
	require.NoError(t, f.Validate())
	assert.Equal(t, "hashicorp/aws", f.Provider)
	assert.NotEmpty(t, f.Version)
}

func TestCatalogsTrackPulumiMajors(t *testing.T) {
	assert.Equal(t, "v6.83.4", aws6.Load().Formats.PulumiVersion)
	assert.Equal(t, "v5.100.0", aws6.Load().Formats.Version)
	assert.Equal(t, "v7.48.0", aws7.Load().Formats.PulumiVersion)
	assert.Equal(t, "v6.66.0", aws7.Load().Formats.Version)
}
