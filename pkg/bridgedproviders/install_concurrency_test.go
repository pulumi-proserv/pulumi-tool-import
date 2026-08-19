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

//go:build providerload

package bridgedproviders

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEnsureProviderInstalledIsSafeForConcurrentCallers reproduces the second
// half of the CI failure: parallel tests each bridge the aws schema, so each
// installs ~/.pulumi/plugins/resource-aws-v<version>/pulumi-resource-aws and
// then execs it for its mapping. On a cold plugin directory one caller execs
// the binary while another is still writing it, and CI reported
// "fork/exec ...: text file busy".
//
// Run with: go test -tags providerload ./pkg/bridgedproviders/
//
// Tagged because it points PULUMI_HOME at an empty directory on purpose and so
// downloads the provider rather than reusing the shared plugin cache.
//
// Caveat: this passed on macOS with the lock reverted, so a green run here is
// not evidence the lock works — the kernels differ. Linux refuses to exec a
// file another process holds open for writing (ETXTBSY, what CI reported);
// macOS refuses the write instead, and otherwise lets the exec proceed against
// whatever bytes are there. pkg/pathlock's tests are the deterministic guard;
// this exercises the real path on the platform CI runs.
func TestEnsureProviderInstalledIsSafeForConcurrentCallers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PULUMI_HOME", filepath.Join(home, ".pulumi"))

	const callers = 4
	ctx := context.Background()

	var wg sync.WaitGroup
	results := make([]*InstallProviderResult, callers)
	errs := make([]error, callers)
	mappingErrs := make([]error, callers)
	start := make(chan struct{})
	for i := range callers {
		wg.Go(func() {
			<-start
			results[i], errs[i] = EnsureProviderInstalled(ctx, InstallProviderOptions{
				Name:    "aws",
				Version: "6.83.2",
			})
			if errs[i] != nil {
				return
			}
			// The exec is where the race surfaced in CI, so the
			// reproduction has to run it, not just the install.
			_, mappingErrs[i] = GetMappingFromBinary(ctx, results[i].BinaryPath, GetMappingOptions{
				Key:      "terraform",
				Provider: "aws",
			})
		})
	}
	close(start)
	wg.Wait()

	for i := range callers {
		require.NoErrorf(t, errs[i], "caller %d failed to install the provider", i)
		require.NoErrorf(t, mappingErrs[i], "caller %d failed to read the mapping from the binary", i)
	}
}
