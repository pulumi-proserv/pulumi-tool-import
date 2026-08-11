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
	"strings"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg/batchimport"
	"github.com/spf13/cobra"
)

func newImportCmd() *cobra.Command {
	var filePath string
	var projectDir string
	var stack string
	var batchSize int
	var noResume bool
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import a prepared import file in batches, isolating failures",
		Long: `Import the resources in a prepared Pulumi import file, in batches, and
report every resource that failed.

A single malformed import ID fails the whole "pulumi import" run. The import
executes as one deployment in which each entry is a step, so one failing step
fails the deployment with a non-zero exit — but steps that already succeeded are
committed to state and are NOT rolled back, and depending on scheduling, later
steps may never have started. The outcome is therefore partial, and the error
alone does not say which resources landed.

When a batch does not fully land, this command re-imports that batch's missing
resources one at a time to identify exactly which ones failed, records them, and
carries on. One run therefore surfaces every bad import ID instead of one per run.

Whether a resource imported is determined by reading stack state afterwards, not
by the importer's exit status. Resources already present in state are skipped, so
a run can be repeated after fixing the reported IDs; pass --no-resume to disable.

Every component entry is included in every batch so child resources can resolve
their parent through the nameTable.

Resources are always imported unprotected and without code generation, matching
the hand-authored migration workflow.

Cloud credentials come from the environment. Wrap the command if you use ESC:

  pulumi env run <esc-env> -- pulumi plugin run import -- import \
    --file imports-ready.json --project-dir . --stack dev

Examples:

  # Inspect the plan without importing
  pulumi plugin run import -- import \
    --file imports-ready.json --project-dir . --stack dev --dry-run

  # Import, 50 resources per batch
  pulumi plugin run import -- import \
    --file imports-ready.json --project-dir . --stack dev --batch-size 50
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if batchSize <= 0 {
				return fmt.Errorf("--batch-size must be positive, got %d", batchSize)
			}

			file, err := batchimport.LoadImportFile(filePath)
			if err != nil {
				return err
			}

			imp, err := batchimport.NewStackImporter(ctx, projectDir, stack)
			if err != nil {
				return err
			}

			res, err := batchimport.Run(ctx, imp, file, batchimport.Options{
				BatchSize: batchSize,
				Resume:    !noResume,
				DryRun:    dryRun,
				Progress:  cmd.ErrOrStderr(),
			})
			if err != nil {
				if res != nil {
					// Show the operator what was identified before the
					// failure; still return the error so the exit code is
					// non-zero.
					_, _ = fmt.Fprint(cmd.OutOrStdout(), formatResult(res, dryRun))
				}
				return err
			}

			_, _ = fmt.Fprint(cmd.OutOrStdout(), formatResult(res, dryRun))

			if len(res.Failed) > 0 {
				// The printed report is the useful output, so suppress cobra's
				// error and usage noise; Execute() still exits non-zero.
				cmd.SilenceUsage = true
				cmd.SilenceErrors = true
				return fmt.Errorf("%d resource(s) failed to import", len(res.Failed))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&filePath, "file", "", "Prepared import file (from the resolve or import-id-match command)")
	cmd.Flags().StringVar(&projectDir, "project-dir", ".", "Pulumi project directory")
	cmd.Flags().StringVar(&stack, "stack", "", "Pulumi stack name")
	cmd.Flags().IntVar(&batchSize, "batch-size", batchimport.DefaultBatchSize, "Resources per batch")
	cmd.Flags().BoolVar(&noResume, "no-resume", false, "Import resources even if already in stack state")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the batch plan without importing")

	cmd.MarkFlagRequired("file")
	cmd.MarkFlagRequired("stack")

	return cmd
}

// formatResult renders the end-of-run summary.
func formatResult(res *batchimport.Result, dryRun bool) string {
	var b strings.Builder

	if dryRun {
		fmt.Fprintf(&b, "\nDry run — nothing imported.\n")
		fmt.Fprintf(&b, "  Would import: %d\n", len(res.Planned))
		fmt.Fprintf(&b, "  Skipped:      %d (already in state)\n", len(res.Skipped))
		fmt.Fprintf(&b, "  Batches:      %d\n", res.BatchCount)
		return b.String()
	}

	fmt.Fprintf(&b, "\nImport summary (%d batches)\n", res.BatchCount)
	fmt.Fprintf(&b, "  Imported: %d\n", len(res.Imported))
	fmt.Fprintf(&b, "  Skipped:  %d (already in state)\n", len(res.Skipped))
	fmt.Fprintf(&b, "  Failed:   %d\n", len(res.Failed))

	if len(res.Failed) > 0 {
		fmt.Fprintf(&b, "\nFAILED RESOURCES\n")
		for _, f := range res.Failed {
			fmt.Fprintf(&b, "  %s (%s)\n", f.Key.Name, f.Key.Type)
			fmt.Fprintf(&b, "    id:    %s\n", f.ID)
			b.WriteString(renderErrorBlock(f.Err))
		}
		fmt.Fprintf(&b, "\nFix the import IDs above and re-run; imported resources are skipped.\n")
	}

	return b.String()
}

// errLineIndent is the indentation applied to continuation lines of a
// multi-line rendered error, so the resource/id/error hierarchy stays
// visually clear and the lines don't run flush against the left margin.
const errLineIndent = "      "

// maxErrorLines caps the number of lines kept from a multi-line error so a
// single noisy failure (e.g. the SDK's wrapped CLI output, which can inline
// an entire "pulumi import" progress stream, Diagnostics/Resources/Duration
// blocks, etc.) can't blow out the FAILED RESOURCES table.
const maxErrorLines = 6

// renderErrorBlock renders a single failure's error text for the
// FAILED RESOURCES table. A single-line error renders unchanged on the
// "error:" line. A multi-line error is condensed: progress-stream artifacts
// and section header/footer noise are dropped, lines carrying real
// diagnostic content (containing "error:" or "Error") are prioritized when
// the result is capped at maxErrorLines, and a trailing marker reports how
// many additional lines were suppressed.
func renderErrorBlock(errText string) string {
	trimmed := strings.TrimSpace(errText)
	rawLines := strings.Split(trimmed, "\n")

	if len(rawLines) <= 1 {
		return fmt.Sprintf("    error: %s\n", trimmed)
	}

	filtered := filterNoiseLines(rawLines)
	if len(filtered) == 0 {
		// Nothing survived filtering; fall back to the first raw line rather
		// than rendering an empty error.
		return fmt.Sprintf("    error: %s\n", strings.TrimSpace(rawLines[0]))
	}
	if len(filtered) == 1 {
		return fmt.Sprintf("    error: %s\n", filtered[0])
	}

	kept, suppressed := capErrorLines(filtered, maxErrorLines)

	var b strings.Builder
	b.WriteString("    error:\n")
	for _, l := range kept {
		fmt.Fprintf(&b, "%s%s\n", errLineIndent, l)
	}
	if suppressed > 0 {
		fmt.Fprintf(&b, "%s... (%d more lines suppressed)\n", errLineIndent, suppressed)
	}
	return b.String()
}

// filterNoiseLines drops blank lines and lines that are pure formatting
// noise from a wrapped CLI error: progress-stream artifacts (lines
// beginning with "=" once trimmed) and the standalone Diagnostics:,
// Resources:, and Duration: ... section lines.
func filterNoiseLines(lines []string) []string {
	var out []string
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "=") {
			continue
		}
		if t == "Diagnostics:" || t == "Resources:" {
			continue
		}
		if strings.HasPrefix(t, "Duration:") {
			continue
		}
		out = append(out, t)
	}
	return out
}

// capErrorLines caps lines at max, preferring to keep lines that carry real
// diagnostic content (containing "error:" or "Error") when there isn't room
// for everything. Relative order is preserved. It returns the kept lines
// and the count of lines suppressed by the cap.
func capErrorLines(lines []string, max int) (kept []string, suppressed int) {
	if len(lines) <= max {
		return lines, 0
	}

	selected := make([]bool, len(lines))
	count := 0

	// First pass: keep diagnostic lines, in order.
	for i, l := range lines {
		if count >= max {
			break
		}
		if strings.Contains(l, "error:") || strings.Contains(l, "Error") {
			selected[i] = true
			count++
		}
	}
	// Second pass: fill any remaining slots with the earliest non-diagnostic
	// lines.
	for i, l := range lines {
		if count >= max {
			break
		}
		if selected[i] {
			continue
		}
		_ = l
		selected[i] = true
		count++
	}

	for i, l := range lines {
		if selected[i] {
			kept = append(kept, l)
		}
	}
	return kept, len(lines) - len(kept)
}

func init() { rootCmd.AddCommand(newImportCmd()) }
