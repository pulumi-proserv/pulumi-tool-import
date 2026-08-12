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
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/optimport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testFile builds an import file with one component and n bare resources named
// res-0..res-(n-1).
func testFile(n int) *ImportFile {
	f := &ImportFile{
		NameTable: map[string]string{"comp": "urn:pulumi:dev::proj::example:index:Comp::comp"},
		Resources: []*optimport.ImportResource{
			{Type: "example:index:Comp", Name: "comp", Component: true},
		},
	}
	for i := 0; i < n; i++ {
		f.Resources = append(f.Resources, &optimport.ImportResource{
			Type:   "aws:s3/bucket:Bucket",
			Name:   fmt.Sprintf("res-%d", i),
			ID:     fmt.Sprintf("id-%d", i),
			Parent: "comp",
		})
	}
	return f
}

func TestRun_CleanRun(t *testing.T) {
	t.Parallel()

	f := newFakeImporter()
	res, err := Run(context.Background(), f, testFile(5), Options{BatchSize: 2, Resume: true})
	require.NoError(t, err)

	assert.Len(t, res.Imported, 5)
	assert.Empty(t, res.Failed)
	assert.Empty(t, res.Skipped)
	assert.Equal(t, 3, res.BatchCount)
	assert.Equal(t, 3, f.callCount, "one ImportBatch per batch, no isolation")
}

func TestRun_ComponentsInEveryBatch(t *testing.T) {
	t.Parallel()

	f := newFakeImporter()
	_, err := Run(context.Background(), f, testFile(5), Options{BatchSize: 2, Resume: true})
	require.NoError(t, err)

	require.Len(t, f.payloads, 3)
	for i, p := range f.payloads {
		var components int
		for _, r := range p {
			if r.Component {
				components++
			}
		}
		assert.Equal(t, 1, components, "batch %d must carry every component entry", i)
	}
	assert.Equal(t, map[string]string{
		"comp": "urn:pulumi:dev::proj::example:index:Comp::comp",
	}, f.nameTable)
}

func TestRun_BatchSizeDefaults(t *testing.T) {
	t.Parallel()

	f := newFakeImporter()
	res, err := Run(context.Background(), f, testFile(3), Options{Resume: true})
	require.NoError(t, err)

	assert.Equal(t, 1, res.BatchCount, "3 resources fit one default-size batch")
	assert.Equal(t, 1, f.callCount)
}

func TestRun_ResumeSkipsResourcesAlreadyInState(t *testing.T) {
	t.Parallel()

	f := newFakeImporter()
	f.state[ResourceKey{Type: "aws:s3/bucket:Bucket", Name: "res-0"}] = true
	f.state[ResourceKey{Type: "aws:s3/bucket:Bucket", Name: "res-2"}] = true

	res, err := Run(context.Background(), f, testFile(4), Options{BatchSize: 10, Resume: true})
	require.NoError(t, err)

	assert.ElementsMatch(t, []ResourceKey{
		{Type: "aws:s3/bucket:Bucket", Name: "res-0"},
		{Type: "aws:s3/bucket:Bucket", Name: "res-2"},
	}, res.Skipped)
	assert.ElementsMatch(t, []ResourceKey{
		{Type: "aws:s3/bucket:Bucket", Name: "res-1"},
		{Type: "aws:s3/bucket:Bucket", Name: "res-3"},
	}, res.Imported)

	require.Len(t, f.nonComponentPayloads(), 1)
	assert.ElementsMatch(t, []ResourceKey{
		{Type: "aws:s3/bucket:Bucket", Name: "res-1"},
		{Type: "aws:s3/bucket:Bucket", Name: "res-3"},
	}, f.nonComponentPayloads()[0], "skipped resources must not be re-imported")
}

func TestRun_NoResumeImportsEverything(t *testing.T) {
	t.Parallel()

	f := newFakeImporter()
	f.state[ResourceKey{Type: "aws:s3/bucket:Bucket", Name: "res-0"}] = true

	res, err := Run(context.Background(), f, testFile(2), Options{BatchSize: 10, Resume: false})
	require.NoError(t, err)

	assert.Empty(t, res.Skipped)
	assert.Len(t, res.Imported, 2)
	assert.Len(t, f.nonComponentPayloads()[0], 2)
}

func TestRun_ResumeDistinguishesSameNameDifferentType(t *testing.T) {
	t.Parallel()

	f := newFakeImporter()
	f.state[ResourceKey{Type: "aws:s3/bucket:Bucket", Name: "shared"}] = true

	file := &ImportFile{Resources: []*optimport.ImportResource{
		{Type: "aws:s3/bucket:Bucket", Name: "shared", ID: "b"},
		{Type: "aws:s3/bucketPublicAccessBlock:BucketPublicAccessBlock", Name: "shared", ID: "p"},
	}}

	res, err := Run(context.Background(), f, file, Options{BatchSize: 10, Resume: true})
	require.NoError(t, err)

	assert.Equal(t, []ResourceKey{{Type: "aws:s3/bucket:Bucket", Name: "shared"}}, res.Skipped)
	assert.Equal(t, []ResourceKey{
		{Type: "aws:s3/bucketPublicAccessBlock:BucketPublicAccessBlock", Name: "shared"},
	}, res.Imported)
}

func TestRun_DryRunImportsNothing(t *testing.T) {
	t.Parallel()

	f := newFakeImporter()
	f.state[ResourceKey{Type: "aws:s3/bucket:Bucket", Name: "res-0"}] = true

	res, err := Run(context.Background(), f, testFile(3), Options{BatchSize: 2, Resume: true, DryRun: true})
	require.NoError(t, err)

	assert.Equal(t, 0, f.callCount, "dry run must not import")
	assert.Len(t, res.Planned, 2, "res-0 is already in state")
	assert.Equal(t, 1, res.BatchCount)
	assert.Empty(t, res.Imported)
}

// The SDK returns an error after a successful --generate-code=false import.
// State, not the error, decides the outcome.
func TestRun_SucceedsDespiteImportBatchError(t *testing.T) {
	t.Parallel()

	f := newFakeImporter()
	f.batchErr = errors.New(
		"failed to read generated code: open /tmp/pulumi-import-1/generated_code.txt: no such file or directory")

	res, err := Run(context.Background(), f, testFile(4), Options{BatchSize: 2, Resume: true})
	require.NoError(t, err)

	assert.Len(t, res.Imported, 4)
	assert.Empty(t, res.Failed)
	assert.Equal(t, 2, f.callCount, "no isolation pass when everything landed")
}

func TestRun_IsolatesTheFailingResource(t *testing.T) {
	t.Parallel()

	f := newFakeImporter()
	bad := ResourceKey{Type: "aws:s3/bucket:Bucket", Name: "res-1"}
	f.failKeys[bad] = true

	res, err := Run(context.Background(), f, testFile(4), Options{BatchSize: 4, Resume: true})
	require.NoError(t, err)

	assert.Len(t, res.Imported, 3)
	require.Len(t, res.Failed, 1)
	assert.Equal(t, bad, res.Failed[0].Key)
	assert.Equal(t, "id-1", res.Failed[0].ID)
	assert.Contains(t, res.Failed[0].Err, "resource does not exist")

	assert.Equal(t, 2, f.callCount, "one batch call plus one isolation call")
	assert.Equal(t, []ResourceKey{bad}, f.nonComponentPayloads()[1],
		"isolation call imports exactly the missing resource")
}

func TestRun_IsolationCallsCarryComponents(t *testing.T) {
	t.Parallel()

	file := &ImportFile{
		NameTable: map[string]string{
			"comp-a": "urn:pulumi:dev::proj::example:index:Comp::comp-a",
			"comp-b": "urn:pulumi:dev::proj::example:index:Comp::comp-b",
		},
		Resources: []*optimport.ImportResource{
			{Type: "example:index:Comp", Name: "comp-a", Component: true},
			{Type: "example:index:Comp", Name: "comp-b", Component: true},
			{Type: "aws:s3/bucket:Bucket", Name: "res-0", ID: "id-0", Parent: "comp-a"},
		},
	}

	f := newFakeImporter()
	f.failKeys[ResourceKey{Type: "aws:s3/bucket:Bucket", Name: "res-0"}] = true

	_, err := Run(context.Background(), f, file, Options{BatchSize: 10, Resume: true})
	require.NoError(t, err)

	require.Len(t, f.payloads, 2)
	var componentNames []string
	for _, r := range f.payloads[1] {
		if r.Component {
			componentNames = append(componentNames, r.Name)
		}
	}
	assert.ElementsMatch(t, []string{"comp-a", "comp-b"}, componentNames,
		"isolation call must carry every component entry")
}

func TestRun_ResourceThatFailsInBatchButSucceedsAlone(t *testing.T) {
	t.Parallel()

	f := newFakeImporter()
	f.failInBatchOnly[ResourceKey{Type: "aws:s3/bucket:Bucket", Name: "res-1"}] = true

	res, err := Run(context.Background(), f, testFile(3), Options{BatchSize: 3, Resume: true})
	require.NoError(t, err)

	assert.ElementsMatch(t, []ResourceKey{
		{Type: "aws:s3/bucket:Bucket", Name: "res-0"},
		{Type: "aws:s3/bucket:Bucket", Name: "res-1"},
		{Type: "aws:s3/bucket:Bucket", Name: "res-2"},
	}, res.Imported)
	assert.Empty(t, res.Failed)
	assert.Equal(t, 2, f.callCount, "one batch call plus one isolation call")
}

func TestRun_WholeBatchFailsAndEveryResourceIsReported(t *testing.T) {
	t.Parallel()

	f := newFakeImporter()
	for i := 0; i < 3; i++ {
		f.failKeys[ResourceKey{Type: "aws:s3/bucket:Bucket", Name: fmt.Sprintf("res-%d", i)}] = true
	}

	res, err := Run(context.Background(), f, testFile(3), Options{BatchSize: 3, Resume: true})
	require.NoError(t, err)

	assert.Empty(t, res.Imported)
	assert.Len(t, res.Failed, 3)
	assert.Equal(t, 4, f.callCount, "one batch call plus three isolation calls")
}

func TestRun_ContinuesToLaterBatchesAfterAFailure(t *testing.T) {
	t.Parallel()

	f := newFakeImporter()
	f.failKeys[ResourceKey{Type: "aws:s3/bucket:Bucket", Name: "res-0"}] = true

	res, err := Run(context.Background(), f, testFile(4), Options{BatchSize: 2, Resume: true})
	require.NoError(t, err)

	assert.Len(t, res.Failed, 1)
	assert.Len(t, res.Imported, 3, "batches after the failure still run")
}

func TestRun_PreservesPartialResultsWhenStateReadFails(t *testing.T) {
	t.Parallel()

	f := newFakeImporter()
	// Call 1 is the pre-loop ExistingResources; call 2 is the after-batch-1
	// read (batch 1 succeeds fully); call 3 is the after-batch-2 read, which
	// we fail here to simulate a transient export failure mid-run.
	f.failExistingAfter = 3

	res, err := Run(context.Background(), f, testFile(4), Options{BatchSize: 2, Resume: true})

	require.Error(t, err)
	require.NotNil(t, res, "partial results must survive a mid-run state read failure")
	assert.ElementsMatch(t, []ResourceKey{
		{Type: "aws:s3/bucket:Bucket", Name: "res-0"},
		{Type: "aws:s3/bucket:Bucket", Name: "res-1"},
	}, res.Imported, "the first batch's results must be preserved")
}

func TestRun_ReportsPerBatchProgress(t *testing.T) {
	t.Parallel()

	f := newFakeImporter()
	bad := ResourceKey{Type: "aws:s3/bucket:Bucket", Name: "res-1"}
	f.failKeys[bad] = true

	var progress bytes.Buffer
	_, err := Run(context.Background(), f, testFile(4), Options{
		BatchSize: 2,
		Resume:    true,
		Progress:  &progress,
	})
	require.NoError(t, err)

	out := progress.String()
	assert.Contains(t, out, "Batch 1/2 (2 resources)")
	assert.Contains(t, out, "Batch 2/2 (2 resources)")
	assert.Contains(t, out, "isolating 1 failure(s)")
}

func TestRun_ComponentsOnlyImportsNothing(t *testing.T) {
	t.Parallel()

	f := newFakeImporter()
	res, err := Run(context.Background(), f, testFile(0), Options{BatchSize: 2, Resume: true})
	require.NoError(t, err)

	assert.Equal(t, 0, res.BatchCount)
	assert.Empty(t, res.Imported)
	assert.Empty(t, res.Skipped)
	assert.Empty(t, res.Failed)
	assert.Empty(t, res.Planned)
	assert.Equal(t, 0, f.callCount, "no ImportBatch calls when there is nothing importable")
}

func TestErrText(t *testing.T) {
	t.Parallel()

	assert.Equal(t,
		"resource not present in stack state after import (last error: boom)",
		errText(errors.New("boom"), errors.New("batch")))
	assert.Equal(t,
		"resource not present in stack state after import (last error: batch)",
		errText(nil, errors.New("batch")))
	assert.Equal(t, "resource not present in stack state after import", errText(nil, nil))
}
