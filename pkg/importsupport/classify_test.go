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

// TestClassifyPluginDidNotRespondIsUnknown covers the message OpenTofu itself
// emits when a plugin stops answering — plugin/grpc_error.go:56 and
// plugin6/grpc_error.go:56, whose comment says "the plugin has stopped running
// for some reason, and is usually the result of a crash".
//
// This was the worst possible omission from transportFailureMarkers, because
// Classify's fallthrough is Supported: a crashed provider was read as a
// POSITIVE answer that the type is importable. Once a plugin is down every
// later probe fails identically, so one crash could mark a whole run's
// resources importable and let genuinely non-importable ones into the import
// file — the exact failure this package exists to prevent.
//
// The full diagnostic text is used, not just the marker, so this fails if the
// substring stops matching what OpenTofu actually produces.
func TestClassifyPluginDidNotRespondIsUnknown(t *testing.T) {
	t.Parallel()

	err := errors.New("Plugin did not respond: The plugin encountered an error, " +
		"and failed to respond to the plugin6.(*GRPCProvider).ImportResourceState call. " +
		"The plugin logs may contain more details.")
	assert.Equal(t, Unknown, Classify(err),
		"a crashed plugin must never be read as an answer")
}

// TestClassifyEveryTransportMarkerIsUnknown guards the list itself. Each marker
// is there because it means "no answer"; one that stopped being matched would
// silently become a Supported verdict, which is the dangerous direction.
func TestClassifyEveryTransportMarkerIsUnknown(t *testing.T) {
	t.Parallel()

	for _, marker := range transportFailureMarkers {
		assert.Equal(t, Unknown, Classify(errors.New("probe failed: "+marker)),
			"marker %q must classify as Unknown", marker)
	}
}
