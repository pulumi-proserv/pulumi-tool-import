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

package tofu

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pulumi/opentofu/depsfile"
)

// LockFileName is the dependency lock file written by "terraform init".
const LockFileName = ".terraform.lock.hcl"

// LockedProviderVersions reads .terraform.lock.hcl from a Terraform project
// directory and returns the exact locked version of each provider, keyed by
// full provider source address (e.g. "registry.terraform.io/hashicorp/aws").
//
// A project with no lock file returns an empty map and no error: the lock file
// is the most accurate source of the versions that produced the state, but it
// is not always present.
func LockedProviderVersions(projectDir string) (map[string]string, error) {
	path := filepath.Join(projectDir, LockFileName)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	locks, diags := depsfile.LoadLocksFromFile(path)
	if diags.HasErrors() {
		return nil, fmt.Errorf("parsing %s: %w", path, diags.Err())
	}

	versions := map[string]string{}
	for addr, lock := range locks.AllProviders() {
		versions[addr.String()] = lock.Version().String()
	}
	return versions, nil
}
