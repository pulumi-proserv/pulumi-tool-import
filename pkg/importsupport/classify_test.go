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

package importsupport

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassifySDKv2NoImporter(t *testing.T) {
	t.Parallel()
	err := errors.New("resource aws_vpn_gateway_route_propagation doesn't support import")
	assert.Equal(t, Unsupported, Classify(err))
}

func TestClassifyPluginFrameworkNoImporter(t *testing.T) {
	t.Parallel()
	err := errors.New("Resource Import Not Implemented: This resource does not support import. " +
		"Please contact the provider developer for additional information.")
	assert.Equal(t, Unsupported, Classify(err))
}

func TestClassifySuccessfulImport(t *testing.T) {
	t.Parallel()
	assert.Equal(t, Supported, Classify(nil))
}

func TestClassifyIDFormatErrorMeansImportable(t *testing.T) {
	t.Parallel()
	// aws_lambda_permission rejects the probe ID but does support import.
	err := errors.New(`Unexpected format of ID ("probe-nonexistent-id"), expected FUNCTION_NAME/STATEMENT_ID`)
	assert.Equal(t, Supported, Classify(err))
}

func TestClassifyNotFoundErrorMeansImportable(t *testing.T) {
	t.Parallel()
	err := errors.New("Cannot import non-existent remote object")
	assert.Equal(t, Supported, Classify(err))
}

// A dead plugin must never read as "importable". The probe runs an
// unconfigured provider, so a resource whose importer dereferences provider
// state can crash the plugin; every later probe then fails at the transport
// and would otherwise be classified Supported, silently letting genuinely
// non-importable resources into the import file.
func TestClassifyPluginCrashIsUnknown(t *testing.T) {
	t.Parallel()
	err := errors.New("rpc error: code = Unavailable desc = transport is closing")
	assert.Equal(t, Unknown, Classify(err))
}

func TestClassifyPluginExitIsUnknown(t *testing.T) {
	t.Parallel()
	err := errors.New("plugin process exited: path=/tmp/terraform-provider-aws pid=123")
	assert.Equal(t, Unknown, Classify(err))
}

func TestClassifyConnectionFailureIsUnknown(t *testing.T) {
	t.Parallel()
	err := errors.New("connection refused")
	assert.Equal(t, Unknown, Classify(err))
}
