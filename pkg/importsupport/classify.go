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

// Package importsupport determines whether a Terraform resource type can be
// imported at all.
//
// A resource type whose upstream Terraform implementation declares no Importer
// cannot be imported by "pulumi import": the attempt fails with a misleading
// "resource '<id>' does not exist", even when the ID is correct and the
// infrastructure is present. Dropping such a resource from the import file is
// worse than the failure — it silently becomes a create against infrastructure
// that already exists.
//
// Importability is not part of any schema: Importer is a Go struct field on the
// provider's schema.Resource, and neither the Terraform gRPC schema RPC nor the
// Pulumi bridge mapping carries it. It is, however, observable: both Terraform
// SDKs check for a missing importer at the top of ImportResourceState, before
// making any provider API calls, so an unconfigured provider answers the
// question without credentials.
package importsupport

import "strings"

// Support is the verdict on whether a resource type can be imported.
type Support int

const (
	// Unknown means importability could not be determined.
	Unknown Support = iota
	// Supported means the resource type declares an importer.
	Supported
	// Unsupported means the resource type declares no importer, so any
	// attempt to import it will fail.
	Unsupported
)

func (s Support) String() string {
	switch s {
	case Supported:
		return "supported"
	case Unsupported:
		return "unsupported"
	default:
		return "unknown"
	}
}

// noImporterMarkers are the errors the Terraform SDKs return when a resource
// type declares no importer. The first is terraform-plugin-sdk v2
// (helper/schema.Provider.ImportState); the second is terraform-plugin-framework.
var noImporterMarkers = []string{
	"doesn't support import",
	"Resource Import Not Implemented",
}

// Classify interprets the outcome of an ImportResourceState probe. A nil error
// means the probe imported something, so the type is importable. Any other
// error — a bad probe ID, a missing remote object — also means the type is
// importable: only the no-importer markers say otherwise.
func Classify(err error) Support {
	if err == nil {
		return Supported
	}
	msg := err.Error()
	for _, marker := range noImporterMarkers {
		if strings.Contains(msg, marker) {
			return Unsupported
		}
	}
	return Supported
}
