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

package batchimport

import (
	"context"
	"fmt"
	"io"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/optimport"
)

// DefaultBatchSize is the number of importable resources per batch.
const DefaultBatchSize = 100

// Importer performs imports and reports what is currently in stack state.
type Importer interface {
	// ImportBatch imports the given resources. Its error is diagnostic only:
	// callers MUST NOT treat it as the verdict on whether resources landed.
	ImportBatch(ctx context.Context, rs []*optimport.ImportResource, nameTable map[string]string) error
	// ExistingResources returns the resources currently in stack state.
	ExistingResources(ctx context.Context) (map[ResourceKey]bool, error)
}

// Failure is a resource that was not in state after being imported alone.
type Failure struct {
	Key ResourceKey
	ID  string
	Err string
}

// Result summarizes a run.
type Result struct {
	Imported   []ResourceKey
	Skipped    []ResourceKey
	Failed     []Failure
	Planned    []ResourceKey
	BatchCount int
}

// Options configures a run.
type Options struct {
	BatchSize int
	// Resume, when true, skips resources already present in stack state. It is
	// documented in the design spec as defaulting to true, but this struct
	// does NOT apply that default — unlike BatchSize, Go's zero value (false)
	// is used as-is. Only the CLI inverts its --no-resume flag to produce
	// true by default; a direct caller passing Options{} gets no-resume.
	Resume bool
	DryRun bool
	// Progress, when non-nil, receives per-batch progress messages as the run
	// proceeds. When nil, no progress is emitted.
	Progress io.Writer
}

// Run imports file's resources in batches.
//
// Whether a resource imported successfully is determined by reading stack state
// afterwards, never by the error returned from ImportBatch: in pulumi/sdk
// v3.222.0, ImportResources returns an error after a *successful* import
// whenever code generation is disabled, which this tool always disables.
func Run(ctx context.Context, imp Importer, file *ImportFile, opts Options) (*Result, error) {
	if opts.BatchSize <= 0 {
		opts.BatchSize = DefaultBatchSize
	}

	res := &Result{}

	var components, importable []*optimport.ImportResource
	for _, r := range file.Resources {
		if r.Component {
			components = append(components, r)
			continue
		}
		importable = append(importable, r)
	}

	if opts.Resume {
		existing, err := imp.ExistingResources(ctx)
		if err != nil {
			return nil, fmt.Errorf("reading stack state: %w", err)
		}

		remaining := importable[:0:0]
		for _, r := range importable {
			if existing[keyOf(r)] {
				res.Skipped = append(res.Skipped, keyOf(r))
				continue
			}
			remaining = append(remaining, r)
		}
		importable = remaining
	}

	for _, r := range importable {
		res.Planned = append(res.Planned, keyOf(r))
	}
	res.BatchCount = (len(importable) + opts.BatchSize - 1) / opts.BatchSize

	if opts.DryRun {
		return res, nil
	}

	for start := 0; start < len(importable); start += opts.BatchSize {
		end := min(start+opts.BatchSize, len(importable))
		batch := importable[start:end]
		batchNum := start/opts.BatchSize + 1

		if opts.Progress != nil {
			fmt.Fprintf(opts.Progress, "Batch %d/%d (%d resources)\n", batchNum, res.BatchCount, len(batch))
		}

		// The error is not a verdict; it is captured only for the report.
		batchErr := imp.ImportBatch(ctx, withComponents(components, batch...), file.NameTable)

		after, err := imp.ExistingResources(ctx)
		if err != nil {
			return res, fmt.Errorf("reading stack state after batch: %w", err)
		}

		var missing []*optimport.ImportResource
		for _, r := range batch {
			if after[keyOf(r)] {
				res.Imported = append(res.Imported, keyOf(r))
				continue
			}
			missing = append(missing, r)
		}

		if len(missing) > 0 && opts.Progress != nil {
			fmt.Fprintf(opts.Progress, "  isolating %d failure(s)\n", len(missing))
		}

		// An isolation payload is components + [r], so only r can newly land
		// from it. Run every isolation import first, then read state once:
		// that single read gives the same verdicts as reading after each
		// isolation import, at a fraction of the cost.
		isoErrs := make(map[ResourceKey]error, len(missing))
		for _, r := range missing {
			isoErrs[keyOf(r)] = imp.ImportBatch(ctx, withComponents(components, r), file.NameTable)
		}

		if len(missing) > 0 {
			afterIso, err := imp.ExistingResources(ctx)
			if err != nil {
				return res, fmt.Errorf("reading stack state after isolating failures: %w", err)
			}
			for _, r := range missing {
				if afterIso[keyOf(r)] {
					res.Imported = append(res.Imported, keyOf(r))
					continue
				}
				res.Failed = append(res.Failed, Failure{
					Key: keyOf(r),
					ID:  r.ID,
					Err: errText(isoErrs[keyOf(r)], batchErr),
				})
			}
		}
	}

	return res, nil
}

// withComponents returns every component entry followed by the given resources.
// Components must accompany every import call so children resolve their parent
// through the nameTable.
func withComponents(
	components []*optimport.ImportResource,
	rs ...*optimport.ImportResource,
) []*optimport.ImportResource {
	out := make([]*optimport.ImportResource, 0, len(components)+len(rs))
	out = append(out, components...)
	out = append(out, rs...)
	return out
}

// errText describes why a resource is reported as failed. The state fact leads:
// an import error is unreliable evidence — the SDK returns one even after a
// successful import when code generation is disabled — so it is attached only
// as context.
func errText(isolationErr, batchErr error) string {
	const base = "resource not present in stack state after import"
	switch {
	case isolationErr != nil:
		return fmt.Sprintf("%s (last error: %s)", base, isolationErr)
	case batchErr != nil:
		return fmt.Sprintf("%s (last error: %s)", base, batchErr)
	default:
		return base
	}
}
