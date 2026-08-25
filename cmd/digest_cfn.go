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
	"fmt"
	"os"
	"strings"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg"
	"github.com/pulumi-proserv/pulumi-tool-import/pkg/cfn"
	"github.com/spf13/cobra"
)

func newDigestCfnCmd() *cobra.Command {
	var stackName, region, out string
	var pulumiStack, pulumiProject, projectDir, runtime string
	var skipSecrets bool
	cmd := &cobra.Command{
		Use:   "cfn",
		Short: "Digest a deployed CloudFormation stack",
		Long: `Digest a deployed CloudFormation stack into an agent-safe digest JSON.

By default, sensitive inline property values (e.g. SecretsManager SecretString,
RDS MasterUserPassword) are discovered, redacted from the digest, and set as
encrypted Pulumi stack-config secrets — this requires --pulumi-stack and
--pulumi-project. Use --skip-secrets to leave values in the digest instead (in
which case the digest contains plaintext and must be .gitignore'd).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			sr, cc, lk, err := cfn.NewAWSClients(ctx, region)
			if err != nil {
				return fmt.Errorf("aws clients: %w", err)
			}
			digest, err := cfn.BuildDigest(ctx, stackName, region, sr, cc, lk)
			if err != nil {
				return fmt.Errorf("build digest: %w", err)
			}

			// Enrich owned secrets with their live current value + version ID, so the
			// companion SecretVersion imports zero-diff (see EnrichSecretsFromLive).
			// Best-effort: a failure here just falls back to template values.
			if secReader, serr := cfn.NewSecretReader(ctx, region); serr != nil {
				fmt.Fprintf(os.Stderr, "WARNING: secret enrichment unavailable: %v\n", serr)
			} else if serr := cfn.EnrichSecretsFromLive(ctx, digest, secReader); serr != nil {
				fmt.Fprintf(os.Stderr, "WARNING: secret enrichment failed: %v\n", serr)
			}

			// Extract sensitive values into encrypted stack config (default on).
			// ExtractSecrets redacts the digest in place so it is safe to read.
			secretCount := 0
			if !skipSecrets {
				entries := cfn.ExtractSecrets(digest)
				if len(entries) > 0 {
					if pulumiStack == "" || pulumiProject == "" {
						return fmt.Errorf("found %d sensitive value(s) to extract but --pulumi-stack/--pulumi-project were not set; "+
							"provide them to store secrets in encrypted stack config, or pass --skip-secrets to leave values in the digest", len(entries))
					}
					if err := pkg.SetSecretsFromState(entries, projectDir, pulumiProject, pulumiStack, runtime); err != nil {
						return fmt.Errorf("set secrets: %w", err)
					}
					secretCount = len(entries)
				}
			}

			if err := cfn.WriteStackDigest(digest, out); err != nil {
				return err
			}

			fmt.Fprintf(os.Stderr, "Resources: %d\n", len(digest.Resources))
			if secretCount > 0 {
				fmt.Fprintf(os.Stderr, "Extracted %d secret(s) to stack config on %s\n", secretCount, pulumiStack)
			}
			if skipSecrets {
				fmt.Fprintf(os.Stderr, "WARNING: --skip-secrets set; digest may contain plaintext secrets — .gitignore it\n")
			}
			if len(digest.NoEchoParameters) > 0 {
				fmt.Fprintf(os.Stderr, "NOTE: NoEcho parameters cannot be extracted (masked by CloudFormation); set these as secrets manually: %s\n",
					strings.Join(digest.NoEchoParameters, ", "))
			}
			fmt.Fprintf(os.Stderr, "Output written to %s\n", out)
			return nil
		},
	}
	cmd.Flags().StringVar(&stackName, "stack-name", "", "Deployed CloudFormation stack name")
	cmd.Flags().StringVar(&region, "region", "", "AWS region")
	cmd.Flags().StringVarP(&out, "out", "o", "", "Output digest JSON path")
	cmd.Flags().StringVar(&pulumiStack, "pulumi-stack", "", "Pulumi stack to set extracted secrets on")
	cmd.Flags().StringVar(&pulumiProject, "pulumi-project", "", "Pulumi project name (for setting secrets)")
	cmd.Flags().StringVar(&projectDir, "project-dir", ".", "Path to the Pulumi project directory (for setting secrets)")
	cmd.Flags().StringVar(&runtime, "runtime", "", "Pulumi runtime when creating Pulumi.yaml (e.g. nodejs)")
	cmd.Flags().BoolVar(&skipSecrets, "skip-secrets", false, "Leave sensitive values in the digest instead of extracting them to stack config")
	cmd.MarkFlagRequired("stack-name")
	cmd.MarkFlagRequired("region")
	cmd.MarkFlagRequired("out")
	return cmd
}
