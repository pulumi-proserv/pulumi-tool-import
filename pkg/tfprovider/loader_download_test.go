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

package tfprovider

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLoadProviderIsSafeForConcurrentCallers is the end-to-end reproduction of
// the install/exec race: several callers load one provider into an empty cache
// directory, which is what a package of t.Parallel() tests does on a cold
// cache. Reverting the lock in getProviderServer fails it.
//
// Run with: go test -tags providerload ./pkg/tfprovider/
//
// It is tagged rather than run by default because it downloads the aws
// provider (~650MB) into a temporary directory, deliberately bypassing the
// shared cache. The size is not incidental: the same test against
// hashicorp/random (~17MB) passed even without the lock, because the window
// between writing the binary and exec-ing it is too narrow to lose reliably.
func TestLoadProviderIsSafeForConcurrentCallers(t *testing.T) {
	// An empty cache directory is the whole point: a warm cache takes the
	// early-return path in getProviderServer and never races.
	t.Setenv(envPluginCache, t.TempDir())

	const callers = 4
	ctx := context.Background()

	var wg sync.WaitGroup
	errs := make([]error, callers)
	provs := make([]Provider, callers)
	start := make(chan struct{})
	for i := range callers {
		wg.Go(func() {
			<-start
			provs[i], errs[i] = LoadProvider(ctx, "registry.opentofu.org/hashicorp/aws", "5.100.0")
		})
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if provs[i] != nil {
			defer func() { _ = provs[i].Close(ctx) }()
		}
		require.NoErrorf(t, err, "caller %d failed to load the provider", i)
	}
}
