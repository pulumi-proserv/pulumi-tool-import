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
