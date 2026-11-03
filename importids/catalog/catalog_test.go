// Copyright 2016-2026, Pulumi Corporation.
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

package catalog

import "testing"

func TestExpand(t *testing.T) {
	got, err := Expand("{group}/{id}", map[string]interface{}{"group": "prod"}, "stream")
	if err != nil || got != "prod/stream" {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, attrs := range []map[string]interface{}{nil, {"group": ""}, {"group": "(sensitive)"}} {
		if _, err := Expand("{group}/{id}", attrs, "stream"); err == nil {
			t.Fatal("accepted missing/redacted attribute")
		}
	}
}

func TestEmbeddedProvenanceMustMatch(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("accepted mismatched provenance")
		}
	}()
	LoadEmbedded([]byte(`{"provider":"hashicorp/aws","pulumiVersion":"v7.48.0","version":"v6.66.0"}`),
		[]byte(`{"provider":"hashicorp/aws","pulumiVersion":"v6.83.4","upstreamVersion":"v5.100.0"}`), nil)
}
