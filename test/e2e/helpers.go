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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aws/smithy-go"
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

// fixtureResourceIDs are the identifiers of the AWS resources
// testdata/tf/main.tf creates, as read out of the fixture's own Terraform
// state. Any field can be empty — a partial apply may not have created
// (and therefore recorded) every resource.
//
// This type and loadFixtureResourceIDs live here, rather than beside their
// only caller in orphan_check.go, because parsing state is pure and worth
// testing offline: a silent parsing gap here disables an AWS-side check
// without failing anything, which is how the for_each certificates below
// went unverified.
type fixtureResourceIDs struct {
	vpcID              string
	vpnGatewayID       string
	customerGatewayID  string
	vpnConnectionID    string
	iotCertificateID   string
	targetGroupID      string
	lambdaFunctionName string
	iamRoleName        string

	// Resources created under the aliased "aws.east" provider, which live in
	// secondaryRegion rather than fixtureRegion. Kept separate because
	// verifying them needs a second AWS config: a client pinned to
	// fixtureRegion reports a us-east-1 certificate as simply absent, which
	// is indistinguishable from "cleaned up".
	eastIoTCertificateID string

	// for_each-keyed certificates (aws_iot_certificate.each). A count- or
	// for_each-expanded resource appears once per key in state, so these
	// cannot be a single field.
	eachIoTCertificateIDs []string

	// The certificate inside modules/certs (module.certs, the component-parent
	// scenario). Kept distinct from iotCertificateID only because it is a
	// separate resource: a module resource carries a "module" key in state and
	// its own resource name, so matching on type and name alone would either
	// miss it or confuse it with a root-level resource of the same name.
	//
	// It has no tags — IoT certificates take none — so the tag scan cannot back
	// this up. Without an explicit case here the certificate had no orphan
	// detection at all.
	moduleIoTCertificateID string
}

// loadFixtureResourceIDs reads <tfDir>/terraform.tfstate directly (the
// local backend file "tofu apply"/"tofu init" write to; see runTofu) and
// extracts the "id" attribute of each resource type this fixture creates
// that verifyFixtureResourcesGone checks for. It must be called before
// "tofu destroy" runs — destroy removes these resources from state, which
// is exactly why this reads state only to learn an ID, never to conclude
// anything about whether the resource still exists.
func loadFixtureResourceIDs(tfDir string) (fixtureResourceIDs, error) {
	var ids fixtureResourceIDs

	statePath := filepath.Join(tfDir, "terraform.tfstate")
	data, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			// "tofu init" (or even "tofu apply") never got far enough to
			// write a state file — nothing to look up by ID, but the
			// tag-based VPC scan in verifyFixtureResourcesGone still runs.
			return ids, nil
		}
		return ids, fmt.Errorf("reading %s: %w", statePath, err)
	}

	var state struct {
		Resources []struct {
			// Module is absent for root-module resources and holds an address
			// like "module.certs" otherwise. Decoded so a module resource can
			// be told apart from a root one with the same type and name.
			Module    string `json:"module"`
			Mode      string `json:"mode"`
			Type      string `json:"type"`
			Name      string `json:"name"`
			Instances []struct {
				Attributes map[string]json.RawMessage `json:"attributes"`
			} `json:"instances"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return ids, fmt.Errorf("parsing %s: %w", statePath, err)
	}

	attrString := func(attrs map[string]json.RawMessage, key string) string {
		raw, ok := attrs[key]
		if !ok {
			return ""
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return ""
		}
		return s
	}

	for _, r := range state.Resources {
		if r.Mode != "managed" || len(r.Instances) == 0 {
			continue
		}
		attrs := r.Instances[0].Attributes
		switch {
		case r.Type == "aws_vpc" && r.Name == "main":
			ids.vpcID = attrString(attrs, "id")
		case r.Type == "aws_vpn_gateway" && r.Name == "vgw":
			ids.vpnGatewayID = attrString(attrs, "id")
		case r.Type == "aws_customer_gateway" && r.Name == "cgw":
			ids.customerGatewayID = attrString(attrs, "id")
		case r.Type == "aws_vpn_connection" && r.Name == "vpn":
			ids.vpnConnectionID = attrString(attrs, "id")
		case r.Type == "aws_iot_certificate" && r.Name == "cert":
			ids.iotCertificateID = attrString(attrs, "id")
		case r.Type == "aws_iot_certificate" && r.Name == "east":
			ids.eastIoTCertificateID = attrString(attrs, "id")
		case r.Type == "aws_iot_certificate" && r.Name == "inmodule" && r.Module == "module.certs":
			ids.moduleIoTCertificateID = attrString(attrs, "id")
		case r.Type == "aws_iot_certificate" && r.Name == "each":
			// Every instance, not just the first: main.tf's for_each over
			// {"alpha","beta"} produces ONE resources entry with two
			// instances, so reading Instances[0] alone left the second
			// certificate with no detection path at all — IoT certificates
			// carry no tags, so the tag scan cannot back it up either.
			for _, inst := range r.Instances {
				if id := attrString(inst.Attributes, "id"); id != "" {
					ids.eachIoTCertificateIDs = append(ids.eachIoTCertificateIDs, id)
				}
			}
		case r.Type == "aws_vpclattice_target_group" && r.Name == "tg":
			ids.targetGroupID = attrString(attrs, "id")
		case r.Type == "aws_lambda_function" && r.Name == "target":
			ids.lambdaFunctionName = attrString(attrs, "function_name")
		case r.Type == "aws_iam_role" && r.Name == "lambda":
			ids.iamRoleName = attrString(attrs, "name")
		}
	}
	return ids, nil
}

// goneErrorCodes are the AWS API error codes that mean "this resource does
// not exist", keyed by the exact ErrorCode() the SDK returns.
//
// Enumerated rather than substring-matched. The previous implementation
// tested strings.Contains(err.Error(), "NotFound") and its comment claimed
// "every one of them contains it" — IAM's does not. NoSuchEntityException
// formats as "NoSuchEntity: ...", so a correctly destroyed role read as an
// unverified survivor and t.Errorf'd on every clean teardown, which also
// means a genuinely orphaned role was indistinguishable from a healthy one.
// That is the same false signal this whole file exists to prevent, so the
// codes are now listed explicitly and covered by a test.
var goneErrorCodes = map[string]bool{
	// ec2: VPN connection, VPN gateway, customer gateway.
	"InvalidVpnConnectionID.NotFound":   true,
	"InvalidVpnGatewayID.NotFound":      true,
	"InvalidCustomerGatewayID.NotFound": true,
	// iot, lambda, vpclattice.
	"ResourceNotFoundException": true,
	// iam. Note the code is "NoSuchEntity", NOT "NoSuchEntityException" —
	// the type is named ...Exception but ErrorCode() drops the suffix.
	"NoSuchEntity": true,
}

// isNotFoundErr reports whether err is an AWS "this resource is gone" error.
//
// It reads the typed smithy error code rather than formatting the error and
// searching the text, so a message that happens to contain a code substring
// cannot be mistaken for one. An unrecognised code returns false, which is
// the safe direction: the caller then reports the resource as unverified
// rather than silently assuming it was cleaned up.
func isNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return goneErrorCodes[apiErr.ErrorCode()]
	}
	return false
}
