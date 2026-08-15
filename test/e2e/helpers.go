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

// Package e2e holds the end-to-end test that drives a real AWS fixture
// through digest/resolve/patch-state and asserts non-importable resources
// go from "create" to "same". This file has no "e2e" build tag: it holds
// pure, offline-testable helpers used by e2e_test.go, so `go test ./...`
// still exercises them without touching AWS.
package e2e

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// expectedURN builds the URN "pulumi preview --json" would emit for an
// unparented resource, matching the format pkg.extractTypeFromURN expects:
// "urn:pulumi:{stack}::{project}::{type}::{name}".
func expectedURN(project, stack, typ, name string) string {
	return fmt.Sprintf("urn:pulumi:%s::%s::%s::%s", stack, project, typ, name)
}

// nonImportableSidecarPath mirrors cmd/non_importable.go's unexported
// nonImportablePath: "resolve tf" writes the non-importable sidecar next to
// its --out import file, replacing the extension with ".non-importable.json"
// ("imports-ready.json" -> "imports-ready.non-importable.json"). Recomputed
// here rather than imported because cmd's helper is unexported.
func nonImportableSidecarPath(importFilePath string) string {
	ext := filepath.Ext(importFilePath)
	return strings.TrimSuffix(importFilePath, ext) + ".non-importable.json"
}

// diffPathLimit caps how many differing paths jsonPathDiff reports, so a
// wholesale mismatch cannot flood the terminal with thousands of lines.
const diffPathLimit = 40

// jsonPathDiff returns the dotted paths at which two decoded JSON documents
// differ, without ever including their values.
//
// It exists because the documents it is used on — stack exports taken with
// --show-secrets — contain decrypted secrets, so a failure must be able to say
// WHERE two states diverged without printing WHAT diverged.
//
// A path is reported when a leaf differs, when a key is present on one side
// only, or when the two sides have different types or lengths at the same
// path. Recursion stops at the first difference on a given branch: knowing
// that an object appeared is enough, and descending into it would list every
// leaf beneath it.
func jsonPathDiff(a, b interface{}, path string) []string {
	if path == "" {
		path = "$"
	}
	switch av := a.(type) {
	case map[string]interface{}:
		bv, ok := b.(map[string]interface{})
		if !ok {
			return []string{path + " (type differs)"}
		}
		keys := map[string]bool{}
		for k := range av {
			keys[k] = true
		}
		for k := range bv {
			keys[k] = true
		}
		names := make([]string, 0, len(keys))
		for k := range keys {
			names = append(names, k)
		}
		sort.Strings(names)
		var out []string
		for _, k := range names {
			x, inA := av[k]
			y, inB := bv[k]
			switch {
			case !inA:
				out = append(out, fmt.Sprintf("%s.%s (only after)", path, k))
			case !inB:
				out = append(out, fmt.Sprintf("%s.%s (only before)", path, k))
			default:
				out = append(out, jsonPathDiff(x, y, path+"."+k)...)
			}
		}
		return out
	case []interface{}:
		bv, ok := b.([]interface{})
		if !ok {
			return []string{path + " (type differs)"}
		}
		if len(av) != len(bv) {
			return []string{fmt.Sprintf("%s (length %d before, %d after)", path, len(av), len(bv))}
		}
		var out []string
		for i := range av {
			out = append(out, jsonPathDiff(av[i], bv[i], fmt.Sprintf("%s[%d]", path, i))...)
		}
		return out
	default:
		if fmt.Sprintf("%v", a) != fmt.Sprintf("%v", b) {
			return []string{path}
		}
		return nil
	}
}

// sanitizedEnv returns the current process environment with AWS_PROFILE
// removed, plus any extra "KEY=VALUE" entries appended.
//
// AWS_PROFILE is commonly exported by a developer shell this test runs
// in and shadows the AWS credentials the ESC wrapper injects, causing every
// AWS call to fail with "the config profile (...) could not be
// found". The wrapper documented at the top of e2e_test.go already runs the
// test under "env -u AWS_PROFILE", which keeps it out of os.Environ() in
// the first place; this is a second, in-process line of defense so a test
// run outside that exact wrapper invocation still fails for the right
// reason (real AWS error) instead of the profile trap.
func sanitizedEnv(extra ...string) []string {
	env := make([]string, 0, len(os.Environ())+len(extra))
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "AWS_PROFILE=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, extra...)
}

// copyDir recursively copies src into dst, creating dst if needed. Used to
// give tofu and pulumi a writable working copy of the read-only testdata
// fixtures (tofu writes .terraform/, terraform.tfstate, lock files; pulumi
// writes stack state under the local backend) without mutating the
// checked-in fixtures.
func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("reading %s: %w", src, err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dst, err)
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(srcPath, dstPath); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening %s: %w", src, err)
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", src, err)
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("creating %s: %w", dst, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copying %s to %s: %w", src, dst, err)
	}
	return nil
}
