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

package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg"
)

// nonImportablePath derives the sidecar path from the import file path, so the
// two land together: "imports-ready.json" -> "imports-ready.non-importable.json".
func nonImportablePath(importFilePath string) string {
	ext := filepath.Ext(importFilePath)
	return strings.TrimSuffix(importFilePath, ext) + ".non-importable.json"
}

// writeNonImportable records the resources that were left out of the import
// file, with the import IDs and Terraform attributes needed to put them into
// state directly.
func writeNonImportable(path string, resources []pkg.NonImportableResource) error {
	if len(resources) == 0 {
		// Nothing to record. Remove any sidecar left by an earlier run rather
		// than leaving a stale one describing resources that now import fine.
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing stale %s: %w", path, err)
		}
		return nil
	}

	doc := pkg.NonImportableFile{
		Comment: "Resources whose Terraform type declares no importer. They were omitted from the " +
			"import file because importing them always fails. Do not simply let them be created: " +
			"write them into the stack's state instead.",
		Resources: resources,
	}

	data, err := json.MarshalIndent(&doc, "", "    ")
	if err != nil {
		return fmt.Errorf("marshaling: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// printNonImportableWarning explains what was dropped and why the obvious
// remedy is dangerous. Silence here is how a resource that already exists
// becomes a create against live infrastructure.
func printNonImportableWarning(resources []pkg.NonImportableResource, sidecarPath string) {
	fmt.Fprintf(os.Stderr, "\n=== %d resource(s) cannot be imported ===\n", len(resources))
	redacted := 0
	for _, r := range resources {
		fmt.Fprintf(os.Stderr, "  %s (%s)\n", r.TerraformAddress, r.Type)
		fmt.Fprintf(os.Stderr, "      id: %s\n", r.ID)
		if len(r.RedactedAttributes) > 0 {
			redacted++
			names := make([]string, 0, len(r.RedactedAttributes))
			for name, configKey := range r.RedactedAttributes {
				names = append(names, fmt.Sprintf("%s (stack config: %s)", name, configKey))
			}
			sort.Strings(names)
			fmt.Fprintf(os.Stderr, "      sensitive, redacted in the digest: %s\n", strings.Join(names, ", "))
		}
	}
	fmt.Fprintf(os.Stderr, "\nTheir Terraform resource types declare no importer, so \"pulumi import\" "+
		"cannot bring them\ninto state — it fails with a misleading \"resource '<id>' does not exist\" "+
		"even though the\nIDs are correct and the infrastructure exists. They have been left out of %s.\n",
		sidecarPath)
	fmt.Fprintf(os.Stderr, "\nWARNING: these resources are now absent from the import file, so the next "+
		"\"pulumi up\"\nwill try to CREATE them against infrastructure that already exists. That is only "+
		"safe\nwhen the resource's Create tolerates a pre-existing object, which for association and\n"+
		"toggle resources it often does not. Write them into the stack's state instead, using the\n"+
		"IDs and attributes recorded in %s.\n", sidecarPath)

	if redacted > 0 {
		fmt.Fprintf(os.Stderr, "\n%d of them have sensitive attributes, which the digest redacted to "+
			"a placeholder.\nResolve each from the stack config key shown above — \"digest tf\" stored the "+
			"real value\nthere as a secret — before writing it to state. Injecting the placeholder would "+
			"give the\nresource a wrong value.\n", redacted)
	}
	fmt.Fprintln(os.Stderr)
}
