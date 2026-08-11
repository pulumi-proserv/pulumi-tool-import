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
	"os"

	"github.com/spf13/cobra"
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "pulumi-tool-import",
	Short: "Import Terraform and AWS CDK/CloudFormation resources into Pulumi",
	Long: `pulumi-tool-import assists migrating existing infrastructure into Pulumi
via the "pulumi import" workflow.

It digests source IaC (Terraform/OpenTofu state or a deployed CloudFormation
stack), resolves the import IDs Pulumi needs, and patches imported state to a
clean preview — targeting the same bridged Pulumi providers (AWS first).

Typical pipeline:

  digest (tf|cfn)  ->  resolve (tf|cfn)  ->  pulumi import  ->  patch-state
`,
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}
