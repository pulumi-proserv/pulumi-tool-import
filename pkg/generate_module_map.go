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

package pkg

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	tfjson "github.com/hashicorp/terraform-json"
	"github.com/pulumi-proserv/pulumi-tool-import/pkg/importsupport"
	"github.com/pulumi-proserv/pulumi-tool-import/pkg/providermap"
	tfcpkg "github.com/pulumi-proserv/pulumi-tool-import/pkg/tfc"
	tofuutil "github.com/pulumi-proserv/pulumi-tool-import/pkg/tofu"
	"github.com/pulumi/opentofu/addrs"
	"github.com/pulumi/opentofu/lang/marks"
	"github.com/pulumi/opentofu/states"
	"github.com/pulumi/opentofu/tofu"
	"github.com/zclconf/go-cty/cty"
)

// RemoteStateOptions configures pulling state from a TFC-compatible API.
type RemoteStateOptions struct {
	Hostname     string
	Organization string
	Workspace    string
	Token        string
}

// GenerateModuleMap is the top-level orchestrator for the module-map subcommand.
// It loads Terraform configuration and state, resolves Pulumi providers, builds a
// ModuleMap, and writes it to outputPath.
// SecretsOptions configures the automatic secret extraction from state.
type SecretsOptions struct {
	ProjectDir  string
	ProjectName string
	Runtime     string
	Skip        bool
}

func GenerateModuleMap(ctx context.Context, tfDir, stateFilePath, outputPath, stackName, projectName string, remote *RemoteStateOptions, secrets *SecretsOptions, checkImportSupport bool) error {
	if stateFilePath != "" && remote != nil {
		return fmt.Errorf("stateFilePath and remote are mutually exclusive")
	}

	// Step 1: Load Terraform/OpenTofu configuration.
	fmt.Fprintf(os.Stderr, "[1/7] Loading Terraform configuration from %s...\n", tfDir)
	config, err := LoadConfig(tfDir)
	if err != nil {
		return fmt.Errorf("loading config from %s: %w", tfDir, err)
	}

	// Step 2: Load state bytes.
	var stateData []byte
	if remote != nil {
		fmt.Fprintf(os.Stderr, "[2/7] Pulling state from %s (%s/%s)...\n", remote.Hostname, remote.Organization, remote.Workspace)
		tfcClient := &tfcpkg.Client{
			Hostname: remote.Hostname,
			Token:    remote.Token,
		}
		stateData, err = tfcClient.StatePull(ctx, remote.Organization, remote.Workspace)
		if err != nil {
			return fmt.Errorf("pulling remote state: %w", err)
		}
	} else {
		fmt.Fprintf(os.Stderr, "[2/7] Reading state from %s...\n", stateFilePath)
		stateData, err = os.ReadFile(stateFilePath)
		if err != nil {
			return fmt.Errorf("reading state file %s: %w", stateFilePath, err)
		}
	}

	// Step 3: Detect format and parse.
	fmt.Fprintf(os.Stderr, "[3/7] Detecting state format...\n")
	format, err := DetectStateFormatBytes(stateData)
	if err != nil {
		return fmt.Errorf("detecting state format: %w", err)
	}

	var rawState *states.State
	var tofuCtx *tofu.Context
	var pulumiProviders map[providermap.TerraformProviderName]*ProviderWithMetadata

	switch format {
	case StateFormatRaw:
		fmt.Fprintf(os.Stderr, "[4/7] Parsing raw state and evaluating expressions...\n")
		rawState, err = LoadRawStateBytes(stateData)
		if err != nil {
			return fmt.Errorf("loading raw state: %w", err)
		}

		var cleanup func()
		tofuCtx, cleanup, err = Evaluate(config, rawState, tfDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not create evaluation context: %v\n", err)
			fmt.Fprintf(os.Stderr, "Continuing without evaluated values.\n")
			tofuCtx = nil
		}
		if cleanup != nil {
			defer cleanup()
		}

		fmt.Fprintf(os.Stderr, "[4b/7] Resolving Pulumi providers...\n")
		tfProviders := getTerraformProvidersForRawState(rawState)
		pulumiProviders, err = PulumiProvidersForTerraformProviders(tfProviders, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not resolve Pulumi providers: %v\n", err)
			fmt.Fprintf(os.Stderr, "Continuing without Pulumi URNs (will use raw Terraform addresses).\n")
			pulumiProviders = nil
		}

	case StateFormatTofuShowJSON:
		var tfjsonState tfjson.State
		if err := json.Unmarshal(stateData, &tfjsonState); err != nil {
			return fmt.Errorf("parsing tofu show JSON state: %w", err)
		}

		rawState = rawStateFromTfjson(&tfjsonState)

		pulumiProviders, err = GetPulumiProvidersForTerraformState(&tfjsonState, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not resolve Pulumi providers: %v\n", err)
			fmt.Fprintf(os.Stderr, "Continuing without Pulumi URNs (will use raw Terraform addresses).\n")
			pulumiProviders = nil
		}
	}

	// Step 5: Build root variable values for expression evaluation.
	var remoteVars []tfcpkg.WorkspaceVariable
	if remote != nil && tofuCtx != nil {
		fmt.Fprintf(os.Stderr, "[5/7] Fetching workspace variables from %s...\n", remote.Hostname)
		tfcClient := &tfcpkg.Client{
			Hostname: remote.Hostname,
			Token:    remote.Token,
		}
		remoteVars, err = tfcClient.ListVariables(ctx, remote.Organization, remote.Workspace)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not fetch workspace variables: %v\n", err)
			fmt.Fprintf(os.Stderr, "Continuing with local tfvars only.\n")
		} else {
			fmt.Fprintf(os.Stderr, "  Fetched %d workspace variables\n", len(remoteVars))
		}
	}

	var evalScopes *EvalScopes
	if tofuCtx != nil && config != nil {
		rootVars := BuildRootVariables(config, tfDir, remoteVars)
		fmt.Fprintf(os.Stderr, "  Built %d root variable values\n", len(rootVars))

		fmt.Fprintf(os.Stderr, "[5b/7] Building eval scopes (one-time graph walk)...\n")
		evalScopes, _ = BuildEvalScopes(ctx, tofuCtx, config, rawState, rootVars)
	}

	// Step 5c: Prepare the import-support check. Terraform resource types that
	// declare no importer cannot be imported at all, and an import file that
	// includes them fails mid-run; flagging them here lets resolve leave them
	// out. The provider itself is the only source for this — see
	// pkg/importsupport.
	var importChecker ImportSupportChecker
	if checkImportSupport {
		fmt.Fprintf(os.Stderr, "[5c/7] Checking which resource types support import...\n")
		lockedVersions, lockErr := tofuutil.LockedProviderVersions(tfDir)
		if lockErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not read provider lock file: %v\n", lockErr)
			lockedVersions = nil
		}
		prober := importsupport.NewProber(lockedVersions)
		defer prober.Close(ctx)
		importChecker = prober
	}

	// Step 6: Build the module map.
	fmt.Fprintf(os.Stderr, "[6/7] Building module map...\n")
	mm, err := BuildModuleMap(ctx, config, evalScopes, rawState, pulumiProviders, stackName, projectName, importChecker)
	if err != nil {
		return fmt.Errorf("building module map: %w", err)
	}

	// Step 7: Write the module map to disk.
	fmt.Fprintf(os.Stderr, "[7/7] Writing module map to %s...\n", outputPath)
	if err := WriteModuleMap(mm, outputPath); err != nil {
		return fmt.Errorf("writing module map: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Module map written to %s\n", outputPath)

	// Step 8: Set sensitive attributes and workspace variables as Pulumi config secrets.
	if secrets != nil && !secrets.Skip {
		fmt.Fprintf(os.Stderr, "[8] Discovering sensitive attributes...\n")
		sensitiveSecrets, err := DiscoverSensitiveSecrets(rawState, secrets.ProjectName)
		if err != nil {
			return fmt.Errorf("discovering secrets: %w", err)
		}
		fmt.Fprintf(os.Stderr, "  Found %d sensitive attributes in state\n", len(sensitiveSecrets))

		// Set tfvars values as plain config. These come from terraform.tfvars
		// and *.auto.tfvars files committed to the repo.
		// Workspace variables (below) take priority and may override these.
		if config != nil {
			tfvarsEntries := collectTFVarsConfig(config, tfDir)
			if len(tfvarsEntries) > 0 {
				sensitiveSecrets = append(sensitiveSecrets, tfvarsEntries...)
				fmt.Fprintf(os.Stderr, "  Added %d tfvars values as plain config\n", len(tfvarsEntries))
			}
		}

		// Set workspace variables as config. Variables marked sensitive in
		// the backend are set as secrets; non-sensitive ones as plain config.
		// Some backends (e.g. Scalr) redact sensitive values in the API
		// response — skip those and warn so the user can set them manually.
		if remoteVars != nil {
			var secretCount, plainCount, skipped int
			for _, rv := range remoteVars {
				if rv.Value == "" {
					skipped++
					fmt.Fprintf(os.Stderr, "  WARNING: workspace var %q is redacted (sensitive in backend), set manually with --secret\n", rv.Key)
					continue
				}
				sensitiveSecrets = append(sensitiveSecrets, ConfigEntry{
					ConfigKey: rv.Key,
					Value:     rv.Value,
					Secret:    rv.Sensitive,
				})
				if rv.Sensitive {
					secretCount++
				} else {
					plainCount++
				}
			}
			fmt.Fprintf(os.Stderr, "  Workspace vars: %d as secrets, %d as plain config (%d skipped, redacted)\n",
				secretCount, plainCount, skipped)
		}

		if len(sensitiveSecrets) > 0 {
			fmt.Fprintf(os.Stderr, "  Setting %d total secrets on stack...\n", len(sensitiveSecrets))
			if err := SetSecretsFromState(sensitiveSecrets, secrets.ProjectDir, secrets.ProjectName, stackName, secrets.Runtime); err != nil {
				return fmt.Errorf("setting secrets: %w", err)
			}
		}
	}

	return nil
}

// rawStateFromTfjson builds a synthetic *states.State from a tfjson.State.
// This allows the StateFormatTofuShowJSON path to reuse the same BuildModuleMap
// code that works with raw state.
func rawStateFromTfjson(tfjsonState *tfjson.State) *states.State {
	state := states.NewState()

	tofuutil.VisitResources(tfjsonState, func(r *tfjson.StateResource) error {
		// Parse module address from the resource address.
		segments := parseModuleSegments(r.Address)
		moduleAddr := addrs.RootModuleInstance
		for _, seg := range segments {
			if seg.key == "" {
				moduleAddr = moduleAddr.Child(seg.name, addrs.NoKey)
			} else if _, err := fmt.Sscanf(seg.key, "%d", new(int)); err == nil {
				var idx int
				fmt.Sscanf(seg.key, "%d", &idx)
				moduleAddr = moduleAddr.Child(seg.name, addrs.IntKey(idx))
			} else {
				moduleAddr = moduleAddr.Child(seg.name, addrs.StringKey(seg.key))
			}
		}

		// Parse provider.
		provider, _ := addrs.ParseProviderSourceString(r.ProviderName)
		providerConfig := addrs.AbsProviderConfig{
			Provider: provider,
		}

		// Build resource address.
		mode := addrs.ManagedResourceMode
		if r.Mode == tfjson.DataResourceMode {
			mode = addrs.DataResourceMode
		}
		resAddr := addrs.Resource{
			Mode: mode,
			Type: r.Type,
			Name: r.Name,
		}

		// Serialize attribute values to JSON.
		attrsJSON, _ := json.Marshal(r.AttributeValues)

		module := state.EnsureModule(moduleAddr)
		module.SetResourceProvider(resAddr, providerConfig)
		module.SetResourceInstanceCurrent(
			addrs.ResourceInstance{Resource: resAddr, Key: addrs.NoKey},
			&states.ResourceInstanceObjectSrc{
				AttrsJSON: attrsJSON,
				// Without this the whole sensitivity model is inert for this
				// state format, and the format is selected AUTOMATICALLY — a
				// "format_version" key is all DetectStateFormatBytes needs, so
				// an operator passing "tofu show -json" output gets no
				// redaction and no flag ever mentions it. redactSensitivePaths
				// and DiscoverSensitiveSecrets both read AttrSensitivePaths, so
				// leaving it empty meant plaintext secrets in the digest, and
				// on this branch that flows on into PulumiOutputs and into the
				// injected resource's state outputs.
				AttrSensitivePaths: sensitivePathsFromTfjson(r.SensitiveValues),
			},
			providerConfig,
			nil,
		)

		return nil
	}, &tofuutil.VisitOptions{IncludeDataSources: true})

	return state
}

// sensitivePathsFromTfjson converts a "tofu show -json" resource's
// "sensitive_values" document into the cty paths the rest of this package
// reads.
//
// The document mirrors the attribute structure with `true` at every sensitive
// leaf — {"password": true, "user": [{"password": true}]} — so a walk of it
// yields exactly the paths OpenTofu would have recorded in raw state. Nested
// paths are emitted as well as top-level ones; redactSensitivePaths handles
// both, and a nested secret with no stack config key fails loudly at injection
// rather than leaking.
func sensitivePathsFromTfjson(raw json.RawMessage) []cty.PathValueMarks {
	if len(raw) == 0 {
		return nil
	}
	var doc interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	var paths []cty.PathValueMarks
	walkSensitiveValues(doc, nil, &paths)
	return paths
}

// walkSensitiveValues appends a path for every `true` leaf in a
// "sensitive_values" document.
func walkSensitiveValues(node interface{}, prefix cty.Path, out *[]cty.PathValueMarks) {
	switch v := node.(type) {
	case bool:
		if !v || len(prefix) == 0 {
			return
		}
		// The path is copied: cty.Path is a slice, and append below reuses its
		// backing array, so every recorded path would otherwise alias the last
		// one written.
		p := make(cty.Path, len(prefix))
		copy(p, prefix)
		*out = append(*out, cty.PathValueMarks{
			Path:  p,
			Marks: cty.NewValueMarks(marks.Sensitive),
		})
	case map[string]interface{}:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			walkSensitiveValues(v[k], append(prefix, cty.GetAttrStep{Name: k}), out)
		}
	case []interface{}:
		for i, elem := range v {
			walkSensitiveValues(elem, append(prefix, cty.IndexStep{
				Key: cty.NumberIntVal(int64(i)),
			}), out)
		}
	}
}
