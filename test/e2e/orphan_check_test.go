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

//go:build e2e

package e2e

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// withShortOrphanPolling temporarily shrinks the polling interval and
// deadline so tests don't take real time to run.
func withShortOrphanPolling(t *testing.T) {
	t.Helper()
	prevInterval, prevDeadline := orphanPollInterval, orphanPollDeadline
	orphanPollInterval = time.Millisecond
	orphanPollDeadline = 50 * time.Millisecond
	t.Cleanup(func() {
		orphanPollInterval = prevInterval
		orphanPollDeadline = prevDeadline
	})
}

func TestEventuallyGoneSucceedsOnceProbeReportsGone(t *testing.T) {
	withShortOrphanPolling(t)

	calls := 0
	fakeT := &testing.T{}
	eventuallyGone(fakeT, "widget", func() (bool, string, error) {
		calls++
		if calls < 3 {
			return false, "state=\"available\"", nil
		}
		return true, "", nil
	})

	if fakeT.Failed() {
		t.Errorf("eventuallyGone reported a failure for a probe that eventually returned gone")
	}
	if calls != 3 {
		t.Errorf("probe called %d times, want exactly 3", calls)
	}
}

func TestEventuallyGoneReportsOrphanWhenProbeNeverGoesGone(t *testing.T) {
	withShortOrphanPolling(t)

	const detail = `state="available"`
	calls := 0
	fakeT := &testing.T{}
	eventuallyGone(fakeT, "widget", func() (bool, string, error) {
		calls++
		return false, detail, nil
	})

	if !fakeT.Failed() {
		t.Errorf("eventuallyGone did not fail for a probe that never reported gone")
	}
	if calls == 0 {
		t.Errorf("probe was never called")
	}
	// eventuallyGone's terminal message is built by orphanedMessage; verify it
	// carries the last probe's detail, which is what a human needs to act on.
	msg := orphanedMessage("widget", detail)
	if !strings.Contains(msg, "ORPHANED") || !strings.Contains(msg, detail) {
		t.Errorf("orphaned message %q does not contain ORPHANED and the detail %q", msg, detail)
	}
}

func TestEventuallyGoneTreatsProbeErrorAsUnverifiedNotOrphaned(t *testing.T) {
	withShortOrphanPolling(t)

	calls := 0
	fakeT := &testing.T{}
	eventuallyGone(fakeT, "widget", func() (bool, string, error) {
		calls++
		return false, "", errors.New("access denied")
	})

	if !fakeT.Failed() {
		t.Errorf("eventuallyGone did not fail when the probe returned an error")
	}
	if calls != 1 {
		t.Errorf("probe called %d times on error, want exactly 1", calls)
	}
}
