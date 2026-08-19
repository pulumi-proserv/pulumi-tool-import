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

package pathlock

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAcquireExcludesTheSameKey(t *testing.T) {
	t.Parallel()

	release := Acquire(t.Name())

	acquired := make(chan struct{})
	go func() {
		defer close(acquired)
		Acquire(t.Name())()
	}()

	select {
	case <-acquired:
		t.Fatal("a second caller acquired the lock while it was held")
	case <-time.After(50 * time.Millisecond):
	}

	release()

	select {
	case <-acquired:
	case <-time.After(10 * time.Second):
		t.Fatal("the second caller never acquired the lock after release")
	}
}

func TestAcquireDoesNotExcludeOtherKeys(t *testing.T) {
	t.Parallel()

	release := Acquire(t.Name())
	defer release()

	// A global lock would deadlock here rather than fail an assertion; the
	// timeout is what turns that into a readable failure.
	done := make(chan struct{})
	go func() {
		defer close(done)
		Acquire(t.Name() + "/other")()
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a different key blocked on a lock held for another key")
	}
}

// TestAcquireSerializesContendingCallers is the property the loaders depend on:
// however many callers race, only one is ever inside the critical section, so
// only one writes the binary and nobody execs a half-written one.
func TestAcquireSerializesContendingCallers(t *testing.T) {
	t.Parallel()

	const callers = 16
	var inside, maxInside atomic.Int32

	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			release := Acquire(t.Name())
			defer release()
			n := inside.Add(1)
			for {
				old := maxInside.Load()
				if n <= old || maxInside.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			inside.Add(-1)
		})
	}
	wg.Wait()

	if got := maxInside.Load(); got != 1 {
		t.Fatalf("%d callers were inside the critical section at once, want 1", got)
	}
}
