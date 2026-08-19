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

// Package pathlock serializes callers that install a binary and then run it.
//
// Both provider loaders in this repository have the same shape: look for a
// binary in a shared cache, download it if it is missing, then fork/exec it.
// Concurrent callers that all miss the cache write and exec one path at the
// same time. The loser fails — "text file busy" on Linux, or, where the kernel
// allows the exec, a plugin handshake against a partially written binary — and
// both callers download the same payload.
//
// The lock is per process. Separate processes sharing ~/.pulumi (for instance
// "go test ./..." running two package binaries at once) can still collide;
// closing that would need a lock file, and no such collision has been observed.
package pathlock

import "sync"

var locks sync.Map // map[string]*sync.Mutex

// Acquire blocks until no other caller holds key, and returns the release
// function. Callers that hold it across the exec as well as the install are
// not required to: an exec can only follow this caller's own install, which
// already excludes any concurrent writer.
//
// Use one key per file path being written. Keys that name different paths do
// not contend.
func Acquire(key string) (release func()) {
	lock, _ := locks.LoadOrStore(key, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}
