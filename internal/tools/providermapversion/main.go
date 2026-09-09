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

// Prints the upstream provider version the providermap currently recommends
// for a Terraform provider, e.g. "v6.38.0" for "aws". Used by
// "make update-import-id-formats" so the scrape and the tool pin the same
// version.
package main

import (
	"fmt"
	"os"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg/providermap"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: providermapversion <provider-short-name, e.g. aws>")
		os.Exit(2)
	}
	addr := providermap.TerraformProviderName("registry.terraform.io/hashicorp/" + os.Args[1])
	v, ok := providermap.GetUpstreamVersion(addr, "")
	if !ok {
		fmt.Fprintf(os.Stderr, "no providermap entry for %s\n", addr)
		os.Exit(1)
	}
	fmt.Println("v" + v)
}
