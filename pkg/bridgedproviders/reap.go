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

package bridgedproviders

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
)

// envPluginCache is the override the terraform-provider plugin honours for
// where it caches Terraform providers; pkg/tfprovider reads the same one.
const envPluginCache = "PULUMI_DYNAMIC_TF_PLUGIN_CACHE_DIR"

func pluginCacheDir() (string, error) {
	if dir := os.Getenv(envPluginCache); dir != "" {
		return dir, nil
	}
	return workspace.GetPulumiPath("dynamic_tf_plugins")
}

type providerProcess struct {
	pid, ppid int
}

// parseProviderProcesses reads "ps -axo pid=,ppid=,args=" output and keeps
// the processes whose executable lives under cacheDir.
func parseProviderProcesses(psOutput, cacheDir string) []providerProcess {
	prefix := filepath.Clean(cacheDir) + string(filepath.Separator)
	var procs []providerProcess
	for _, line := range strings.Split(psOutput, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		if !strings.HasPrefix(fields[2], prefix) {
			continue
		}
		procs = append(procs, providerProcess{pid: pid, ppid: ppid})
	}
	return procs
}

// orphans returns the providers whose parent has died. A Terraform provider
// only ever talks to the process that started it, so once that process is
// gone nothing can use the provider again.
func orphans(procs []providerProcess) []int {
	var pids []int
	for _, p := range procs {
		if p.ppid == 1 {
			pids = append(pids, p.pid)
		}
	}
	return pids
}

func listProviderProcesses(cacheDir string) ([]providerProcess, error) {
	if runtime.GOOS == "windows" {
		return nil, nil
	}
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,args=").Output()
	if err != nil {
		return nil, fmt.Errorf("listing processes: %w", err)
	}
	return parseProviderProcesses(string(out), cacheDir), nil
}

const (
	reparentWait = 3 * time.Second
	termWait     = 2 * time.Second
	pollInterval = 50 * time.Millisecond
)

// reapOrphanedProviders terminates every Terraform provider under cacheDir
// whose parent has died, and returns the pids it terminated.
//
// The terraform-provider plugin starts each Terraform provider with a
// background context and never kills it, relying on a deferred close in its
// main that cannot run because the engine (and plugin.Provider.Close) stop
// the plugin with SIGKILL. The provider is reparented to pid 1 and lives until
// reboot (pulumi-terraform-bridge#3349). Until that is fixed upstream, the
// caller that closed the plugin cleans up after it.
//
// A provider whose parent is still alive belongs to a live caller and is left
// alone. One whose parent no longer exists but has not been reparented yet is
// waited for, bounded, so a reap right after Close does not miss it.
func reapOrphanedProviders(cacheDir string) ([]int, error) {
	deadline := time.Now().Add(reparentWait)
	var procs []providerProcess
	for {
		var err error
		procs, err = listProviderProcesses(cacheDir)
		if err != nil {
			return nil, err
		}
		if !anyAwaitingReparent(procs) || time.Now().After(deadline) {
			break
		}
		time.Sleep(pollInterval)
	}

	pids := orphans(procs)
	for _, pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	deadline = time.Now().Add(termWait)
	for _, pid := range pids {
		for processExists(pid) && time.Now().Before(deadline) {
			time.Sleep(pollInterval)
		}
		if processExists(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	return pids, nil
}

// anyAwaitingReparent reports whether some provider's parent has exited but
// the kernel has not yet moved the provider under pid 1.
func anyAwaitingReparent(procs []providerProcess) bool {
	for _, p := range procs {
		if p.ppid != 1 && !processExists(p.ppid) {
			return true
		}
	}
	return false
}

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
