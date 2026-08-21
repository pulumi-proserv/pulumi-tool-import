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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/pulumi-proserv/pulumi-tool-import/pkg/bridge"
	"github.com/pulumi-proserv/pulumi-tool-import/pkg/importsupport"
	"github.com/pulumi-proserv/pulumi-tool-import/pkg/providermap"
	"github.com/pulumi-proserv/pulumi-tool-import/pkg/tfprovider"
	"github.com/pulumi/opentofu/addrs"
	"github.com/pulumi/opentofu/configs"
	"github.com/pulumi/opentofu/states"
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge"
	shim "github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfshim"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/zclconf/go-cty/cty"
)

// ModuleMap is the top-level structure for the module-map.json sidecar file.
type ModuleMap struct {
	// FormatVersion is the digest file format this tool wrote. LoadDigest
	// refuses a version newer than it knows; 0 (absent) predates the field.
	FormatVersion int                        `json:"digestFormatVersion,omitempty"`
	Modules       map[string]*ModuleMapEntry `json:"modules"`
	RootResources []ModuleResource           `json:"rootResources,omitempty"`
	// Providers maps each Terraform provider address to the Pulumi provider
	// the digest's property names were computed with — the pin patch-state
	// loads schemas by. Values: "name@version" (statically bridged),
	// "dynamic@<tfVersion>" or bare "dynamic" (dynamically bridged, with the
	// Terraform version when the lock file supplied one), or "" (digest
	// written before versions were recorded — unpinned, warned at load).
	Providers map[string]string `json:"providers,omitempty"`
	// ImportSupportChecked records whether resource types were checked for
	// import support. Without it, a digest built with the check skipped is
	// indistinguishable from one where the check ran and flagged nothing, and
	// consumers cannot tell "nothing is non-importable" from "nobody looked".
	ImportSupportChecked bool `json:"importSupportChecked,omitempty"`
}

// ModuleResource represents a single resource within a module instance.
type ModuleResource struct {
	Mode             string                 `json:"mode"` // "managed" or "data"
	TranslatedURN    string                 `json:"translatedUrn"`
	TerraformAddress string                 `json:"terraformAddress"`
	ImportID         string                 `json:"importId"`
	Attributes       map[string]interface{} `json:"attributes,omitempty"`
	// NonImportable marks a resource whose Terraform type declares no importer.
	// "pulumi import" cannot bring it into state — the attempt fails with a
	// misleading "resource '<id>' does not exist" — so resolve leaves it out of
	// the import file rather than emitting an entry guaranteed to fail.
	NonImportable bool `json:"nonImportable,omitempty"`
	// PulumiOutputs are the Terraform attributes converted to Pulumi property
	// names and shapes, and RawStateDelta is the bridge's
	// __pulumi_raw_state_delta for them. Both are computed from REDACTED
	// attributes, so neither ever contains a secret, and both are populated
	// only for resources NonImportable flags.
	PulumiOutputs map[string]interface{} `json:"pulumiOutputs,omitempty"`
	RawStateDelta map[string]interface{} `json:"rawStateDelta,omitempty"`
	// RawStateDeltaReason says why RawStateDelta is absent when computing one
	// was attempted; InjectionStateReason says why the resource has no
	// injection state at all. The distinction is what lets a reader tell "we
	// tried and could not" from "nobody looked", which PulumiOutputs == nil
	// alone cannot express and which need different responses.
	RawStateDeltaReason  string `json:"rawStateDeltaReason,omitempty"`
	InjectionStateReason string `json:"injectionStateReason,omitempty"`
	// SchemaVersion is written into state as __meta, so a later provider
	// upgrade runs the right state upgraders.
	SchemaVersion int64 `json:"schemaVersion,omitempty"`
}

// ImportSupportChecker reports whether a Terraform resource type can be
// imported. providerAddr is the full provider source address (e.g.
// "registry.terraform.io/hashicorp/aws"). Implemented by
// *importsupport.Prober.
type ImportSupportChecker interface {
	Check(ctx context.Context, providerAddr, tfType string) importsupport.Support
}

type ProviderAccessor interface {
	Provider(ctx context.Context, providerAddr string) (tfprovider.Provider, bool)
}

// ModuleMapEntry represents a single module instance in the module map.
type ModuleMapEntry struct {
	TerraformPath string                     `json:"terraformPath"`
	Source        string                     `json:"source,omitempty"`
	IndexKey      string                     `json:"indexKey,omitempty"`
	IndexType     string                     `json:"indexType,omitempty"`
	Resources     []ModuleResource           `json:"resources"`
	Interface     *ModuleInterface           `json:"interface,omitempty"`
	Modules       map[string]*ModuleMapEntry `json:"modules,omitempty"`
}

// ModuleInterface describes the inputs and outputs of a module.
type ModuleInterface struct {
	Inputs  []ModuleInterfaceField `json:"inputs"`
	Outputs []ModuleInterfaceField `json:"outputs"`
}

// ModuleInterfaceField describes a single input variable or output value.
type ModuleInterfaceField struct {
	Name           string      `json:"name"`
	Type           interface{} `json:"type,omitempty"`
	Required       bool        `json:"required,omitempty"`
	Default        interface{} `json:"default,omitempty"`
	Description    string      `json:"description,omitempty"`
	Expression     string      `json:"expression,omitempty"`
	EvaluatedValue interface{} `json:"evaluatedValue,omitempty"`
}

// BuildModuleMap constructs a ModuleMap from Terraform configuration and state.
// tofuCtx and state may be nil if evaluation is not available.
// pulumiProviders may be nil if URN generation should fall back to raw addresses.
// importChecker may be nil to skip flagging non-importable resource types.
func BuildModuleMap(
	ctx context.Context,
	config *configs.Config,
	evalScopes *EvalScopes,
	state *states.State,
	pulumiProviders map[providermap.TerraformProviderName]*ProviderWithMetadata,
	stackName string,
	projectName string,
	importChecker ImportSupportChecker,
) (*ModuleMap, error) {
	mm := &ModuleMap{
		Modules:              make(map[string]*ModuleMapEntry),
		ImportSupportChecked: importChecker != nil,
	}

	// Store provider registry addresses for downstream consumers (e.g., patch-state).
	// The value is the Pulumi provider the digest's property names came from
	// ("name@version", or "dynamic[@tfVersion]"): injection re-resolves schemas
	// from this map, and without the version a digest computed against one
	// provider major and consumed against another is indistinguishable from a
	// matched pair.
	if pulumiProviders != nil {
		mm.Providers = make(map[string]string, len(pulumiProviders))
		for tfAddr, pwm := range pulumiProviders {
			mm.Providers[string(tfAddr)] = pwm.ResolvedPulumi
		}
	}

	fmt.Fprintf(os.Stderr, "  Building module entries...\n")
	err := buildModuleMapLevel(ctx, mm.Modules, config, evalScopes, state, pulumiProviders, stackName, projectName, nil, importChecker) //nolint:lll
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "  Found %d module entries\n", len(mm.Modules))

	// Collect root-level resources (empty segments = root module).
	fmt.Fprintf(os.Stderr, "  Matching root-level resources...\n")
	rootResources, err := matchResources(ctx, state, nil, pulumiProviders, stackName, projectName, importChecker)
	if err != nil {
		return nil, err
	}
	if len(rootResources) > 0 {
		mm.RootResources = rootResources
	}
	fmt.Fprintf(os.Stderr, "  Found %d root resources\n", len(rootResources))

	return mm, nil
}

// buildModuleMapLevel processes one level of module calls and recurses into children.
// parentSegments tracks the module path prefix for nested modules.
func buildModuleMapLevel(
	ctx context.Context,
	target map[string]*ModuleMapEntry,
	config *configs.Config,
	evalScopes *EvalScopes,
	state *states.State,
	pulumiProviders map[providermap.TerraformProviderName]*ProviderWithMetadata,
	stackName string,
	projectName string,
	parentSegments []moduleSegment,
	importChecker ImportSupportChecker,
) error {
	if config == nil || config.Module == nil {
		return nil
	}

	for name, call := range config.Module.ModuleCalls {
		// Discover instances of this module from state.
		instances := discoverModuleInstances(state, parentSegments, name)
		fmt.Fprintf(os.Stderr, "    module %s: %d instance(s)\n", name, len(instances))

		// Get call-site expression text for each attribute.
		callExpressions := getCallExpressions(call)

		for _, inst := range instances {
			segments := make([]moduleSegment, len(parentSegments)+1)
			copy(segments, parentSegments)
			segments[len(parentSegments)] = moduleSegment{name: name, key: inst.key}

			mapKey := name
			if inst.key != "" {
				mapKey = name + "[" + formatKey(inst.key) + "]"
			}

			fmt.Fprintf(os.Stderr, "      %s: matching resources...\n", mapKey)
			moduleResources, err := matchResources(ctx, state, segments, pulumiProviders, stackName, projectName, importChecker)
			if err != nil {
				return err
			}
			entry := &ModuleMapEntry{
				TerraformPath: buildModulePath(segments),
				Source:        call.SourceAddrRaw,
				IndexKey:      inst.key,
				Resources:     moduleResources,
			}
			fmt.Fprintf(os.Stderr, "      %s: %d resources\n", mapKey, len(entry.Resources))

			// Determine index type.
			if inst.key != "" {
				if _, err := fmt.Sscanf(inst.key, "%d", new(int)); err == nil {
					entry.IndexType = "int"
				} else {
					entry.IndexType = "string"
				}
			}

			// Build interface from child config.
			childConfig := config.Children[name]
			if childConfig != nil && childConfig.Module != nil {
				fmt.Fprintf(os.Stderr, "      %s: building interface...\n", mapKey)
				entry.Interface = buildModuleInterface(childConfig, callExpressions)

				// If eval is available, populate evaluatedValue for inputs.
				if evalScopes != nil {
					fmt.Fprintf(os.Stderr, "      %s: evaluating expressions...\n", mapKey)
					populateEvaluatedValues(entry.Interface, evalScopes, segments)
				}
			}

			// Recurse into nested modules.
			if childConfig != nil && len(childConfig.Module.ModuleCalls) > 0 {
				entry.Modules = make(map[string]*ModuleMapEntry)
				err := buildModuleMapLevel(
					ctx, entry.Modules, childConfig, evalScopes, state,
					pulumiProviders, stackName, projectName, segments, importChecker,
				)
				if err != nil {
					return err
				}
				if len(entry.Modules) == 0 {
					entry.Modules = nil
				}
			}

			target[mapKey] = entry
		}
	}

	return nil
}

// moduleInstance represents a discovered module instance from state.
type moduleInstance struct {
	key string // empty for non-indexed, "0"/"1" for count, "key" for for_each
}

// discoverModuleInstances finds unique module instances from raw state that match
// the given parent path and module name.
func discoverModuleInstances(state *states.State, parentSegments []moduleSegment, moduleName string) []moduleInstance {
	seen := map[string]bool{}
	var instances []moduleInstance

	parentDepth := len(parentSegments)

	if state != nil {
		for _, module := range state.Modules {
			segments := moduleSegmentsFromAddr(module.Addr)
			if len(segments) <= parentDepth {
				continue
			}

			// Check that parent path matches.
			match := true
			for i, ps := range parentSegments {
				if segments[i].name != ps.name || segments[i].key != ps.key {
					match = false
					break
				}
			}
			if !match {
				continue
			}

			// Check that the next segment matches our module name.
			seg := segments[parentDepth]
			if seg.name != moduleName {
				continue
			}

			instanceKey := seg.key
			if !seen[instanceKey] {
				seen[instanceKey] = true
				instances = append(instances, moduleInstance{key: instanceKey})
			}
		}
	}

	// Sort instances for deterministic output.
	sort.Slice(instances, func(i, j int) bool {
		return instances[i].key < instances[j].key
	})

	// If no instances found in state, still emit one entry for non-indexed modules
	// if they exist in config (they might just have no resources).
	if len(instances) == 0 {
		instances = append(instances, moduleInstance{key: ""})
	}

	return instances
}

// matchResources finds resources in raw state that belong to the given module instance
// and returns ModuleResource entries with URN, Terraform address, and import ID.
func matchResources(
	ctx context.Context,
	state *states.State,
	segments []moduleSegment,
	pulumiProviders map[providermap.TerraformProviderName]*ProviderWithMetadata,
	stackName string,
	projectName string,
	importChecker ImportSupportChecker,
) ([]ModuleResource, error) {
	var resources []ModuleResource
	modulePath := buildModulePath(segments)

	if state != nil {
		for _, module := range state.Modules {
			modSegments := moduleSegmentsFromAddr(module.Addr)
			if buildModulePath(modSegments) != modulePath {
				continue
			}

			for _, res := range module.Resources {
				providerName := res.ProviderConfig.Provider.String()
				resourceType := res.Addr.Resource.Type

				for instKey, inst := range res.Instances {
					if inst.Current == nil {
						continue
					}

					// Build the full address: module path + resource address + instance key
					address := res.Addr.Resource.String()
					if instKey != nil {
						address += instKey.String()
					}
					if len(module.Addr) > 0 {
						address = module.Addr.String() + "." + address
					}

					// Parse attributes from AttrsJSON.
					var attrs map[string]interface{}
					importID := ""
					if inst.Current.AttrsJSON != nil {
						if parsed, err := decodeAttrs(inst.Current.AttrsJSON); err == nil {
							attrs = parsed
							if id, ok := attrs["id"]; ok {
								importID = formatImportID(id)
							}
						}
					}

					// Determine mode string.
					mode := "managed"
					if res.Addr.Resource.Mode == addrs.DataResourceMode {
						mode = "data"
					}

					// Data sources don't map to Pulumi resources.
					urn := ""
					if mode == "managed" {
						urn = buildResourceURN(address, providerName, resourceType, pulumiProviders, stackName, projectName)
					}

					mr := ModuleResource{
						Mode:             mode,
						TranslatedURN:    urn,
						TerraformAddress: address,
						ImportID:         importID,
					}

					if attrs != nil {
						redactSensitivePaths(attrs, inst.Current.AttrSensitivePaths)
						// The provider schema is a second, independent source.
						// Redaction is otherwise driven entirely by the state's
						// own sensitive marks, so where those are missing it
						// runs, reports success, and leaves the secret in the
						// clear. Anything the schema marks and the state did
						// not is redacted here, and DiscoverSensitiveSecrets
						// recovers it into stack config from the same schema.
						schemaMap := bridgedSchemaMap(pulumiProviders, providerName, resourceType)
						redactSchemaSensitive(attrs, schemaMap)
						// Nested recovery is top-level only throughout, so a
						// nested attribute the state did not mark cannot be
						// redacted here without making it unresolvable later.
						// Failing is the honest answer for that case.
						if leaks := schemaSensitiveLeaks(attrs, schemaMap); len(leaks) > 0 {
							return nil, fmt.Errorf(
								"%s: the provider schema marks the nested attribute(s) %s sensitive, but the "+
									"Terraform state carries no sensitive mark for them, so the digest would "+
									"record the real values in plaintext. Recovering a nested secret from stack "+
									"config is not implemented (see issue #28), so this cannot be redacted "+
									"either. Re-run \"terraform refresh\" (or \"tofu refresh\") so the state "+
									"records the marks, or exclude this resource",
								address, strings.Join(leaks, ", "))
						}
						mr.Attributes = attrs
					}

					if mode == "managed" && importChecker != nil {
						mr.NonImportable = importChecker.Check(ctx, providerName, resourceType) == importsupport.Unsupported
						if mr.NonImportable && attrs != nil {
							populateInjectionState(ctx, &mr, importChecker, pulumiProviders, providerName, resourceType, attrs)
						}
					}

					resources = append(resources, mr)
				}
			}
		}
	}

	if resources == nil {
		resources = []ModuleResource{}
	}
	return resources, nil
}

// bridgedSchemaMap returns the bridged Terraform schema for a resource type,
// or nil when no provider was loaded for it — in which case there is no second
// source to cross-check redaction against.
func bridgedSchemaMap(
	pulumiProviders map[providermap.TerraformProviderName]*ProviderWithMetadata,
	providerName, resourceType string,
) shim.SchemaMap {
	pwm := lookupBridgedProvider(pulumiProviders, providerName)
	if pwm == nil {
		return nil
	}
	shimResource := pwm.P.ResourcesMap().Get(resourceType)
	if shimResource == nil {
		return nil
	}
	return shimResource.Schema()
}

// decodeAttrs decodes AttrsJSON with json.Number rather than float64:
// integers above 2^53 otherwise decode to a different number, silently, and
// that value flows into the sidecar and on into Pulumi state.
func decodeAttrs(data []byte) (map[string]interface{}, error) {
	var attrs map[string]interface{}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&attrs); err != nil {
		return nil, err
	}
	return attrs, nil
}

func formatImportID(v interface{}) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func warnInjectionState(mr *ModuleResource) {
	fmt.Fprintf(os.Stderr, "Warning: no injection state for %s: %s\n",
		mr.TerraformAddress, mr.InjectionStateReason)
}

func populateInjectionState(
	ctx context.Context,
	mr *ModuleResource,
	importChecker ImportSupportChecker,
	pulumiProviders map[providermap.TerraformProviderName]*ProviderWithMetadata,
	providerName string,
	resourceType string,
	attrs map[string]interface{},
) {
	accessor, ok := importChecker.(ProviderAccessor)
	if !ok {
		mr.InjectionStateReason = "no live Terraform provider (import-support probe not run)"
		return
	}
	pair := resolveInjectionProviders(ctx, accessor, pulumiProviders, providerName, resourceType)
	if pair.MissingReason != "" {
		mr.InjectionStateReason = pair.MissingReason
		warnInjectionState(mr)
		return
	}
	prov, schemaMap, schemaInfos := pair.Live, pair.SchemaMap, pair.SchemaInfos

	attrsJSON, err := json.Marshal(attrs)
	if err != nil {
		mr.InjectionStateReason = fmt.Sprintf("re-marshalling redacted attributes: %v", err)
		warnInjectionState(mr)
		return
	}

	outputs, delta, deltaReason, version, ok := safeComputeInjectionState(
		ctx, prov, resourceType, attrsJSON, schemaMap, schemaInfos)
	if !ok {
		mr.InjectionStateReason = fmt.Sprintf(
			"computing injection state for %s panicked or failed; the resource will be "+
				"injected from raw attribute renaming instead", resourceType)
		warnInjectionState(mr)
		return
	}
	mr.PulumiOutputs = outputs
	mr.RawStateDelta = delta
	mr.SchemaVersion = version
	if delta == nil && deltaReason != "" {
		fmt.Fprintf(os.Stderr, "Warning: no raw state delta for %s (%s): %s\n",
			mr.TerraformAddress, resourceType, deltaReason)
		mr.RawStateDeltaReason = deltaReason
	}
}

func safeComputeInjectionState(
	ctx context.Context,
	prov tfprovider.Provider,
	tfType string,
	attrsJSON []byte,
	schemaMap shim.SchemaMap,
	schemaInfos map[string]*tfbridge.SchemaInfo,
) (outputs map[string]interface{}, delta map[string]interface{}, deltaReason string, version int64, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "Warning: computing injection state for %s panicked: %v\n", tfType, r)
			outputs, delta, deltaReason, version, ok = nil, nil, "", 0, false
		}
	}()

	o, d, reason, v, err := ComputeInjectionState(ctx, prov, tfType, attrsJSON, schemaMap, schemaInfos)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: computing injection state for %s failed: %v\n", tfType, err)
		return nil, nil, "", 0, false
	}
	return o, d, reason, v, true
}

// buildResourceURN constructs a Pulumi URN for a Terraform resource, or falls back
// to the raw Terraform address if provider mapping is unavailable.
func buildResourceURN(
	address string,
	providerName string,
	resourceType string,
	pulumiProviders map[providermap.TerraformProviderName]*ProviderWithMetadata,
	stackName string,
	projectName string,
) string {
	if pulumiProviders == nil {
		return address
	}

	prov := lookupBridgedProvider(pulumiProviders, providerName)
	if prov == nil {
		return address
	}

	typeToken, err := bridge.PulumiTypeToken(resourceType, prov.Provider)
	if err != nil {
		return address
	}

	pulumiName := PulumiNameFromTerraformAddress(address, resourceType)
	return fmt.Sprintf("urn:pulumi:%s::%s::%s::%s", stackName, projectName, typeToken, pulumiName)
}

// getCallExpressions extracts the raw HCL expression text for each attribute
// in a module call's config body.
func getCallExpressions(call *configs.ModuleCall) map[string]string {
	result := make(map[string]string)
	if call.Config == nil {
		return result
	}

	attrs, _ := call.Config.JustAttributes()
	for attrName, attr := range attrs {
		rng := attr.Expr.Range()
		src, err := os.ReadFile(rng.Filename)
		if err != nil {
			continue
		}
		startByte := rng.Start.Byte
		endByte := rng.End.Byte
		if startByte >= 0 && endByte <= len(src) && startByte < endByte {
			result[attrName] = string(src[startByte:endByte])
		}
	}

	return result
}

// buildModuleInterface constructs a ModuleInterface from a child config's
// variables and outputs.
func buildModuleInterface(childConfig *configs.Config, callExpressions map[string]string) *ModuleInterface {
	iface := &ModuleInterface{}

	// Build inputs from variables.
	varNames := make([]string, 0, len(childConfig.Module.Variables))
	for name := range childConfig.Module.Variables {
		varNames = append(varNames, name)
	}
	sort.Strings(varNames)

	for _, varName := range varNames {
		v := childConfig.Module.Variables[varName]
		field := ModuleInterfaceField{
			Name:        varName,
			Description: v.Description,
		}

		// Type: convert cty.Type to a string representation.
		if v.Type != cty.NilType {
			field.Type = v.Type.FriendlyName()
		}

		// Required: a variable is required if it has no default value.
		if v.Default == cty.NilVal {
			field.Required = true
		} else {
			field.Default = ctyValueToInterface(v.Default)
		}

		// Expression from call site.
		if expr, ok := callExpressions[varName]; ok {
			field.Expression = expr
		}

		iface.Inputs = append(iface.Inputs, field)
	}

	// Build outputs.
	outputNames := make([]string, 0, len(childConfig.Module.Outputs))
	for name := range childConfig.Module.Outputs {
		outputNames = append(outputNames, name)
	}
	sort.Strings(outputNames)

	for _, outName := range outputNames {
		o := childConfig.Module.Outputs[outName]
		field := ModuleInterfaceField{
			Name:        outName,
			Description: o.Description,
		}
		iface.Outputs = append(iface.Outputs, field)
	}

	return iface
}

// populateEvaluatedValues uses pre-built EvalScopes to evaluate variable values
// in a specific module instance and populates the EvaluatedValue field.
func populateEvaluatedValues(
	iface *ModuleInterface,
	evalScopes *EvalScopes,
	segments []moduleSegment,
) {
	// Build the module instance address from segments.
	addr := addrs.RootModuleInstance
	for _, seg := range segments {
		if seg.key == "" {
			addr = addr.Child(seg.name, addrs.NoKey)
		} else if _, err := fmt.Sscanf(seg.key, "%d", new(int)); err == nil {
			var idx int
			fmt.Sscanf(seg.key, "%d", &idx)
			addr = addr.Child(seg.name, addrs.IntKey(idx))
		} else {
			addr = addr.Child(seg.name, addrs.StringKey(seg.key))
		}
	}

	scope := evalScopes.Scope(addr)
	if scope == nil {
		return
	}

	for i, input := range iface.Inputs {
		expr, parseDiags := hclsyntax.ParseExpression(
			[]byte("var."+input.Name), "<eval>", hcl.Pos{Line: 1, Column: 1},
		)
		if parseDiags.HasErrors() {
			continue
		}

		val, evalDiags := scope.EvalExpr(expr, cty.DynamicPseudoType)
		if evalDiags.HasErrors() {
			continue
		}

		iface.Inputs[i].EvaluatedValue = ctyValueToInterface(val)
	}
}

// ConfigEntry represents a config value to be set on a Pulumi stack.
// When Secret is true, the value is encrypted in the stack config.
type ConfigEntry struct {
	ConfigKey string
	Value     string
	Secret    bool
}

// SensitiveSecret is an alias for backwards compatibility.
type SensitiveSecret = ConfigEntry

// DiscoverSensitiveSecrets walks the state and collects all sensitive attribute
// values, returning them as config key / value pairs. The config key is derived
// by flattening the terraform address and attribute name.
//
// projectName is used for length checking: Pulumi config keys are limited to
// 128 chars total including the "project:" namespace prefix.
//
// After collecting all secrets, this function:
//  1. Deduplicates keys by appending _2, _3, etc. and warns to stderr
//  2. Checks key lengths and returns an error if any exceed the limit
func DiscoverSensitiveSecrets(
	state *states.State,
	projectName string,
	pulumiProviders map[providermap.TerraformProviderName]*ProviderWithMetadata,
) ([]SensitiveSecret, error) {
	if state == nil {
		return nil, nil
	}

	type rawSecret struct {
		address   string
		attribute string
		value     string
	}

	var raw []rawSecret
	for _, module := range state.Modules {
		for _, res := range module.Resources {
			providerName := res.ProviderConfig.Provider.String()
			resourceType := res.Addr.Resource.Type

			// The provider schema names sensitive attributes independently of
			// the state's marks, and matchResources redacts on that basis, so
			// discovery has to look there too — otherwise it would redact a
			// value it never recorded a config key for, and injection would
			// fail on a placeholder nothing can resolve.
			schemaMap := bridgedSchemaMap(pulumiProviders, providerName, resourceType)
			schemaSensitive := map[string]bool{}
			if schemaMap != nil {
				schemaMap.Range(func(name string, sch shim.Schema) bool {
					if sch.Sensitive() {
						schemaSensitive[name] = true
					}
					return true
				})
			}

			for instKey, inst := range res.Instances {
				if inst.Current == nil {
					continue
				}
				if len(inst.Current.AttrSensitivePaths) == 0 && len(schemaSensitive) == 0 {
					continue
				}

				// Build the full address.
				address := res.Addr.Resource.String()
				if instKey != nil {
					address += instKey.String()
				}
				if len(module.Addr) > 0 {
					address = module.Addr.String() + "." + address
				}

				// Parse attributes.
				if inst.Current.AttrsJSON == nil {
					continue
				}
				attrs, err := decodeAttrs(inst.Current.AttrsJSON)
				if err != nil {
					continue
				}

				// Collect sensitive top-level attributes, from either source.
				names := map[string]bool{}
				for _, pvm := range inst.Current.AttrSensitivePaths {
					if len(pvm.Path) != 1 {
						continue
					}
					if step, ok := pvm.Path[0].(cty.GetAttrStep); ok {
						names[step.Name] = true
					}
				}
				for name := range schemaSensitive {
					names[name] = true
				}

				sorted := make([]string, 0, len(names))
				for name := range names {
					sorted = append(sorted, name)
				}
				sort.Strings(sorted)

				for _, name := range sorted {
					value, exists := attrs[name]
					if !exists || value == nil {
						continue
					}
					raw = append(raw, rawSecret{
						address:   address,
						attribute: name,
						value:     fmt.Sprintf("%v", value),
					})
				}
			}
		}
	}

	sort.Slice(raw, func(i, j int) bool {
		if raw[i].address != raw[j].address {
			return raw[i].address < raw[j].address
		}
		return raw[i].attribute < raw[j].attribute
	})

	// Generate keys and handle dedup + length checking.
	maxKeyLen := 128 - len(projectName) - 1 // subtract "project:" namespace
	keyCounts := make(map[string]int)
	keyToAddress := make(map[string]string) // first address that produced each key
	var collisions []string

	var secrets []SensitiveSecret
	var tooLong []string

	for _, r := range raw {
		key := flattenAddress(r.address, r.attribute)
		keyCounts[key]++
		count := keyCounts[key]

		if count == 1 {
			keyToAddress[key] = r.address
		}

		finalKey := key
		if count > 1 {
			finalKey = fmt.Sprintf("%s_%d", key, count)
			collisions = append(collisions, fmt.Sprintf(
				"%q is produced by both %s and %s (attribute %q)",
				key, keyToAddress[key], r.address, r.attribute))
		}

		if len(finalKey) > maxKeyLen {
			tooLong = append(tooLong, fmt.Sprintf(
				"key %q (%d chars, max %d) from %s",
				finalKey, len(finalKey), maxKeyLen, r.address))
		}

		secrets = append(secrets, ConfigEntry{
			ConfigKey: finalKey,
			Value:     r.value,
			Secret:    true,
		})
	}

	if len(collisions) > 0 {
		for _, msg := range collisions {
			fmt.Fprintf(os.Stderr, "  ERROR: colliding config key: %s\n", msg)
		}
		return secrets, fmt.Errorf(
			"%d config key collision(s): two sensitive attributes flatten to the same stack "+
				"config key, and the sidecar cannot tell them apart — injection would write one "+
				"resource's secret into the other. Rename or remap the resources so their "+
				"addresses differ after flattening, or set these secrets by hand and re-run "+
				"with --skip-secrets", len(collisions))
	}

	if len(tooLong) > 0 {
		for _, msg := range tooLong {
			fmt.Fprintf(os.Stderr, "  ERROR: config key too long: %s\n", msg)
		}
		return secrets, fmt.Errorf("%d config key(s) exceed the 128-char Pulumi limit (including %q namespace)", len(tooLong), projectName+":")
	}

	return secrets, nil
}

// flattenAddress converts a terraform address + attribute into a concise Pulumi config key.
//
// Terraform addresses like:
//
//	module.console_secrets["mysvc-console-service-develop"].aws_secretsmanager_secret_version.this["mysvc-console-service-develop/cap_client_oauth"]
//
// are shortened by:
//  1. Stripping all "module." prefixes
//  2. Stripping resource types (e.g. aws_secretsmanager_secret_version)
//  3. Stripping generic resource names like "this", "ssm_parameters"
//  4. Deduplicating for_each keys that repeat between module and resource levels
//
// The result is a human-readable key like "console_secrets_cap_client_oauth_secret_string".
func flattenAddress(address, attribute string) string {
	clean := strings.NewReplacer(
		"\"", "",
		" ", "_",
	)
	address = clean.Replace(address)

	// Generic resource names that add no value.
	genericNames := map[string]bool{
		"this":           true,
		"ssm_parameters": true,
	}

	// Parse the address into module segments and a resource tail.
	// Address forms:
	//   module.A[k1].module.B[k2].resource_type.name[k3]   (nested modules)
	//   module.A[k1].resource_type.name[k3]                 (single module)
	//   resource_type.name[k3]                              (root resource)
	type segment struct {
		name string
		key  string
	}
	var moduleSegments []segment
	var resourceName, resourceKey string

	// Split into dot-separated parts, handling brackets.
	remaining := address
	var parts []string
	for remaining != "" {
		// Find next dot that isn't inside brackets.
		depth := 0
		dotIdx := -1
		for i, c := range remaining {
			switch c {
			case '[':
				depth++
			case ']':
				depth--
			case '.':
				if depth == 0 {
					dotIdx = i
				}
			}
			if dotIdx >= 0 {
				break
			}
		}
		if dotIdx >= 0 {
			parts = append(parts, remaining[:dotIdx])
			remaining = remaining[dotIdx+1:]
		} else {
			parts = append(parts, remaining)
			remaining = ""
		}
	}

	// Walk parts: "module" keywords indicate module segments; the last two non-module
	// parts are resource_type and resource_name.
	i := 0
	for i < len(parts) {
		if parts[i] == "module" && i+1 < len(parts) {
			name, key := splitForEachKey(parts[i+1])
			moduleSegments = append(moduleSegments, segment{name: name, key: key})
			i += 2
		} else {
			break
		}
	}

	// Remaining parts: resource_type[.resource_name[key]]
	// parts[i] = resource type (discarded), parts[i+1] = resource name + optional key
	if i+1 < len(parts) {
		// Skip resource type at parts[i]
		resourceName, resourceKey = splitForEachKey(parts[i+1])
	} else if i < len(parts) {
		// Only resource type, no separate name (rare)
		resourceName, _ = splitForEachKey(parts[i])
	}

	// Build key from meaningful segments.
	var keyParts []string

	// Collect all sanitized module keys for dedup.
	var allModuleKeys []string
	for _, ms := range moduleSegments {
		keyParts = append(keyParts, ms.name)
		if ms.key != "" {
			sanitized := sanitizeSegment(ms.key)
			keyParts = append(keyParts, sanitized)
			allModuleKeys = append(allModuleKeys, sanitized)
		}
	}

	// Include resource name only if it's not generic.
	if resourceName != "" && !genericNames[resourceName] {
		keyParts = append(keyParts, resourceName)
	}

	// Include resource key, deduplicating against module keys.
	if resourceKey != "" {
		sanitized := sanitizeSegment(resourceKey)
		// Try to strip redundant prefixes from module keys.
		for _, mk := range allModuleKeys {
			if sanitized == mk {
				sanitized = ""
				break
			}
			if strings.HasPrefix(sanitized, mk+"_") {
				sanitized = sanitized[len(mk)+1:]
				break
			}
		}
		if sanitized != "" {
			keyParts = append(keyParts, sanitized)
		}
	}

	keyParts = append(keyParts, attribute)
	return strings.Join(keyParts, "_")
}

// splitForEachKey splits "name[key]" into ("name", "key") or ("name", "") if no key.
func splitForEachKey(s string) (string, string) {
	if idx := strings.Index(s, "["); idx >= 0 {
		key := strings.TrimRight(s[idx+1:], "]")
		return s[:idx], key
	}
	return s, ""
}

// sanitizeSegment replaces non-alphanumeric chars with underscores and collapses runs.
func sanitizeSegment(s string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
			lastUnderscore = false
		} else if !lastUnderscore {
			b.WriteRune('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}

// SetSecretsFromState writes config entries to Pulumi stack config using the automation API.
// Entries with Secret=true are encrypted; others are set as plain config.
// Secret values are never printed or logged.
func SetSecretsFromState(entries []ConfigEntry, projectDir, projectName, stack, runtime string) error {
	// Ensure a Pulumi project exists before stack operations.
	if err := ensurePulumiProject(projectDir, projectName, runtime); err != nil {
		return err
	}

	configMap := make(auto.ConfigMap, len(entries))
	for _, e := range entries {
		configMap[e.ConfigKey] = auto.ConfigValue{Value: e.Value, Secret: e.Secret}
	}

	if err := writeConfigValues(projectDir, stack, configMap); err != nil {
		return err
	}

	var secretCount, plainCount int
	for _, e := range entries {
		if e.Secret {
			secretCount++
		} else {
			plainCount++
		}
	}
	fmt.Fprintf(os.Stderr, "Set %d secrets and %d plain config values on stack %s\n", secretCount, plainCount, stack)
	return nil
}

// writeConfigValues creates a local workspace, ensures the stack exists, and writes config values.
// It sets each key individually to avoid overwriting existing config.
func writeConfigValues(projectDir, stack string, configMap auto.ConfigMap) error {
	ctx := context.Background()
	ws, err := auto.NewLocalWorkspace(ctx, auto.WorkDir(projectDir))
	if err != nil {
		return fmt.Errorf("creating workspace: %w", err)
	}

	// Create the stack if it doesn't already exist.
	fmt.Fprintf(os.Stderr, "Ensuring stack %s exists...\n", stack)
	if err := ws.CreateStack(ctx, stack); err != nil && !auto.IsCreateStack409Error(err) {
		return fmt.Errorf("creating stack %s: %w", stack, err)
	}

	for key, val := range configMap {
		if err := ws.SetConfig(ctx, stack, key, val); err != nil {
			return fmt.Errorf("setting config key %q: %w", key, err)
		}
	}
	return nil
}

// redactSensitivePaths replaces sensitive attribute values with "(sensitive)" based on
// the AttrSensitivePaths from the state. This uses the sensitivity information that
// Terraform/OpenTofu persists in the state file itself, which tracks sensitivity from
// both provider schemas and sensitive variable propagation.
func redactSensitivePaths(attrs map[string]interface{}, paths []cty.PathValueMarks) {
	for _, pvm := range paths {
		if len(pvm.Path) == 0 {
			continue
		}
		redactAtPath(attrs, pvm.Path)
	}
}

func redactAtPath(container interface{}, path cty.Path) {
	if len(path) == 0 {
		return
	}
	last := len(path) == 1

	switch step := path[0].(type) {
	case cty.GetAttrStep:
		m, ok := container.(map[string]interface{})
		if !ok {
			return
		}
		value, exists := m[step.Name]
		if !exists {
			return
		}
		if !last {
			redactAtPath(value, path[1:])
			return
		}
		if value == nil {
			return
		}
		m[step.Name] = redactedPlaceholder

	case cty.IndexStep:
		switch c := container.(type) {
		case []interface{}:
			idx, ok := indexStepOrdinal(step, len(c))
			if !ok {
				for i := range c {
					redactSliceElement(c, i, path, last)
				}
				return
			}
			redactSliceElement(c, idx, path, last)
		case map[string]interface{}:
			key, ok := indexStepKey(step)
			if !ok {
				return
			}
			value, exists := c[key]
			if !exists {
				return
			}
			if !last {
				redactAtPath(value, path[1:])
				return
			}
			if value == nil {
				return
			}
			c[key] = redactedPlaceholder
		}
	}
}

func redactSliceElement(s []interface{}, i int, path cty.Path, last bool) {
	if i < 0 || i >= len(s) {
		return
	}
	if !last {
		redactAtPath(s[i], path[1:])
		return
	}
	if s[i] == nil {
		return
	}
	s[i] = redactedPlaceholder
}

func indexStepOrdinal(step cty.IndexStep, length int) (int, bool) {
	if step.Key.Type() != cty.Number {
		return 0, false
	}
	f, _ := step.Key.AsBigFloat().Float64()
	i := int(f)
	if i < 0 || i >= length {
		return 0, false
	}
	return i, true
}

func indexStepKey(step cty.IndexStep) (string, bool) {
	if step.Key.Type() != cty.String {
		return "", false
	}
	return step.Key.AsString(), true
}

// ctyValueToInterface converts a cty.Value to a plain Go value suitable for JSON serialization.
// Values marked as sensitive (via cty marks from OpenTofu's sensitivity tracking) are redacted.
func ctyValueToInterface(v cty.Value) interface{} {
	if v == cty.NilVal || !v.IsKnown() {
		return nil
	}

	// Strip marks (sensitivity, etc.). If the value is marked sensitive, redact it.
	if v.IsMarked() {
		unmarked, marks := v.Unmark()
		for mark := range marks {
			if mark == "sensitive" {
				return "(sensitive)"
			}
		}
		v = unmarked
	}

	if v.IsNull() {
		return nil
	}

	ty := v.Type()

	switch {
	case ty == cty.String:
		return v.AsString()
	case ty == cty.Number:
		bf := v.AsBigFloat()
		if bf.IsInt() {
			i, _ := bf.Int64()
			return i
		}
		f, _ := bf.Float64()
		return f
	case ty == cty.Bool:
		return v.True()
	case ty.IsListType() || ty.IsTupleType() || ty.IsSetType():
		var result []interface{}
		for it := v.ElementIterator(); it.Next(); {
			_, elem := it.Element()
			result = append(result, ctyValueToInterface(elem))
		}
		return result
	case ty.IsMapType() || ty.IsObjectType():
		result := make(map[string]interface{})
		for it := v.ElementIterator(); it.Next(); {
			key, elem := it.Element()
			result[key.AsString()] = ctyValueToInterface(elem)
		}
		return result
	default:
		return nil
	}
}

// WriteModuleMap serializes a ModuleMap to JSON and writes it to the given path.
func WriteModuleMap(mm *ModuleMap, path string) error {
	mm.FormatVersion = CurrentDigestFormatVersion
	data, err := json.MarshalIndent(mm, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling module map: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing module map to %s: %w", path, err)
	}
	return nil
}
