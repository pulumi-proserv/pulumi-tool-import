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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg"
	"github.com/pulumi-proserv/pulumi-tool-import/pkg/providermap"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func newPatchStateTfCmd() *cobra.Command {
	var statePath string
	var digestPath string
	var fieldsPath string
	var mappingFile string
	var outPath string
	var projectDir string
	var stack string
	var configDir string
	var nonImportablePath string
	var previewJSONPath string

	cmd := &cobra.Command{
		Use:   "tf",
		Short: "Patch imported state with not_read field values from TF digest",
		Long: `Patch a Pulumi stack state (from pulumi stack export) with field values
from a TF digest that the cloud API import doesn't return.

Uses a curated fields file (--fields) that lists which fields per resource type
are not returned by the cloud API on import and need patching. For each matching
resource, if the state input is nil:
  1. Use the digest value if available (from TF state)
  2. Fall back to the default from the fields file

After patching, re-import the state with: pulumi stack import --file <output>

Example:

  pulumi stack export > state.json
  pulumi plugin run import -- patch-state \
    --state state.json \
    --digest tf-digest.json \
    --fields data/aws-import-diff-fields.json \
    --mapping-file mappings.yaml \
    --out patched-state.json
  pulumi stack import --file patched-state.json
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			stackMode := statePath == "" && outPath == ""
			if !stackMode && (statePath == "" || outPath == "") {
				return fmt.Errorf("file mode needs both --state and --out; " +
					"omit both to operate on the stack directly via --project-dir and --stack")
			}
			if stackMode && (projectDir == "" || stack == "") {
				return fmt.Errorf("stack mode needs --project-dir and --stack")
			}
			if stackMode && previewJSONPath != "" {
				return fmt.Errorf("--preview-json applies to file mode only; " +
					"stack mode runs the preview itself")
			}
			if nonImportablePath != "" && !stackMode && previewJSONPath == "" {
				return fmt.Errorf("--non-importable requires --preview-json: injected resources take " +
					"their URN, parent, provider and dependencies from the program, so a preview is " +
					"needed; produce one with \"pulumi preview --json > preview.json\"")
			}

			var session *pkg.StackSession
			var backupPath string
			var stateData []byte

			if stackMode {
				ctx := cmd.Context()
				var err error
				session, err = pkg.NewStackSession(ctx, projectDir, stack)
				if err != nil {
					return err
				}
				stateData, err = session.Export(ctx)
				if err != nil {
					return err
				}

				// Write the backup before anything mutates the stack, so an
				// interrupted run leaves a one-line recovery rather than a
				// reconstruction job.
				backupPath = fmt.Sprintf("%s-%s.backup.json", stack, time.Now().UTC().Format("20060102-150405"))
				if err := os.WriteFile(backupPath, stateData, 0o600); err != nil {
					return fmt.Errorf("writing backup: %w", err)
				}
				fmt.Fprintf(os.Stderr, "Stack state backed up to %s\n", backupPath)
				fmt.Fprintf(os.Stderr, "  Recover with: pulumi stack import --file %s\n", backupPath)
			} else {
				var err error
				stateData, err = os.ReadFile(statePath)
				if err != nil {
					return fmt.Errorf("reading state file: %w", err)
				}
			}

			// Load digest.
			digestData, err := os.ReadFile(digestPath)
			if err != nil {
				return fmt.Errorf("reading digest: %w", err)
			}
			var digest pkg.ModuleMap
			digestDec := json.NewDecoder(bytes.NewReader(digestData))
			digestDec.UseNumber()
			if err := digestDec.Decode(&digest); err != nil {
				return fmt.Errorf("parsing digest: %w", err)
			}

			// Load mappings.
			moduleMappings := make(map[string]string)
			resourceMappings := make(map[string]string)
			if mappingFile != "" {
				mfData, err := os.ReadFile(mappingFile)
				if err != nil {
					return fmt.Errorf("reading mapping file: %w", err)
				}
				var mf struct {
					Modules   map[string]string `yaml:"modules"`
					Mappings  map[string]string `yaml:"mappings"`
					Resources map[string]string `yaml:"resources"`
				}
				if err := yaml.Unmarshal(mfData, &mf); err != nil {
					return fmt.Errorf("parsing mapping file: %w", err)
				}
				for k, v := range mf.Mappings {
					moduleMappings[k] = v
				}
				for k, v := range mf.Modules {
					moduleMappings[k] = v
				}
				for k, v := range mf.Resources {
					resourceMappings[k] = v
				}
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
						// Strip "project:" namespace prefix if present.
						cleanKey := key
						if idx := strings.Index(key, ":"); idx >= 0 {
							cleanKey = key[idx+1:]
						}
						configSecrets[cleanKey] = val.Value
					}
				}
				fmt.Fprintf(os.Stderr, "Loaded %d secret config values from stack %s\n", len(configSecrets), stack)
			}

			var patched []byte
			var result *pkg.PatchStateResult

			fieldsFile, err := pkg.LoadFieldsFile(fieldsPath)
			if err != nil {
				return err
			}
			patched, result, err = pkg.PatchState(stateData, &digest, fieldsFile, moduleMappings, resourceMappings, configSecrets, configDir)
			if err != nil {
				return err
			}

			var injectResult *pkg.InjectResult
			if nonImportablePath != "" {
				sidecar, err := pkg.LoadNonImportableFile(nonImportablePath)
				if err != nil {
					return err
				}

				var preview *pkg.PreviewDigest
				if stackMode {
					preview, err = session.PreviewJSON(cmd.Context())
				} else {
					var previewData []byte
					previewData, err = os.ReadFile(previewJSONPath)
					if err == nil {
						preview, err = pkg.ParsePreviewJSON(previewData)
					} else {
						err = fmt.Errorf("reading preview JSON: %w", err)
					}
				}
				if err != nil {
					return err
				}

				providers, err := loadProvidersForDigest(&digest)
				if err != nil {
					return err
				}

				patched, injectResult, err = pkg.InjectNonImportable(
					patched, sidecar, preview, providers, configSecrets)
				if err != nil {
					return err
				}
			}

			if stackMode {
				ctx := cmd.Context()
				if err := session.Import(ctx, patched); err != nil {
					return err
				}

				verify, err := session.PreviewJSON(ctx)
				if err != nil {
					return fmt.Errorf("verifying preview failed; state is imported but unverified. "+
						"Restore with: pulumi stack import --file %s\n%w", backupPath, err)
				}

				var injectedURNs []string
				if injectResult != nil {
					injectedURNs = injectResult.URNs
				}
				problems := pkg.CheckInjectedOps(verify, injectedURNs)
				if len(problems) > 0 {
					fmt.Fprintf(os.Stderr, "\nInjection did not verify:\n")
					for _, p := range problems {
						fmt.Fprintf(os.Stderr, "  %s\n", p)
					}
					fmt.Fprintf(os.Stderr, "Restoring %s\n", backupPath)
					if err := session.Import(ctx, stateData); err != nil {
						return fmt.Errorf("restoring backup failed; restore by hand with: "+
							"pulumi stack import --file %s\n%w", backupPath, err)
					}
					return fmt.Errorf("injection reverted: %d resource(s) did not preview as unchanged",
						len(problems))
				}
				fmt.Fprintf(os.Stderr, "\nVerified: all %d injected resource(s) preview as unchanged.\n",
					len(injectedURNs))
			} else {
				// Write output.
				if err := os.WriteFile(outPath, patched, 0o600); err != nil {
					return fmt.Errorf("writing output: %w", err)
				}
				fmt.Fprintf(os.Stderr, "Patched state written to %s\n", outPath)
			}

			// Print stats.
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

			if injectResult != nil {
				fmt.Fprintf(os.Stderr, "  Injected:           %d resources\n", injectResult.Injected)
				fmt.Fprintf(os.Stderr, "  Secrets resolved:   %d\n", injectResult.SecretsResolved)
				if injectResult.NoDelta > 0 {
					fmt.Fprintf(os.Stderr, "  %d resource(s) injected without Terraform raw-state metadata\n",
						injectResult.NoDelta)
				}
				if !stackMode {
					fmt.Fprintf(os.Stderr, "\nVerify with: pulumi stack import --file %s && pulumi preview\n"+
						"A correct injection previews as zero operations. Do not use \"pulumi refresh\" "+
						"to check: it reports these resources unchanged even when their values are wrong.\n",
						outPath)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&statePath, "state", "", "Exported stack state (from pulumi stack export)")
	cmd.Flags().StringVar(&digestPath, "digest", "", "TF digest (tf-digest.json)")
	cmd.Flags().StringVar(&fieldsPath, "fields", "", "Curated fields file (aws-import-diff-fields.json)")
	cmd.Flags().StringVar(&mappingFile, "mapping-file", "", "Path to YAML mapping file")
	cmd.Flags().StringVarP(&outPath, "out", "o", "", "Output path for patched state")
	cmd.Flags().StringVar(&projectDir, "project-dir", "", "Pulumi project directory (for reading stack config secrets)")
	cmd.Flags().StringVar(&stack, "stack", "", "Pulumi stack name (for reading stack config secrets)")
	cmd.Flags().StringVar(&configDir, "config-dir", "", "TF config directory (for resolving asset file paths)")
	cmd.Flags().StringVar(&nonImportablePath, "non-importable", "",
		"Sidecar from \"resolve tf\" whose resources should be written into state")
	cmd.Flags().StringVar(&previewJSONPath, "preview-json", "",
		"Output of \"pulumi preview --json\", the source of injected resource metadata")

	cmd.MarkFlagRequired("digest")
	cmd.MarkFlagRequired("fields")
	cmd.MarkFlagRequired("config-dir")

	return cmd
}

// loadProvidersForDigest loads Pulumi provider schemas for the Terraform
// providers a digest records. "digest tf" stores those addresses precisely so
// downstream commands can do this without a Terraform state file.
func loadProvidersForDigest(
	digest *pkg.ModuleMap,
) (map[providermap.TerraformProviderName]*pkg.ProviderWithMetadata, error) {
	if len(digest.Providers) == 0 {
		return nil, nil
	}
	names := make([]providermap.TerraformProviderName, 0, len(digest.Providers))
	for addr := range digest.Providers {
		names = append(names, providermap.TerraformProviderName(addr))
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })

	providers, err := pkg.PulumiProvidersForTerraformProviders(names, nil)
	if err != nil {
		return nil, fmt.Errorf("loading provider schemas for property name mapping: %w", err)
	}
	return providers, nil
}
