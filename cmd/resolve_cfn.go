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

	"github.com/pulumi-proserv/pulumi-tool-import/pkg"
	"github.com/pulumi-proserv/pulumi-tool-import/pkg/cfn"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func newResolveCfnCmd() *cobra.Command {
	var digestPath, importPath, mappingPath, provider, outPath string
	cmd := &cobra.Command{
		Use:   "cfn",
		Short: "Fill a Pulumi import file from a cfn-digest (digest cfn output)",
		RunE: func(cmd *cobra.Command, args []string) error {
			digestPtr, err := cfn.LoadStackDigest(digestPath)
			if err != nil {
				return err
			}
			digest := *digestPtr
			importData, err := os.ReadFile(importPath)
			if err != nil {
				return fmt.Errorf("reading import file: %w", err)
			}
			var importFile pkg.ImportFile
			if err := json.Unmarshal(importData, &importFile); err != nil {
				return fmt.Errorf("parsing import file: %w", err)
			}
			mappings := map[string]string{}
			if mappingPath != "" {
				mfData, err := os.ReadFile(mappingPath)
				if err != nil {
					return fmt.Errorf("reading mapping file: %w", err)
				}
				if err := yaml.Unmarshal(mfData, &mappings); err != nil {
					return fmt.Errorf("parsing mapping file: %w", err)
				}
			}
			result := cfn.FillFromDigest(&digest, &importFile, mappings, provider)
			outData, err := json.MarshalIndent(&importFile, "", "    ")
			if err != nil {
				return fmt.Errorf("marshaling output: %w", err)
			}
			if err := os.WriteFile(outPath, outData, 0o644); err != nil {
				return fmt.Errorf("writing output file: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Filled:    %d\n", result.Filled)
			fmt.Fprintf(os.Stderr, "Unmatched: %d\n", result.Unmatched)
			for _, w := range result.Warnings {
				fmt.Fprintf(os.Stderr, "  WARNING: %s\n", w)
			}
			fmt.Fprintf(os.Stderr, "Output written to %s\n", outPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&digestPath, "digest", "", "Path to cfn-digest.json (from `digest cfn`)")
	cmd.Flags().StringVar(&importPath, "import-file", "", "Path to the Pulumi import skeleton")
	cmd.Flags().StringVar(&mappingPath, "mapping-file", "", "Optional YAML map of import-entry name -> CFN logical ID")
	cmd.Flags().StringVar(&provider, "provider", "classic", "Import-ID provider format: classic | native")
	cmd.Flags().StringVarP(&outPath, "out", "o", "", "Output path for the filled import file")
	cmd.MarkFlagRequired("digest")
	cmd.MarkFlagRequired("import-file")
	cmd.MarkFlagRequired("out")
	return cmd
}
