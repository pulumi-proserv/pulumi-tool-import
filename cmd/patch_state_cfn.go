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
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg"
	"github.com/pulumi-proserv/pulumi-tool-import/pkg/cfn"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/spf13/cobra"
)

func newPatchStateCfnCmd() *cobra.Command {
	var statePath string
	var digestPath string
	var fieldsPath string
	var outPath string
	var region string
	var artifactsDir string
	var projectDir string
	var stack string

	cmd := &cobra.Command{
		Use:   "cfn",
		Short: "Patch imported state with not_read field values from a CFN digest",
		Long: `Patch a Pulumi stack state (from pulumi stack export) with field values
from a CloudFormation stack digest (digest cfn) that the cloud API import
doesn't return.

Uses a curated fields file (--fields) that lists which fields per resource type
are not returned by the cloud API on import and need patching. For each matching
resource, if the state input is nil:
  1. Use the digest's per-resource attribute value if available
  2. Fall back to the default from the fields file

For aws:lambda/function:Function resources whose fields file entry declares a
"code" asset, the deployed Lambda code zip is downloaded from AWS into
--artifacts-dir and referenced as a local FileArchive so preview is clean
without embedding CDK's build artifacts.

After patching, re-import the state with: pulumi stack import --file <output>

Example:

  pulumi stack export > state.json
  pulumi plugin run import -- patch-state cfn \
    --state state.json \
    --digest cfn-digest.json \
    --fields data/aws-import-diff-fields.json \
    --region us-east-1 \
    --artifacts-dir ./artifacts \
    --out patched-state.json
  pulumi stack import --file patched-state.json
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			stateData, err := os.ReadFile(statePath)
			if err != nil {
				return fmt.Errorf("reading state file: %w", err)
			}

			digestData, err := os.ReadFile(digestPath)
			if err != nil {
				return fmt.Errorf("reading digest: %w", err)
			}
			var digest cfn.StackDigest
			if err := json.Unmarshal(digestData, &digest); err != nil {
				return fmt.Errorf("parsing digest: %w", err)
			}
			// CFN digests carry no per-Lambda arn, so region can't be derived from
			// it during code download — default to the digest's region.
			if region == "" {
				region = digest.Region
			}

			fieldsFile, err := pkg.LoadFieldsFile(fieldsPath)
			if err != nil {
				return err
			}

			// Read config secrets from stack if --project-dir and --stack are set.
			var configSecrets map[string]string
			if projectDir != "" && stack != "" {
				ctx := context.Background()
				ws, err := auto.NewLocalWorkspace(ctx, auto.WorkDir(projectDir))
				if err != nil {
					return fmt.Errorf("creating workspace: %w", err)
				}
				allConfig, err := ws.GetAllConfig(ctx, stack)
				if err != nil {
					return fmt.Errorf("reading stack config: %w", err)
				}
				configSecrets = make(map[string]string, len(allConfig))
				for key, val := range allConfig {
					if val.Secret {
						cleanKey := key
						if idx := strings.Index(key, ":"); idx >= 0 {
							cleanKey = key[idx+1:]
						}
						configSecrets[cleanKey] = val.Value
					}
				}
				fmt.Fprintf(os.Stderr, "Loaded %d secret config values from stack %s\n", len(configSecrets), stack)
			}

			// Build the name map keyed by CFN logical ID.
			nameMap := make(map[string]*pkg.ModuleResource, len(digest.Resources))
			for i := range digest.Resources {
				r := &digest.Resources[i]
				if r.Skipped {
					continue
				}
				attrs := make(map[string]interface{}, len(r.Attributes))
				for k, v := range r.Attributes {
					attrs[k] = v
				}
				nameMap[r.LogicalID] = &pkg.ModuleResource{
					Mode:             "managed",
					TerraformAddress: r.LogicalID,
					ImportID:         r.ImportID,
					Attributes:       attrs,
				}
			}

			// Tier 2: pre-download deployed Lambda code for any function whose
			// fields file entry declares a "code" asset — but only for functions
			// actually present in the migrated program's state, not every function
			// in the digest (e.g. dropped provider-framework lambdas).
			lambdaIDsInState, err := pkg.StateLogicalIDsByType(stateData, "aws:lambda/function:Function")
			if err != nil {
				return fmt.Errorf("scanning state for lambdas: %w", err)
			}
			ctx := context.Background()
			for i := range digest.Resources {
				r := &digest.Resources[i]
				if r.Skipped || r.PulumiType != "aws:lambda/function:Function" {
					continue
				}
				if !lambdaIDsInState[r.LogicalID] {
					continue
				}
				if !fieldsFileHasCodeAsset(fieldsFile, r.PulumiType) {
					continue
				}
				functionName := r.PhysicalID
				if functionName == "" {
					continue
				}
				arn, _ := r.Attributes["arn"].(string)

				// Prefer the resource's own region from the digest; fall back to
				// the stack/--region.
				fnRegion := r.Region
				if fnRegion == "" {
					fnRegion = region
				}

				destPath := filepath.Join(artifactsDir, functionName+".zip")
				fmt.Fprintf(os.Stderr, "Downloading Lambda code for %s...\n", functionName)
				if err := pkg.DownloadLambdaCodeToFile(ctx, functionName, arn, fnRegion, destPath); err != nil {
					return fmt.Errorf("downloading lambda code for %s: %w", functionName, err)
				}
				if mr, ok := nameMap[r.LogicalID]; ok {
					mr.Attributes["code"] = functionName + ".zip"
				}
			}

			patched, result, err := pkg.PatchStateFromCFN(stateData, nameMap, fieldsFile, configSecrets, artifactsDir)
			if err != nil {
				return err
			}

			if err := os.WriteFile(outPath, patched, 0o600); err != nil {
				return fmt.Errorf("writing output: %w", err)
			}

			fmt.Fprintf(os.Stderr, "Patched state written to %s\n", outPath)
			fmt.Fprintf(os.Stderr, "  Patched:            %d resources\n", result.Patched)
			fmt.Fprintf(os.Stderr, "  Fields from digest: %d\n", result.FieldsFromDigest)
			fmt.Fprintf(os.Stderr, "  Fields from defaults: %d\n", result.FieldsFromDefaults)
			fmt.Fprintf(os.Stderr, "  Skipped sensitive:  %d\n", result.SkippedSensitive)
			if result.SkippedFalsySuppressed > 0 {
				fmt.Fprintf(os.Stderr, "  Skipped falsy suppressed: %d\n", result.SkippedFalsySuppressed)
			}
			fmt.Fprintf(os.Stderr, "  No fields to patch: %d\n", result.NoFields)
			fmt.Fprintf(os.Stderr, "  Digest mapped:      %d\n", result.DigestMapped)
			fmt.Fprintf(os.Stderr, "  Delta validated:    %d\n", result.DeltaValidated)
			if result.DeltaFailed > 0 {
				fmt.Fprintf(os.Stderr, "  Delta FAILED:       %d (outputs reverted)\n", result.DeltaFailed)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&statePath, "state", "", "Exported stack state (from pulumi stack export)")
	cmd.Flags().StringVar(&digestPath, "digest", "", "CFN digest (cfn-digest.json, from `digest cfn`)")
	cmd.Flags().StringVar(&fieldsPath, "fields", "", "Curated fields file (aws-import-diff-fields.json)")
	cmd.Flags().StringVarP(&outPath, "out", "o", "", "Output path for patched state")
	cmd.Flags().StringVar(&region, "region", "", "AWS region (for Lambda code download)")
	cmd.Flags().StringVar(&artifactsDir, "artifacts-dir", "", "Directory to write downloaded Lambda code zips (also used to resolve asset paths)")
	cmd.Flags().StringVar(&projectDir, "project-dir", "", "Pulumi project directory (for reading stack config secrets)")
	cmd.Flags().StringVar(&stack, "stack", "", "Pulumi stack name (for reading stack config secrets)")

	cmd.MarkFlagRequired("state")
	cmd.MarkFlagRequired("digest")
	cmd.MarkFlagRequired("fields")
	cmd.MarkFlagRequired("artifacts-dir")
	cmd.MarkFlagRequired("out")

	return cmd
}

// fieldsFileHasCodeAsset reports whether the fields file declares a "code"
// asset field for the given Pulumi type (checked against both the full and
// short type key, matching how PatchStateFromCFN resolves fields).
func fieldsFileHasCodeAsset(fieldsFile *pkg.FieldsFile, pulumiType string) bool {
	if fieldsFile == nil {
		return false
	}
	if cat, ok := fieldsFile.Fields[pulumiType]; ok {
		if fi, ok := cat.NotRead["code"]; ok && fi.Asset != "" {
			return true
		}
	}
	return false
}
