// Copyright 2026, Pulumi Corporation.
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
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
	"github.com/stretchr/testify/require"
)

// newSingleWorkspaceFixture creates and seeds a disposable remote workspace.
func newSingleWorkspaceFixture(t *testing.T) string {
	t.Helper()
	org := os.Getenv("PULUMI_TEST_ORG")
	if org == "" {
		t.Skip("set PULUMI_TEST_ORG to run disposable remote-workspace tests")
	}
	token := os.Getenv("PULUMI_ACCESS_TOKEN")
	require.NotEmpty(t, token, "PULUMI_TEST_ORG requires PULUMI_ACCESS_TOKEN")
	host := os.Getenv("PULUMI_TEST_TF_HOST")
	if host == "" {
		host = "tf.pulumi.com"
	}
	require.NotContains(t, host, "/", "PULUMI_TEST_TF_HOST must be a hostname, not a URL")
	workspace := "tool-import-test-" + strings.ToLower(rand.Text()) + "_fixture"
	dir := copyTestdata(t, "testdata/tf-single-workspace")
	configPath := filepath.Join(dir, "main.tf")
	config, err := os.ReadFile(configPath)
	require.NoError(t, err)
	config = []byte(strings.NewReplacer(
		`"__TEST_HOST__"`, strconv.Quote(host),
		`"__TEST_ORG__"`, strconv.Quote(org),
		`"__TEST_WORKSPACE__"`, strconv.Quote(workspace),
	).Replace(string(config)))
	require.NoError(t, os.WriteFile(configPath, config, 0o600))

	// Use an isolated credentials file for arbitrary backend hostnames.
	credentials, err := json.Marshal(map[string]interface{}{
		"credentials": map[string]interface{}{host: map[string]string{"token": token}},
	})
	require.NoError(t, err)
	credentialsPath := filepath.Join(t.TempDir(), "credentials.tfrc.json")
	require.NoError(t, os.WriteFile(credentialsPath, credentials, 0o600))
	t.Setenv("TF_CLI_CONFIG_FILE", credentialsPath)
	// Environment credentials take precedence over the credentials file.
	t.Setenv("TF_TOKEN_"+strings.ReplaceAll(strings.ReplaceAll(host, ".", "_"), "-", "__"), token)

	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	baseURL := "https://" + host + "/api/v2"
	workspacePath := "/organizations/" + url.PathEscape(org) + "/workspaces/" + url.PathEscape(workspace)
	status, _, err := fixtureWorkspaceRequest(client, token, http.MethodGet, baseURL+workspacePath, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status, "expected a fresh workspace name in %s on %s", org, host)
	t.Logf("temporary workspace: %s/%s on %s", org, workspace, host)
	// Register before creation so cleanup also runs after initialization or seeding fails.
	t.Cleanup(func() {
		status, body, err := fixtureWorkspaceRequest(client, token, http.MethodGet, baseURL+workspacePath, nil)
		if err == nil && status == http.StatusNotFound {
			return
		}
		if err != nil || status != http.StatusOK {
			t.Errorf("cleanup lookup for %s/%s failed: HTTP %d, %v", org, workspace, status, err)
			return
		}
		var response struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &response); err != nil || response.Data.ID == "" {
			t.Errorf("cleanup lookup for %s/%s returned no workspace ID", org, workspace)
			return
		}
		status, _, err = fixtureWorkspaceRequest(client, token, http.MethodDelete,
			baseURL+"/workspaces/"+url.PathEscape(response.Data.ID), nil)
		if err != nil || (status != http.StatusNoContent && status != http.StatusNotFound) {
			t.Errorf("cleanup delete for %s/%s failed: HTTP %d, %v", org, workspace, status, err)
			return
		}
		t.Logf("deleted temporary workspace: %s/%s", org, workspace)
	})

	payload, err := json.Marshal(map[string]interface{}{"data": map[string]interface{}{
		"type":       "workspaces",
		"attributes": map[string]interface{}{"name": workspace, "execution-mode": "local"},
	}})
	require.NoError(t, err)
	status, _, err = fixtureWorkspaceRequest(client, token, http.MethodPost,
		baseURL+"/organizations/"+url.PathEscape(org)+"/workspaces", payload)
	require.NoError(t, err)
	require.Contains(t, []int{http.StatusOK, http.StatusCreated}, status, "creating workspace in %s on %s", org, host)

	tofu, err := tofuNew(dir)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	require.NoError(t, tofu.Init(ctx))
	statePath, err := filepath.Abs("testdata/tf-project/terraform.tfstate")
	require.NoError(t, err)
	require.NoError(t, tofu.StatePush(ctx, statePath, tfexec.Lock(true), tfexec.LockTimeout("30s")))
	return dir
}

func fixtureWorkspaceRequest(client *http.Client, token, method, endpoint string, body []byte) (int, []byte, error) {
	request, err := http.NewRequest(method, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/vnd.api+json")
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return response.StatusCode, nil, fmt.Errorf("reading workspace response: %w", err)
	}
	return response.StatusCode, data, nil
}
