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

package tfprovider

import (
	"testing"
	"time"

	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/vendored/opentofu/addrs"

	"github.com/apparentlymart/go-versions/versions"
	"github.com/stretchr/testify/require"
)

func testAddr(t *testing.T, s string) addrs.Provider {
	t.Helper()
	addr, diags := addrs.ParseProviderSourceString(s)
	require.False(t, diags.HasErrors(), "parse %q", s)
	return addr
}

func testVersion(t *testing.T, s string) versions.Version {
	t.Helper()
	v, err := versions.ParseVersion(s)
	require.NoError(t, err)
	return v
}

func TestProviderInstallLockExcludesTheSamePath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	addr := testAddr(t, "registry.opentofu.org/hashicorp/aws")
	version := testVersion(t, "5.100.0")

	unlock := lockProviderInstall(dir, addr, version)

	acquired := make(chan struct{})
	go func() {
		defer close(acquired)
		lockProviderInstall(dir, addr, version)()
	}()

	select {
	case <-acquired:
		t.Fatal("a second caller acquired the lock for the same provider path while it was held")
	case <-time.After(50 * time.Millisecond):
	}

	unlock()

	select {
	case <-acquired:
	case <-time.After(10 * time.Second):
		t.Fatal("the second caller never acquired the lock after it was released")
	}
}

func TestProviderInstallLockIsPerPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	aws := testAddr(t, "registry.opentofu.org/hashicorp/aws")
	random := testAddr(t, "registry.opentofu.org/hashicorp/random")
	v5 := testVersion(t, "5.100.0")
	v6 := testVersion(t, "6.0.0")

	held := lockProviderInstall(dir, aws, v5)
	defer held()

	for name, unlock := range map[string]func(){
		"different provider":  lockProviderInstall(dir, random, v5),
		"different version":   lockProviderInstall(dir, aws, v6),
		"different cache dir": lockProviderInstall(t.TempDir(), aws, v5),
	} {
		require.NotNil(t, unlock, name)
		unlock()
	}
}
