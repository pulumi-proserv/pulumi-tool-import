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

package tfc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	Hostname string
	Token    string
	HTTP     *http.Client
}

const (
	ScopeWorkspace   = "workspace"
	ScopeEnvironment = "environment"
)

// WorkspaceVariable represents a single variable from the TFC/Scalr workspace.
type WorkspaceVariable struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	Category  string `json:"category"` // "terraform" or "env"
	HCL       bool   `json:"hcl"`
	Sensitive bool   `json:"sensitive"`
	Scope     string `json:"scope"`
}

func (c *Client) ListVariables(ctx context.Context, org, workspace string) ([]WorkspaceVariable, error) {
	httpClient := c.httpClient()
	baseURL := c.baseURL()

	disc, err := c.discoverAll(ctx, httpClient, baseURL)
	if err != nil {
		return nil, err
	}

	wsID, err := c.getWorkspaceID(ctx, httpClient, disc.apiPrefix, org, workspace)
	if err != nil {
		return nil, err
	}

	if disc.iacpPrefix != "" {
		return c.listScalrVars(ctx, httpClient, disc.iacpPrefix, org, wsID)
	}

	varsURL := fmt.Sprintf("%s/workspaces/%s/vars", disc.apiPrefix, wsID)
	return c.listVarsPaginated(ctx, httpClient, varsURL)
}

func (c *Client) listScalrVars(ctx context.Context, httpClient *http.Client, iacpPrefix, environmentID, wsID string) ([]WorkspaceVariable, error) {
	if !strings.HasPrefix(environmentID, "env-") {
		return nil, fmt.Errorf("on Scalr the organization must be the environment ID (env-…), got %q", environmentID)
	}
	varsURL := fmt.Sprintf("%s/vars?filter%%5Benvironment%%5D=%s", iacpPrefix, environmentID)
	entries, err := c.fetchVarsPages(ctx, httpClient, varsURL)
	if err != nil {
		return nil, err
	}
	var ordered []WorkspaceVariable
	for _, e := range entries {
		if e.scopeEnvironmentID == environmentID && e.scopeWorkspaceID == wsID {
			e.Scope = ScopeWorkspace
			ordered = append(ordered, e.WorkspaceVariable)
		}
	}
	for _, e := range entries {
		if e.scopeEnvironmentID == environmentID && e.scopeWorkspaceID == "" {
			e.Scope = ScopeEnvironment
			ordered = append(ordered, e.WorkspaceVariable)
		}
	}
	return dedupeByKey(ordered), nil
}

func (c *Client) listVarsPaginated(ctx context.Context, httpClient *http.Client, baseVarsURL string) ([]WorkspaceVariable, error) {
	entries, err := c.fetchVarsPages(ctx, httpClient, baseVarsURL)
	if err != nil {
		return nil, err
	}
	vars := make([]WorkspaceVariable, 0, len(entries))
	for _, e := range entries {
		e.Scope = ScopeWorkspace
		vars = append(vars, e.WorkspaceVariable)
	}
	return dedupeByKey(vars), nil
}

func dedupeByKey(vars []WorkspaceVariable) []WorkspaceVariable {
	seen := make(map[string]bool, len(vars))
	var out []WorkspaceVariable
	for _, v := range vars {
		if seen[v.Key] {
			continue
		}
		seen[v.Key] = true
		out = append(out, v)
	}
	return out
}

type varEntry struct {
	WorkspaceVariable
	scopeWorkspaceID   string
	scopeEnvironmentID string
}

func (c *Client) fetchVarsPages(ctx context.Context, httpClient *http.Client, baseVarsURL string) ([]varEntry, error) {
	var entries []varEntry
	for pageNum := 1; ; pageNum++ {
		sep := "?"
		if strings.Contains(baseVarsURL, "?") {
			sep = "&"
		}
		url := fmt.Sprintf("%s%spage%%5Bnumber%%5D=%d&page%%5Bsize%%5D=100", baseVarsURL, sep, pageNum)

		var page varsPage
		_, err := c.doJSON(ctx, httpClient, url, &page)
		if err != nil {
			return nil, fmt.Errorf("listing workspace variables: %w", err)
		}

		for _, d := range page.Data {
			if d.Attributes.Category != "terraform" {
				continue
			}
			entries = append(entries, varEntry{
				WorkspaceVariable: WorkspaceVariable{
					Key:       d.Attributes.Key,
					Value:     d.Attributes.Value,
					Category:  d.Attributes.Category,
					HCL:       d.Attributes.HCL,
					Sensitive: d.Attributes.Sensitive,
				},
				scopeWorkspaceID:   d.Relationships.Workspace.ID(),
				scopeEnvironmentID: d.Relationships.Environment.ID(),
			})
		}

		if page.Meta.Pagination.NextPage == nil || pageNum >= page.Meta.Pagination.TotalPages {
			break
		}
	}
	return entries, nil
}

type relationship struct {
	Data *struct {
		ID string `json:"id"`
	} `json:"data"`
}

func (r relationship) ID() string {
	if r.Data == nil {
		return ""
	}
	return r.Data.ID
}

// varsPage represents one page of the JSON:API list-variables response.
type varsPage struct {
	Data []struct {
		Attributes struct {
			Key       string `json:"key"`
			Value     string `json:"value"`
			Category  string `json:"category"`
			HCL       bool   `json:"hcl"`
			Sensitive bool   `json:"sensitive"`
		} `json:"attributes"`
		Relationships struct {
			Workspace   relationship `json:"workspace"`
			Environment relationship `json:"environment"`
		} `json:"relationships"`
	} `json:"data"`
	Meta struct {
		Pagination struct {
			CurrentPage int  `json:"current-page"`
			NextPage    *int `json:"next-page"`
			TotalPages  int  `json:"total-pages"`
			TotalCount  int  `json:"total-count"`
		} `json:"pagination"`
	} `json:"meta"`
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			ForceAttemptHTTP2: true,
		},
	}
}

func (c *Client) StatePull(ctx context.Context, org, workspace string) ([]byte, error) {
	httpClient := c.httpClient()

	baseURL := c.baseURL()

	apiPrefix, err := c.discover(ctx, httpClient, baseURL)
	if err != nil {
		return nil, err
	}

	wsID, err := c.getWorkspaceID(ctx, httpClient, apiPrefix, org, workspace)
	if err != nil {
		return nil, err
	}

	downloadURL, err := c.getStateDownloadURL(ctx, httpClient, apiPrefix, wsID)
	if err != nil {
		return nil, err
	}

	return c.downloadState(ctx, httpClient, downloadURL)
}

func (c *Client) baseURL() string {
	h := c.Hostname
	if strings.HasPrefix(h, "http://") || strings.HasPrefix(h, "https://") {
		return strings.TrimRight(h, "/")
	}
	return "https://" + strings.TrimRight(h, "/")
}

// discoveryResult holds the resolved API prefixes from the well-known document.
type discoveryResult struct {
	// apiPrefix is the primary TFE-compatible API prefix (tfe.v2 or state.v2).
	apiPrefix string
	// iacpPrefix is the Scalr-native IACP v3 API prefix, if available.
	// When present, it should be preferred for variable listing as it includes
	// environment-scope variables that the TFE-compatible endpoint omits.
	iacpPrefix string
}

func (c *Client) discover(ctx context.Context, httpClient *http.Client, baseURL string) (string, error) {
	result, err := c.discoverAll(ctx, httpClient, baseURL)
	if err != nil {
		return "", err
	}
	return result.apiPrefix, nil
}

func (c *Client) discoverAll(ctx context.Context, httpClient *http.Client, baseURL string) (*discoveryResult, error) {
	url := baseURL + "/.well-known/terraform.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating discovery request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("service discovery failed for %s: %w", c.Hostname, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("service discovery failed: %s returned status %d", c.Hostname, resp.StatusCode)
	}

	var discovery map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&discovery); err != nil {
		return nil, fmt.Errorf("service discovery failed: %s did not return a valid /.well-known/terraform.json", c.Hostname)
	}

	result := &discoveryResult{}

	for _, key := range []string{"tfe.v2", "state.v2"} {
		if prefix, ok := discovery[key]; ok {
			result.apiPrefix, err = resolveServicePrefix(baseURL, prefix)
			if err != nil {
				return nil, fmt.Errorf("service discovery failed: %w", err)
			}
			break
		}
	}

	if result.apiPrefix == "" {
		return nil, fmt.Errorf("service discovery failed: %s did not return a valid /.well-known/terraform.json", c.Hostname)
	}

	// Check for Scalr-native IACP v3 API.
	if prefix, ok := discovery["iacp.v3"]; ok {
		result.iacpPrefix, err = resolveServicePrefix(baseURL, prefix)
		if err != nil {
			return nil, fmt.Errorf("service discovery failed: %w", err)
		}
	}

	return result, nil
}

func resolveServicePrefix(baseURL, prefix string) (string, error) {
	prefix = strings.TrimRight(prefix, "/")
	if strings.HasPrefix(prefix, "http://") || strings.HasPrefix(prefix, "https://") {
		base, err := url.Parse(baseURL)
		if err != nil {
			return "", err
		}
		p, err := url.Parse(prefix)
		if err != nil {
			return "", fmt.Errorf("invalid service prefix %q: %w", prefix, err)
		}
		if p.Host != base.Host {
			return "", fmt.Errorf("%s advertises its API on a different host, %s; refusing to send the token there", base.Host, p.Host)
		}
		return prefix, nil
	}
	return baseURL + "/" + strings.TrimLeft(prefix, "/"), nil
}

type HTTPError struct {
	Method     string
	URL        string
	StatusCode int
	Status     string
	Message    string
}

func (e *HTTPError) Error() string {
	msg := fmt.Sprintf("%s %s → %s", e.Method, e.URL, e.Status)
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

const maxErrorBodyBytes = 4096

func newHTTPError(req *http.Request, resp *http.Response) *HTTPError {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	return &HTTPError{
		Method:     req.Method,
		URL:        req.URL.String(),
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Message:    serverMessage(body),
	}
}

func serverMessage(body []byte) string {
	var pulumiShape struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &pulumiShape); err == nil && pulumiShape.Message != "" {
		return pulumiShape.Message
	}
	var jsonAPIShape struct {
		Errors []struct {
			Title  string `json:"title"`
			Detail string `json:"detail"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &jsonAPIShape); err == nil && len(jsonAPIShape.Errors) > 0 {
		e := jsonAPIShape.Errors[0]
		if e.Detail != "" {
			return e.Detail
		}
		return e.Title
	}
	text := strings.TrimSpace(string(body))
	if text == "" || strings.HasPrefix(text, "<") {
		return ""
	}
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	return text
}

func workspaceNameHint(hostname, workspace string) string {
	if !strings.Contains(hostname, "tf.pulumi.com") {
		return ""
	}
	if strings.Contains(workspace, "_") && !strings.Contains(workspace, "/") {
		return ""
	}
	return "Pulumi Cloud workspace names take the form <project>_<stack> (the cloud { workspaces { name } } value), not <project>/<stack>"
}

func (c *Client) doJSON(ctx context.Context, httpClient *http.Client, url string, target interface{}) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/vnd.api+json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", req.Method, url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return resp, newHTTPError(req, resp)
	}

	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return resp, fmt.Errorf("%s %s → %s: response is not valid JSON", req.Method, url, resp.Status)
	}
	return resp, nil
}

func (c *Client) getWorkspaceID(ctx context.Context, httpClient *http.Client, apiPrefix, org, workspace string) (string, error) {
	url := fmt.Sprintf("%s/organizations/%s/workspaces/%s", apiPrefix, org, workspace)

	var result struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}

	resp, err := c.doJSON(ctx, httpClient, url, &result)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return "", fmt.Errorf("authentication failed for %s (%w)", c.Hostname, err)
		}
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			hint := workspaceNameHint(c.Hostname, workspace)
			if hint != "" {
				hint = "; " + hint
			}
			return "", fmt.Errorf("workspace %s/%s not found on %s (%w)%s", org, workspace, c.Hostname, err, hint)
		}
		return "", fmt.Errorf("looking up workspace %s/%s: %w", org, workspace, err)
	}

	if result.Data.ID == "" {
		return "", fmt.Errorf("workspace %s/%s returned empty ID", org, workspace)
	}

	return result.Data.ID, nil
}

func (c *Client) getStateDownloadURL(ctx context.Context, httpClient *http.Client, apiPrefix, workspaceID string) (string, error) {
	url := fmt.Sprintf("%s/workspaces/%s/current-state-version", apiPrefix, workspaceID)

	var result struct {
		Data struct {
			Attributes struct {
				HostedStateDownloadURL string `json:"hosted-state-download-url"`
			} `json:"attributes"`
		} `json:"data"`
	}

	resp, err := c.doJSON(ctx, httpClient, url, &result)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return "", fmt.Errorf("no state found for workspace %s (%w)", workspaceID, err)
		}
		return "", fmt.Errorf("getting state version for workspace %s: %w", workspaceID, err)
	}

	downloadURL := result.Data.Attributes.HostedStateDownloadURL
	if downloadURL == "" {
		return "", fmt.Errorf("no state download URL for workspace %s", workspaceID)
	}

	return downloadURL, nil
}

func (c *Client) downloadState(ctx context.Context, httpClient *http.Client, downloadURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating state download request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading state: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		httpErr := newHTTPError(req, resp)
		httpErr.URL = req.URL.Scheme + "://" + req.URL.Host + "/…"
		return nil, fmt.Errorf("downloading state: %w", httpErr)
	}

	return io.ReadAll(resp.Body)
}
