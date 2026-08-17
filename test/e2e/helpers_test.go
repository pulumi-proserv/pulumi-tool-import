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
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestJSONPathDiffNamesPathsWithoutValues is the guard on the property that
// makes jsonPathDiff safe to print: the stack exports it compares are taken
// with --show-secrets, so a reported path must never carry the value at that
// path. Here the differing leaf is a "secret" string, and the output must
// locate it without quoting it.
func TestJSONPathDiffNamesPathsWithoutValues(t *testing.T) {
	t.Parallel()
	var a, b interface{}
	if err := json.Unmarshal([]byte(`{"r":[{"outputs":{"key":"s3cret-before","keep":1}}]}`), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"r":[{"outputs":{"key":"s3cret-after","keep":1}}]}`), &b); err != nil {
		t.Fatal(err)
	}

	paths := jsonPathDiff(a, b, "")
	if len(paths) != 1 || paths[0] != "$.r[0].outputs.key" {
		t.Fatalf("jsonPathDiff = %q, want exactly [\"$.r[0].outputs.key\"]", paths)
	}
	for _, p := range paths {
		for _, secret := range []string{"s3cret-before", "s3cret-after"} {
			if strings.Contains(p, secret) {
				t.Errorf("path %q leaks the value at that path — these documents hold decrypted secrets", p)
			}
		}
	}
}

func TestJSONPathDiffIdenticalDocuments(t *testing.T) {
	t.Parallel()
	var a, b interface{}
	doc := `{"r":[{"outputs":{"key":"v"}}],"n":2}`
	if err := json.Unmarshal([]byte(doc), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(doc), &b); err != nil {
		t.Fatal(err)
	}
	if paths := jsonPathDiff(a, b, ""); len(paths) != 0 {
		t.Errorf("jsonPathDiff on identical documents = %q, want none", paths)
	}
}

// TestJSONPathDiffReportsPresenceAndShape covers the three non-leaf cases:
// a key on one side only, a length change, and a type change. Each must be
// reported once at the point of difference rather than recursed into.
func TestJSONPathDiffReportsPresenceAndShape(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, before, after, want string
	}{
		{"only before", `{"a":1,"b":{"deep":1}}`, `{"a":1}`, "$.b (only before)"},
		{"only after", `{"a":1}`, `{"a":1,"b":{"deep":1}}`, "$.b (only after)"},
		{"length", `{"a":[1,2]}`, `{"a":[1]}`, "$.a (length 2 before, 1 after)"},
		{"type", `{"a":{"x":1}}`, `{"a":[1]}`, "$.a (type differs)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var a, b interface{}
			if err := json.Unmarshal([]byte(tc.before), &a); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.after), &b); err != nil {
				t.Fatal(err)
			}
			paths := jsonPathDiff(a, b, "")
			if len(paths) != 1 || paths[0] != tc.want {
				t.Errorf("jsonPathDiff = %q, want exactly [%q]", paths, tc.want)
			}
		})
	}
}

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
	t.Setenv("AWS_PROFILE", "some-profile")
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

// fixtureStateJSON is a Terraform state in the shape "tofu apply" writes for
// testdata/tf/main.tf, trimmed to the resources loadFixtureResourceIDs looks
// for. The load-bearing detail is aws_iot_certificate.each: a for_each- (or
// count-) expanded resource is ONE "resources" entry holding one instance per
// key, which is the shape that has to be got right.
const fixtureStateJSON = `{
  "version": 4,
  "resources": [
    {"mode":"managed","type":"aws_vpc","name":"main",
     "instances":[{"attributes":{"id":"vpc-0abc"}}]},
    {"mode":"managed","type":"aws_vpn_gateway","name":"vgw",
     "instances":[{"attributes":{"id":"vgw-0abc"}}]},
    {"mode":"managed","type":"aws_customer_gateway","name":"cgw",
     "instances":[{"attributes":{"id":"cgw-0abc"}}]},
    {"mode":"managed","type":"aws_vpn_connection","name":"vpn",
     "instances":[{"attributes":{"id":"vpn-0abc"}}]},
    {"mode":"managed","type":"aws_iot_certificate","name":"cert",
     "instances":[{"attributes":{"id":"cert-west"}}]},
    {"mode":"managed","type":"aws_iot_certificate","name":"east",
     "instances":[{"attributes":{"id":"cert-east"}}]},
    {"mode":"managed","type":"aws_iot_certificate","name":"each",
     "instances":[
       {"index_key":"alpha","attributes":{"id":"cert-alpha"}},
       {"index_key":"beta","attributes":{"id":"cert-beta"}}
     ]},
    {"mode":"managed","type":"aws_vpclattice_target_group","name":"tg",
     "instances":[{"attributes":{"id":"tg-0abc"}}]},
    {"mode":"managed","type":"aws_lambda_function","name":"target",
     "instances":[{"attributes":{"function_name":"tool-import-e2e-lambda"}}]},
    {"mode":"managed","type":"aws_iam_role","name":"lambda",
     "instances":[{"attributes":{"name":"tool-import-e2e-lambda-role"}}]},
    {"mode":"data","type":"aws_caller_identity","name":"current",
     "instances":[{"attributes":{"id":"123456789012"}}]}
  ]
}`

// TestLoadFixtureResourceIDsCollectsEveryForEachInstance pins the regression
// that made this worth testing offline: the loop read Instances[0] only, so
// the "beta" certificate was never collected and had no detection path at all
// — IoT certificates carry no tags, so the tag scan cannot back one up. A
// missed ID here fails nothing; it just silently skips an AWS-side check.
func TestLoadFixtureResourceIDsCollectsEveryForEachInstance(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "terraform.tfstate"), []byte(fixtureStateJSON), 0o600); err != nil {
		t.Fatalf("writing state fixture: %v", err)
	}

	ids, err := loadFixtureResourceIDs(dir)
	if err != nil {
		t.Fatalf("loadFixtureResourceIDs() error = %v", err)
	}

	want := []string{"cert-alpha", "cert-beta"}
	if len(ids.eachIoTCertificateIDs) != len(want) {
		t.Fatalf("eachIoTCertificateIDs = %v, want %v — a for_each resource is one entry "+
			"with one instance per key, so every instance must be read", ids.eachIoTCertificateIDs, want)
	}
	for i, w := range want {
		if ids.eachIoTCertificateIDs[i] != w {
			t.Errorf("eachIoTCertificateIDs[%d] = %q, want %q", i, ids.eachIoTCertificateIDs[i], w)
		}
	}
}

// TestLoadFixtureResourceIDsReadsEveryCheckedResource guards the other half of
// the same failure mode: every field verifyFixtureResourcesGone gates a check
// on must actually get populated, since an empty one skips its check silently.
func TestLoadFixtureResourceIDsReadsEveryCheckedResource(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "terraform.tfstate"), []byte(fixtureStateJSON), 0o600); err != nil {
		t.Fatalf("writing state fixture: %v", err)
	}

	ids, err := loadFixtureResourceIDs(dir)
	if err != nil {
		t.Fatalf("loadFixtureResourceIDs() error = %v", err)
	}

	for _, tt := range []struct {
		field string
		got   string
		want  string
	}{
		{"vpcID", ids.vpcID, "vpc-0abc"},
		{"vpnGatewayID", ids.vpnGatewayID, "vgw-0abc"},
		{"customerGatewayID", ids.customerGatewayID, "cgw-0abc"},
		{"vpnConnectionID", ids.vpnConnectionID, "vpn-0abc"},
		{"iotCertificateID", ids.iotCertificateID, "cert-west"},
		{"eastIoTCertificateID", ids.eastIoTCertificateID, "cert-east"},
		{"targetGroupID", ids.targetGroupID, "tg-0abc"},
		{"lambdaFunctionName", ids.lambdaFunctionName, "tool-import-e2e-lambda"},
		{"iamRoleName", ids.iamRoleName, "tool-import-e2e-lambda-role"},
	} {
		if tt.got != tt.want {
			t.Errorf("%s = %q, want %q", tt.field, tt.got, tt.want)
		}
	}
}

// TestLoadFixtureResourceIDsMissingStateIsNotAnError covers the path where
// "tofu init" never wrote state. That must not error — the tag-based VPC scan
// still has to run — but it must also not be mistaken for "nothing to check".
func TestLoadFixtureResourceIDsMissingStateIsNotAnError(t *testing.T) {
	t.Parallel()

	ids, err := loadFixtureResourceIDs(t.TempDir())
	if err != nil {
		t.Fatalf("loadFixtureResourceIDs() on a missing state file error = %v, want nil", err)
	}
	if ids.vpcID != "" || len(ids.eachIoTCertificateIDs) != 0 {
		t.Errorf("loadFixtureResourceIDs() on a missing state file = %+v, want zero value", ids)
	}
}
