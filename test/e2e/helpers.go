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

func expectedURN(project, stack, typ, name string) string {
	return fmt.Sprintf("urn:pulumi:%s::%s::%s::%s", stack, project, typ, name)
}

func nonImportableSidecarPath(importFilePath string) string {
	ext := filepath.Ext(importFilePath)
	return strings.TrimSuffix(importFilePath, ext) + ".non-importable.json"
}

const diffPathLimit = 40

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

type fixtureResourceIDs struct {
	vpcID              string
	vpnGatewayID       string
	customerGatewayID  string
	vpnConnectionID    string
	iotCertificateID   string
	targetGroupID      string
	lambdaFunctionName string
	iamRoleName        string

	eastIoTCertificateID string

	eachIoTCertificateIDs []string

	moduleIoTCertificateID string
}

func loadFixtureResourceIDs(tfDir string) (fixtureResourceIDs, error) {
	var ids fixtureResourceIDs

	statePath := filepath.Join(tfDir, "terraform.tfstate")
	data, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return ids, nil
		}
		return ids, fmt.Errorf("reading %s: %w", statePath, err)
	}

	var state struct {
		Resources []struct {
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

var goneErrorCodes = map[string]bool{
	"InvalidVpnConnectionID.NotFound":   true,
	"InvalidVpnGatewayID.NotFound":      true,
	"InvalidCustomerGatewayID.NotFound": true,
	"ResourceNotFoundException":         true,
	"NoSuchEntity":                      true,
}

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
