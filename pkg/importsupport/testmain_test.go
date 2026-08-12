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

package importsupport

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg/tfprovider"
)

// TestMain installs the test provider once, serially, before the parallel
// tests run. Each parallel test loads the provider itself, and on a cold cache
// they race: one forks/execs the binary while another is still writing it, and
// the exec fails with "text file busy". The load failure is not loud — the
// prober falls back to the curated list and reports Unknown — so the race
// surfaces as a confusing assertion failure rather than an install error.
//
// pkg/testmain_test.go does the same for the bridged providers.
func TestMain(m *testing.M) {
	ctx := context.Background()
	if provider, err := tfprovider.LoadProvider(ctx, randomProvider, randomProviderVersion); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: pre-warm of %s %s failed: %v\n",
			randomProvider, randomProviderVersion, err)
	} else {
		provider.Close(ctx)
	}
	os.Exit(m.Run())
}
