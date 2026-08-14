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

// No "e2e" build tag on this file: these are offline unit tests for the pure
// helpers in helpers.go, and must run under plain "go test ./...".
package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpectedURN(t *testing.T) {
	t.Parallel()

	got := expectedURN("tool-import-e2e", "e2e-123",
		"aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation", "prop[0]")
	want := "urn:pulumi:e2e-123::tool-import-e2e::" +
		"aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation::prop[0]"
	if got != want {
		t.Errorf("expectedURN() = %q, want %q", got, want)
	}
}

func TestNonImportableSidecarPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		importFile string
		want       string
	}{
		{"imports-ready.json", "imports-ready.non-importable.json"},
		{"/tmp/e2e/filled-import.json", "/tmp/e2e/filled-import.non-importable.json"},
		{"noext", "noext.non-importable.json"},
	}
	for _, tt := range tests {
		t.Run(tt.importFile, func(t *testing.T) {
			if got := nonImportableSidecarPath(tt.importFile); got != tt.want {
				t.Errorf("nonImportableSidecarPath(%q) = %q, want %q", tt.importFile, got, tt.want)
			}
		})
	}
}

func TestSanitizedEnvDropsAWSProfile(t *testing.T) {
	t.Setenv("AWS_PROFILE", "devsandbox")
	t.Setenv("SOME_OTHER_VAR", "kept")

	env := sanitizedEnv("EXTRA=1")

	for _, kv := range env {
		if strings.HasPrefix(kv, "AWS_PROFILE=") {
			t.Fatalf("sanitizedEnv() kept AWS_PROFILE: %v", env)
		}
	}
	var sawOther, sawExtra bool
	for _, kv := range env {
		if kv == "SOME_OTHER_VAR=kept" {
			sawOther = true
		}
		if kv == "EXTRA=1" {
			sawExtra = true
		}
	}
	if !sawOther {
		t.Errorf("sanitizedEnv() dropped an unrelated var: %v", env)
	}
	if !sawExtra {
		t.Errorf("sanitizedEnv() did not append extra entries: %v", env)
	}
}

func TestCopyDir(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "nested", "dst")

	if err := os.WriteFile(filepath.Join(src, "main.tf"), []byte("root file"), 0o644); err != nil {
		t.Fatal(err)
	}
	subdir := filepath.Join(src, "modules", "vpc")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "vpc.tf"), []byte("module file"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := copyDir(src, dst); err != nil {
		t.Fatalf("copyDir() error = %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dst, "main.tf"))
	if err != nil {
		t.Fatalf("reading copied root file: %v", err)
	}
	if string(got) != "root file" {
		t.Errorf("root file content = %q, want %q", got, "root file")
	}

	got, err = os.ReadFile(filepath.Join(dst, "modules", "vpc", "vpc.tf"))
	if err != nil {
		t.Fatalf("reading copied nested file: %v", err)
	}
	if string(got) != "module file" {
		t.Errorf("nested file content = %q, want %q", got, "module file")
	}

	// The source fixture must be untouched.
	if _, err := os.Stat(filepath.Join(src, "main.tf")); err != nil {
		t.Errorf("source file disappeared: %v", err)
	}
}
