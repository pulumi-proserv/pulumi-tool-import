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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newMockTFCServer(t *testing.T, org, workspace, workspaceID string, stateBody []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/.well-known/terraform.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"tfe.v2": "/api/v2/",
		})
	})

	mux.HandleFunc(fmt.Sprintf("/api/v2/organizations/%s/workspaces/%s", org, workspace),
		func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer test-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/vnd.api+json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"id":   workspaceID,
					"type": "workspaces",
				},
			})
		})

	mux.HandleFunc(fmt.Sprintf("/api/v2/workspaces/%s/current-state-version", workspaceID),
		func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer test-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/vnd.api+json")
			downloadURL := fmt.Sprintf("%s/state-download/%s", r.Header.Get("X-Test-Base-URL"), workspaceID)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"type": "state-versions",
					"attributes": map[string]interface{}{
						"hosted-state-download-url": downloadURL,
					},
				},
			})
		})

	mux.HandleFunc(fmt.Sprintf("/state-download/%s", workspaceID),
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write(stateBody)
		})

	return httptest.NewServer(mux)
}

func TestStatePull_Success(t *testing.T) {
	t.Parallel()

	fakeState := []byte(`{"version":4,"terraform_version":"1.5.0","serial":1,"lineage":"abc","outputs":{},"resources":[]}`)
	server := newMockTFCServer(t, "myorg", "myworkspace", "ws-abc123", fakeState)
	defer server.Close()

	client := &Client{
		Hostname: server.URL,
		Token:    "test-token",
		HTTP: &http.Client{
			Transport: &addBaseURLHeader{base: server.URL, rt: http.DefaultTransport},
		},
	}

	data, err := client.StatePull(context.Background(), "myorg", "myworkspace")
	require.NoError(t, err)
	assert.Equal(t, fakeState, data)
}

type addBaseURLHeader struct {
	base string
	rt   http.RoundTripper
}

func (a *addBaseURLHeader) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("X-Test-Base-URL", a.base)
	return a.rt.RoundTrip(req)
}

func TestStatePull_Unauthorized(t *testing.T) {
	t.Parallel()

	server := newMockTFCServer(t, "myorg", "myworkspace", "ws-abc123", nil)
	defer server.Close()

	client := &Client{
		Hostname: server.URL,
		Token:    "wrong-token",
	}

	_, err := client.StatePull(context.Background(), "myorg", "myworkspace")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication failed")
}

func TestListVariables_Paginated(t *testing.T) {
	t.Parallel()

	org, workspace, wsID := "myorg", "myworkspace", "ws-abc123"
	mux := http.NewServeMux()

	mux.HandleFunc("/.well-known/terraform.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"tfe.v2": "/api/v2/"})
	})

	mux.HandleFunc(fmt.Sprintf("/api/v2/organizations/%s/workspaces/%s", org, workspace),
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{"id": wsID, "type": "workspaces"},
			})
		})

	mux.HandleFunc(fmt.Sprintf("/api/v2/workspaces/%s/vars", wsID),
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			if r.URL.Query().Get("page[number]") == "2" {
				// Page 2: last page
				json.NewEncoder(w).Encode(map[string]interface{}{
					"data": []map[string]interface{}{
						{"attributes": map[string]interface{}{"key": "var_b", "value": "val_b", "category": "terraform"}},
					},
					"meta": map[string]interface{}{
						"pagination": map[string]interface{}{
							"current-page": 2, "next-page": nil, "total-pages": 2, "total-count": 3,
						},
					},
				})
				return
			}
			// Page 1
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": []map[string]interface{}{
					{"attributes": map[string]interface{}{"key": "var_a", "value": "val_a", "category": "terraform"}},
					{"attributes": map[string]interface{}{"key": "env_var", "value": "skip", "category": "env"}},
				},
				"meta": map[string]interface{}{
					"pagination": map[string]interface{}{
						"current-page": 1, "next-page": 2, "total-pages": 2, "total-count": 3,
					},
				},
			})
		})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := &Client{Hostname: server.URL, Token: "test-token"}
	vars, err := client.ListVariables(context.Background(), org, workspace)
	require.NoError(t, err)
	require.Len(t, vars, 2)
	assert.Equal(t, "var_a", vars[0].Key)
	assert.Equal(t, "var_b", vars[1].Key)
}

func TestListVariables_SinglePage(t *testing.T) {
	t.Parallel()

	org, workspace, wsID := "myorg", "myworkspace", "ws-abc123"
	mux := http.NewServeMux()

	mux.HandleFunc("/.well-known/terraform.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"tfe.v2": "/api/v2/"})
	})

	mux.HandleFunc(fmt.Sprintf("/api/v2/organizations/%s/workspaces/%s", org, workspace),
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{"id": wsID, "type": "workspaces"},
			})
		})

	mux.HandleFunc(fmt.Sprintf("/api/v2/workspaces/%s/vars", wsID),
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": []map[string]interface{}{
					{"attributes": map[string]interface{}{"key": "only_var", "value": "val", "category": "terraform"}},
				},
				"meta": map[string]interface{}{
					"pagination": map[string]interface{}{
						"current-page": 1, "next-page": nil, "total-pages": 1, "total-count": 1,
					},
				},
			})
		})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := &Client{Hostname: server.URL, Token: "test-token"}
	vars, err := client.ListVariables(context.Background(), org, workspace)
	require.NoError(t, err)
	require.Len(t, vars, 1)
	assert.Equal(t, "only_var", vars[0].Key)
}

func TestStatePull_WorkspaceNotFound(t *testing.T) {
	t.Parallel()

	server := newMockTFCServer(t, "myorg", "myworkspace", "ws-abc123", nil)
	defer server.Close()

	client := &Client{
		Hostname: server.URL,
		Token:    "test-token",
		HTTP: &http.Client{
			Transport: &addBaseURLHeader{base: server.URL, rt: http.DefaultTransport},
		},
	}

	_, err := client.StatePull(context.Background(), "myorg", "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// Response shapes as observed on tf.pulumi.com, 2026-09-15.
func newMockPulumiCloudServer(t *testing.T, org, workspace, workspaceID string, stateBody []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	mux.HandleFunc("/.well-known/terraform.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"modules.v1": server.URL + "/v1/modules/",
			"state.v2":   server.URL + "/api/v2",
			"tfe.v2":     server.URL + "/api/v2",
			"tfe.v2.1":   server.URL + "/api/v2",
			"tfe.v2.2":   server.URL + "/api/v2",
		})
	})

	authorized := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"code":401,"message":"Unauthorized: No credentials provided or are invalid."}`)
			return false
		}
		return true
	}

	mux.HandleFunc("/api/v2/organizations/"+org+"/workspaces/", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/api/v2/organizations/"+org+"/workspaces/")
		if name != workspace {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"code":404,"message":"Not Found: workspace '%s' not found"}`, name)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"id":         workspaceID,
				"type":       "workspaces",
				"attributes": map[string]interface{}{"name": workspace},
			},
		})
	})

	mux.HandleFunc("/api/v2/workspaces/"+workspaceID+"/current-state-version", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"type": "state-versions",
				"id":   "sv-1",
				"attributes": map[string]interface{}{
					"hosted-state-download-url":      server.URL + "/api/v2/workspaces/" + workspaceID + "/state-versions/sv-1",
					"hosted-json-state-download-url": "",
				},
			},
		})
	})

	mux.HandleFunc("/api/v2/workspaces/"+workspaceID+"/state-versions/sv-1", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(stateBody)
	})

	mux.HandleFunc("/api/v2/workspaces/"+workspaceID+"/vars", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "404 page not found", http.StatusNotFound)
	})

	return server
}

func TestStatePull_PulumiCloud_AbsoluteDiscoveryURLs(t *testing.T) {
	t.Parallel()

	fakeState := []byte(`{"version":4,"terraform_version":"1.11.14","serial":1,"lineage":"abc","outputs":{},"resources":[]}`)
	server := newMockPulumiCloudServer(t, "myorg", "myproject_dev", "0d6a4f6e-1111-4222-8333-444455556666", fakeState)

	client := &Client{Hostname: server.URL, Token: "test-token"}
	data, err := client.StatePull(context.Background(), "myorg", "myproject_dev")
	require.NoError(t, err)
	assert.Equal(t, fakeState, data)
}

func TestListVariables_PulumiCloud_RouteNotImplemented(t *testing.T) {
	t.Parallel()

	server := newMockPulumiCloudServer(t, "myorg", "myproject_dev", "ws-1", nil)

	client := &Client{Hostname: server.URL, Token: "test-token"}
	_, err := client.ListVariables(context.Background(), "myorg", "myproject_dev")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GET "+server.URL+"/api/v2/workspaces/ws-1/vars")
	assert.Contains(t, err.Error(), "404 Not Found")
}

func TestStatePull_WorkspaceNotFound_NamesRequestAndStatus(t *testing.T) {
	t.Parallel()

	server := newMockPulumiCloudServer(t, "myorg", "myproject_dev", "ws-1", nil)

	client := &Client{Hostname: server.URL, Token: "test-token"}
	_, err := client.StatePull(context.Background(), "myorg", "myproject-dev")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "workspace myorg/myproject-dev not found")
	assert.Contains(t, err.Error(), "GET "+server.URL+"/api/v2/organizations/myorg/workspaces/myproject-dev")
	assert.Contains(t, err.Error(), "404 Not Found")
	assert.Contains(t, err.Error(), "workspace 'myproject-dev' not found", "server message is surfaced")
}

func TestStatePull_Unauthorized_NamesRequestAndStatus(t *testing.T) {
	t.Parallel()

	server := newMockPulumiCloudServer(t, "myorg", "myproject_dev", "ws-1", nil)

	client := &Client{Hostname: server.URL, Token: "wrong-token"}
	_, err := client.StatePull(context.Background(), "myorg", "myproject_dev")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication failed")
	assert.Contains(t, err.Error(), "401 Unauthorized")
	assert.Contains(t, err.Error(), "GET "+server.URL+"/api/v2/organizations/myorg/workspaces/myproject_dev")
}

func TestWorkspaceNameHint(t *testing.T) {
	t.Parallel()

	assert.Contains(t, workspaceNameHint("tf.pulumi.com", "myproject/dev"), "<project>_<stack>")
	assert.Contains(t, workspaceNameHint("https://tf.pulumi.com", "myproject-dev"), "<project>_<stack>")
	assert.Empty(t, workspaceNameHint("tf.pulumi.com", "myproject_dev"), "already in the required form")
	assert.Empty(t, workspaceNameHint("app.terraform.io", "myproject/dev"))
}

// Response shapes as observed on app.terraform.io, 2026-09-15.
func newMockTerraformCloudServer(t *testing.T, org, workspace, workspaceID string, stateBody []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	mux.HandleFunc("/.well-known/terraform.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"modules.v1":   "/api/registry/v1/modules/",
			"providers.v1": "/api/registry/v1/providers/",
			"state.v2":     "/api/v2/",
			"tfe.v2":       "/api/v2/",
			"tfe.v2.1":     "/api/v2/",
			"tfe.v2.2":     "/api/v2/",
			"versions.v1":  "https://checkpoint-api.hashicorp.com/v1/versions/",
		})
	})

	jsonAPIError := func(w http.ResponseWriter, status int, title string) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"errors":[{"status":"%d","title":"%s"}]}`, status, title)
	}
	authorized := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			jsonAPIError(w, http.StatusUnauthorized, "unauthorized")
			return false
		}
		return true
	}

	mux.HandleFunc("/api/v2/organizations/"+org+"/workspaces/", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		if strings.TrimPrefix(r.URL.Path, "/api/v2/organizations/"+org+"/workspaces/") != workspace {
			jsonAPIError(w, http.StatusNotFound, "not found")
			return
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{"id": workspaceID, "type": "workspaces"},
		})
	})

	mux.HandleFunc("/api/v2/workspaces/"+workspaceID+"/current-state-version", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"type": "state-versions",
				"id":   "sv-1",
				"attributes": map[string]interface{}{
					"hosted-state-download-url":      server.URL + "/api/state-versions/sv-1/hosted_state",
					"hosted-json-state-download-url": server.URL + "/api/state-versions/sv-1/hosted_json_state",
					"serial":                         1,
					"status":                         "finalized",
				},
			},
		})
	})

	mux.HandleFunc("/api/state-versions/sv-1/hosted_state", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(stateBody)
	})

	mux.HandleFunc("/api/v2/workspaces/"+workspaceID+"/vars", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{{
				"id":   "var-1",
				"type": "vars",
				"attributes": map[string]interface{}{
					"key": "greeting", "value": "from-tfc", "sensitive": false, "category": "terraform", "hcl": false,
				},
			}},
			"meta": map[string]interface{}{"pagination": map[string]interface{}{
				"current-page": 1, "next-page": nil, "prev-page": nil, "total-pages": 1, "total-count": 1,
			}},
		})
	})

	return server
}

func TestStatePull_TerraformCloud(t *testing.T) {
	t.Parallel()

	fakeState := []byte(`{"version":4,"terraform_version":"1.14.3","serial":1,"lineage":"abc","outputs":{},"resources":[]}`)
	server := newMockTerraformCloudServer(t, "myorg", "myworkspace", "ws-abc123", fakeState)

	client := &Client{Hostname: server.URL, Token: "test-token"}
	data, err := client.StatePull(context.Background(), "myorg", "myworkspace")
	require.NoError(t, err)
	assert.Equal(t, fakeState, data)

	vars, err := client.ListVariables(context.Background(), "myorg", "myworkspace")
	require.NoError(t, err)
	assert.Equal(t, []WorkspaceVariable{{Key: "greeting", Value: "from-tfc", Category: "terraform"}}, vars)
}

func TestStatePull_TerraformCloud_JSONAPIErrorTitle(t *testing.T) {
	t.Parallel()

	server := newMockTerraformCloudServer(t, "myorg", "myworkspace", "ws-1", nil)

	client := &Client{Hostname: server.URL, Token: "test-token"}
	_, err := client.StatePull(context.Background(), "myorg", "nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "→ 404 Not Found: not found")
	assert.NotContains(t, err.Error(), "<project>_<stack>", "naming hint is Pulumi Cloud only")

	client = &Client{Hostname: server.URL, Token: "bad"}
	_, err = client.StatePull(context.Background(), "myorg", "myworkspace")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "→ 401 Unauthorized: unauthorized")
}

// Response shapes as observed on a Scalr account, 2026-09-17. The vars route
// reproduces the asymmetry listScalrVars exists for: filter[workspace]
// omits environment-scoped variables, filter[environment] returns them.
func newMockScalrServer(t *testing.T, environmentID, workspace, workspaceID string, stateBody []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	mux.HandleFunc("/.well-known/terraform.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"blob.v1":  "/api/tfe/v1/blobs/",
			"state.v2": "/api/tfe/v2/",
			"tfe.v2":   "/api/tfe/v2/",
			"tfe.v2.1": "/api/tfe/v2/",
			"iacp.v3":  "/api/iacp/v3/",
		})
	})

	jsonAPIError := func(w http.ResponseWriter, status int, title string) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"errors":[{"status":"%d","title":"%s"}]}`, status, title)
	}
	authorized := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			jsonAPIError(w, http.StatusUnauthorized, "Unauthorized")
			return false
		}
		return true
	}

	mux.HandleFunc("/api/tfe/v2/organizations/"+environmentID+"/workspaces/", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/api/tfe/v2/organizations/"+environmentID+"/workspaces/")
		if name != workspace {
			jsonAPIError(w, http.StatusNotFound, "Workspace with name '"+name+"' not found or user unauthorized.")
			return
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{"id": workspaceID, "type": "workspaces"},
		})
	})

	mux.HandleFunc("/api/tfe/v2/workspaces/"+workspaceID+"/current-state-version", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"type": "state-versions",
				"id":   "sv-1",
				"attributes": map[string]interface{}{
					"hosted-state-download-url": server.URL + "/api/tfe/v1/blobs/signed-blob-token",
					"serial":                    1,
					"status":                    "finalized",
				},
			},
		})
	})

	mux.HandleFunc("/api/tfe/v1/blobs/signed-blob-token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(stateBody)
	})

	scopedVar := func(id, key, value, wsID string) map[string]interface{} {
		var wsRel interface{}
		if wsID != "" {
			wsRel = map[string]interface{}{"data": map[string]interface{}{"id": wsID, "type": "workspaces"}}
		}
		return map[string]interface{}{
			"id":   id,
			"type": "vars",
			"attributes": map[string]interface{}{
				"key": key, "value": value, "category": "terraform", "hcl": false, "sensitive": false,
			},
			"relationships": map[string]interface{}{
				"environment": map[string]interface{}{"data": map[string]interface{}{"id": environmentID, "type": "environments"}},
				"workspace":   wsRel,
			},
		}
	}
	allVars := []map[string]interface{}{
		scopedVar("var-env-greeting", "greeting", "from-environment", ""),
		scopedVar("var-env-scoped", "env_scoped", "from-environment", ""),
		scopedVar("var-ws-greeting", "greeting", "from-workspace", workspaceID),
		scopedVar("var-sibling-greeting", "greeting", "from-sibling", "ws-sibling"),
		scopedVar("var-sibling-only", "sibling_only", "from-sibling", "ws-sibling"),
	}
	mux.HandleFunc("/api/iacp/v3/vars", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		q := r.URL.Query()
		var data []map[string]interface{}
		for _, v := range allVars {
			wsRel := v["relationships"].(map[string]interface{})["workspace"]
			wsID := ""
			if wsRel != nil {
				wsID = wsRel.(map[string]interface{})["data"].(map[string]interface{})["id"].(string)
			}
			if f := q.Get("filter[workspace]"); f != "" && wsID != f {
				continue
			}
			if f := q.Get("filter[environment]"); f != "" && f != environmentID {
				continue
			}
			data = append(data, v)
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": data,
			"meta": map[string]interface{}{"pagination": map[string]interface{}{
				"current-page": 1, "next-page": nil, "prev-page": nil, "total-pages": 1, "total-count": len(data),
			}},
		})
	})

	return server
}

func TestStatePull_Scalr(t *testing.T) {
	t.Parallel()

	fakeState := []byte(`{"version":4,"terraform_version":"1.5.7","serial":1,"lineage":"abc","outputs":{},"resources":[]}`)
	server := newMockScalrServer(t, "env-abc", "myworkspace", "ws-abc", fakeState)

	client := &Client{Hostname: server.URL, Token: "test-token"}
	data, err := client.StatePull(context.Background(), "env-abc", "myworkspace")
	require.NoError(t, err)
	assert.Equal(t, fakeState, data)

	_, err = client.StatePull(context.Background(), "env-abc", "nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "→ 404 Not Found: Workspace with name 'nope' not found or user unauthorized.")
}

func TestListVariables_Scalr_IncludesEnvironmentScope(t *testing.T) {
	t.Parallel()

	server := newMockScalrServer(t, "env-abc", "myworkspace", "ws-abc", nil)

	client := &Client{Hostname: server.URL, Token: "test-token"}
	vars, err := client.ListVariables(context.Background(), "env-abc", "myworkspace")
	require.NoError(t, err)
	assert.Equal(t, []WorkspaceVariable{
		{Key: "greeting", Value: "from-workspace", Category: "terraform"},
		{Key: "env_scoped", Value: "from-environment", Category: "terraform"},
	}, vars, "workspace-scoped wins the key clash, environment-scoped is included, the sibling workspace's are not")
}
