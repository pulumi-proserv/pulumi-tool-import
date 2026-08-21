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
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg/providermap"
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge"
)

const secretPlaceholder = "[secret]"

// unknownPlaceholder is how the engine serializes a value that is not yet
// known at preview time (plugin.UnknownStringValue). It reaches injection
// because an injected resource can depend on another injected resource, whose
// outputs are not in state yet. Copied into state it becomes an
// ordinary-looking string no later operation can tell from a real value.
const unknownPlaceholder = "04da6b54-80e4-46f7-96ec-b56ff0331ba9"

const (
	rawStateDeltaKey = "__pulumi_raw_state_delta"
	metaKey          = "__meta"
)

// isReservedOutputKey reports whether k is tool/bridge bookkeeping rather
// than a resource property. The one predicate for every loop that walks an
// outputs bag — a new reserved key added here covers them all.
func isReservedOutputKey(k string) bool {
	return k == rawStateDeltaKey || k == metaKey || k == reservedDefaultsKey
}

type InjectResult struct {
	Injected                   int
	SecretsResolved            int
	DeltaAttached              int
	DeltaAbsentFromSidecar     int
	DeltaDroppedSensitive      int
	DeltaDroppedUnrecoverable  int
	DeltaAbsentNotes           []string
	DeltaDroppedNotes          []string
	DeltaDroppedSensitiveNotes []string
	URNs                       []string
}

func InjectNonImportable(
	stateData []byte,
	sidecar *NonImportableFile,
	preview *PreviewDigest,
	providers map[providermap.TerraformProviderName]*ProviderWithMetadata,
	configSecrets map[string]string,
) ([]byte, *InjectResult, error) {
	if sidecar == nil || len(sidecar.Resources) == 0 {
		// Verified even though nothing was injected: callers rely on the bytes
		// this returns having been checked, whether or not it changed them.
		if err := VerifyDeploymentIntegrity(stateData); err != nil {
			return nil, nil, err
		}
		return stateData, &InjectResult{}, nil
	}
	if preview == nil {
		return nil, nil, fmt.Errorf("injection requires a preview: run \"pulumi preview --json\"")
	}

	var state map[string]interface{}
	dec := json.NewDecoder(bytes.NewReader(stateData))
	dec.UseNumber()
	if err := dec.Decode(&state); err != nil {
		return nil, nil, fmt.Errorf("parsing state: %w", err)
	}

	deployment, ok := state["deployment"].(map[string]interface{})
	if !ok {
		return nil, nil, fmt.Errorf("state missing deployment")
	}
	resources, ok := deployment["resources"].([]interface{})
	if !ok {
		return nil, nil, fmt.Errorf("state missing resources")
	}

	creates, err := preview.CreatesByTypeName()
	if err != nil {
		return nil, nil, err
	}

	typeMap := BuildPulumiToTFTypeMap(providers)
	result := &InjectResult{}

	built := make([]map[string]interface{}, 0, len(sidecar.Resources))
	seen := make(map[PreviewKey]*NonImportableResource, len(sidecar.Resources))
	for i := range sidecar.Resources {
		r := &sidecar.Resources[i]
		key := PreviewKey{Type: r.Type, Name: r.Name}

		if prev, dup := seen[key]; dup {
			return nil, nil, fmt.Errorf(
				"sidecar lists %s %q twice, for both %s and %s; each preview create step must "+
					"match exactly one sidecar entry",
				r.Type, r.Name, prev.TerraformAddress, r.TerraformAddress)
		}
		seen[key] = r

		newState, err := creates.Lookup(key)
		if err != nil {
			return nil, nil, err
		}
		if newState == nil {
			return nil, nil, fmt.Errorf(
				"no create step in the preview matches %s %q (%s); the program must declare "+
					"this resource for its state to be injected",
				r.Type, r.Name, r.TerraformAddress)
		}

		obj, secrets, outcome, note, err := buildInjectedResource(r, newState, typeMap, providers, configSecrets)
		if err != nil {
			return nil, nil, err
		}
		result.SecretsResolved += secrets
		switch outcome {
		case deltaOK:
			result.DeltaAttached++
		case deltaAbsent:
			result.DeltaAbsentFromSidecar++
			result.DeltaAbsentNotes = append(result.DeltaAbsentNotes, note)
		case deltaDroppedSensitive:
			result.DeltaDroppedSensitive++
			result.DeltaDroppedSensitiveNotes = append(result.DeltaDroppedSensitiveNotes, note)
		case deltaDroppedUnrecoverable:
			result.DeltaDroppedUnrecoverable++
			result.DeltaDroppedNotes = append(result.DeltaDroppedNotes, note)
		}
		built = append(built, obj)
	}

	orderInjected(built)
	for _, obj := range built {
		resources = append(resources, obj)
		result.Injected++
		if urn, ok := obj["urn"].(string); ok {
			result.URNs = append(result.URNs, urn)
		}
	}
	deployment["resources"] = resources

	out, err := json.MarshalIndent(state, "", "    ")
	if err != nil {
		return nil, nil, fmt.Errorf("serializing injected state: %w", err)
	}
	if err := VerifyDeploymentIntegrity(out); err != nil {
		return nil, nil, err
	}
	return out, result, nil
}

type deltaOutcome int

const (
	deltaOK deltaOutcome = iota
	deltaAbsent
	deltaDroppedSensitive
	deltaDroppedUnrecoverable
)

func buildInjectedResource(
	r *NonImportableResource,
	newState map[string]interface{},
	typeMap map[string]string,
	providers map[providermap.TerraformProviderName]*ProviderWithMetadata,
	configSecrets map[string]string,
) (map[string]interface{}, int, deltaOutcome, string, error) {
	obj := make(map[string]interface{}, len(newState)+3)
	for k, v := range newState {
		if k == rawStateDeltaKey {
			continue
		}
		obj[k] = v
	}
	obj["custom"] = true
	// A custom resource with no ID passes every check this tool makes and then
	// panics the engine on the NEXT operation: VerifyDeploymentIntegrity only
	// rejects the inverse case, and the provider plugin asserts on it —
	// contract.Assertf(req.ID != "", "Diff requires an ID").
	if r.ID == "" {
		return nil, 0, deltaOK, "", fmt.Errorf(
			"%s %q has no import ID: the sidecar records no top-level \"id\" attribute for it, "+
				"and a custom resource without an ID corrupts the deployment — the next "+
				"operation against this stack fails inside the engine rather than reporting "+
				"the missing value. Set the resource's ID in the sidecar by hand, or exclude it",
			r.Type, r.Name)
	}
	obj["id"] = r.ID

	var fields map[string]*SchemaFieldInfo
	if prov, tfType, found := LookupProviderForPulumiType(r.Type, typeMap, providers); found {
		fields = GetSchemaFieldInfo(prov, tfType)
	}

	var outputs map[string]interface{}
	if r.PulumiOutputs != nil {
		outputs = make(map[string]interface{}, len(r.PulumiOutputs))
		for k, v := range r.PulumiOutputs {
			outputs[k] = v
		}
	} else {
		outputs = MapTFAttributesToPulumi(r.Attributes, fields)
		if r.InjectionStateReason != "" {
			fmt.Fprintf(os.Stderr,
				"  WARNING: %s (%s %q) injected from raw attribute renaming, not the "+
					"schema-aware conversion: %s\n",
				r.TerraformAddress, r.Type, r.Name, r.InjectionStateReason)
		} else {
			fmt.Fprintf(os.Stderr,
				"  WARNING: %s (%s %q) injected from raw attribute renaming: the sidecar "+
					"carries no Pulumi outputs and no reason. Re-run \"digest tf\" without "+
					"--skip-import-check to compute them.\n",
				r.TerraformAddress, r.Type, r.Name)
		}
	}

	secretsResolved, err := resolveOutputSecrets(r, outputs, fields, configSecrets)
	if err != nil {
		return nil, 0, deltaOK, "", err
	}

	inputs := map[string]interface{}{}
	if raw, ok := newState["inputs"].(map[string]interface{}); ok {
		for k, v := range raw {
			inputs[k] = v
		}
	}

	inputSecrets, err := resolveSecretInputs(r, inputs, fields, configSecrets)
	if err != nil {
		return nil, 0, deltaOK, "", err
	}
	secretsResolved += inputSecrets

	if err := resolveUnknownInputs(r, inputs, outputs); err != nil {
		return nil, 0, deltaOK, "", err
	}

	if _, ok := inputs[reservedDefaultsKey]; !ok {
		inputs[reservedDefaultsKey] = []interface{}{}
	}

	// Fills only properties Terraform has no opinion on ("region" on AWS is the
	// case that matters: per-resource in the Pulumi provider, provider-level in
	// Terraform, so it is never in the sidecar). A Terraform-derived value always
	// wins. Must run after resolveSecretInputs, so a copied secret carries the
	// envelope rather than a bare placeholder or bare plaintext.
	fillOutputsFromInputs(inputs, outputs)

	outcome, note := attachRawStateDelta(r, obj, outputs)

	if err := checkNoPlaceholders(r, "input", inputs, "inputs"); err != nil {
		return nil, 0, deltaOK, "", err
	}
	if err := checkNoPlaceholders(r, "output", outputs, "outputs"); err != nil {
		return nil, 0, deltaOK, "", err
	}

	obj["inputs"] = inputs
	obj["outputs"] = outputs

	return obj, secretsResolved, outcome, note, nil
}

func checkNoPlaceholders(r *NonImportableResource, kind string, v interface{}, path string) error {
	switch val := v.(type) {
	case string:
		if val == secretPlaceholder || val == redactedPlaceholder {
			return fmt.Errorf(
				"%s %q %s %s still contains the placeholder %q after secret resolution; "+
					"the sidecar or stack config is missing the real value",
				r.Type, r.Name, kind, path, val)
		}
		if val == unknownPlaceholder {
			return fmt.Errorf(
				"%s %q %s %s is the engine's unknown-value sentinel, which means the preview "+
					"could not resolve it — typically a reference to another resource this run "+
					"is injecting. Injecting it would write a placeholder into state that no "+
					"later operation can tell from a real value. Re-run \"patch-state\" after "+
					"the dependency is in state, or set the value by hand",
				r.Type, r.Name, kind, path)
		}
	case map[string]interface{}:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := checkNoPlaceholders(r, kind, val[k], path+"."+k); err != nil {
				return err
			}
		}
	case []interface{}:
		for i, elem := range val {
			if err := checkNoPlaceholders(r, kind, elem, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

const reservedDefaultsKey = "__defaults"

func fillOutputsFromInputs(inputs, outputs map[string]interface{}) {
	for k, v := range inputs {
		if isReservedOutputKey(k) {
			continue
		}
		if _, exists := outputs[k]; exists {
			continue
		}
		outputs[k] = v
	}
}

func attachRawStateDelta(r *NonImportableResource, obj, outputs map[string]interface{}) (deltaOutcome, string) {
	if r.SchemaVersion != 0 {
		if payload, ok := metaPayload(r.SchemaVersion); ok {
			outputs[metaKey] = payload
		}
	}

	if r.RawStateDelta == nil {
		if r.RawStateDeltaReason != "" {
			return deltaAbsent, fmt.Sprintf(
				"%s (%s %q): sidecar carried no raw-state delta: %s",
				r.TerraformAddress, r.Type, r.Name, r.RawStateDeltaReason)
		}
		return deltaAbsent, fmt.Sprintf(
			"%s (%s %q): sidecar carried no raw-state delta; check whether \"digest tf\" produced one",
			r.TerraformAddress, r.Type, r.Name)
	}

	deltaJSON, err := json.Marshal(r.RawStateDelta)
	if err == nil && bytes.Contains(deltaJSON, []byte(redactedPlaceholder)) {
		return deltaDroppedSensitive, fmt.Sprintf(
			"%s (%s %q): raw-state delta dropped, embedded an unresolvable %q placeholder",
			r.TerraformAddress, r.Type, r.Name, redactedPlaceholder)
	}

	outputs[rawStateDeltaKey] = r.RawStateDelta

	urn, _ := obj["urn"].(string)
	if err := validateRecover(urn, outputs); err != nil {
		delete(outputs, rawStateDeltaKey)
		return deltaDroppedUnrecoverable, fmt.Sprintf(
			"%s (%s %q): raw-state delta dropped, failed validation: %v",
			r.TerraformAddress, r.Type, r.Name, err)
	}

	// Enveloped only now, AFTER validation: validateRecover builds its
	// PropertyValue with deltaPropertyValue, which does not interpret Pulumi
	// sentinel maps, so it must see the plain form.
	outputs[rawStateDeltaKey] = envelopeReplaceNodes(r.RawStateDelta)
	return deltaOK, ""
}

func envelopeReplaceNodes(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		if _, isReplace := val["replace"]; isReplace {
			encoded, err := json.Marshal(val)
			if err != nil {
				return val
			}
			return map[string]interface{}{
				sigKey:      secretSig,
				"plaintext": string(encoded),
			}
		}
		out := make(map[string]interface{}, len(val))
		for k, elem := range val {
			out[k] = envelopeReplaceNodes(elem)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(val))
		for i, elem := range val {
			out[i] = envelopeReplaceNodes(elem)
		}
		return out
	default:
		return v
	}
}

func metaPayload(schemaVersion int64) (string, bool) {
	metaJSON, err := json.Marshal(map[string]interface{}{
		"schema_version": strconv.FormatInt(schemaVersion, 10),
	})
	if err != nil {
		return "", false
	}
	payload := string(metaJSON)
	if payload == `{"schema_version":"0"}` {
		return "", false
	}
	return payload, true
}

func resolveOutputSecrets(
	r *NonImportableResource,
	outputs map[string]interface{},
	fields map[string]*SchemaFieldInfo,
	configSecrets map[string]string,
) (int, error) {
	if len(r.RedactedAttributes) == 0 {
		return 0, nil
	}

	tfNames := make([]string, 0, len(r.RedactedAttributes))
	for tfName := range r.RedactedAttributes {
		tfNames = append(tfNames, tfName)
	}
	sort.Strings(tfNames)

	resolved := 0
	for _, tfName := range tfNames {
		configKey := r.RedactedAttributes[tfName]

		pulumiName := snakeToCamel(tfName)
		if fi, ok := fields[tfName]; ok && fi.PulumiName != "" {
			pulumiName = fi.PulumiName
		}

		val, ok := outputs[pulumiName]
		if !ok {
			continue
		}
		s, isString := val.(string)
		if !isString || s != redactedPlaceholder {
			continue
		}

		value, ok := configSecrets[configKey]
		if !ok || value == "" {
			return 0, fmt.Errorf(
				"%s %q output %q needs the secret in stack config key %q, which is not set; "+
					"pass --project-dir and --stack so it can be read",
				r.Type, r.Name, pulumiName, configKey)
		}

		encoded, err := json.Marshal(value)
		if err != nil {
			return 0, fmt.Errorf("encoding secret for %s: %w", configKey, err)
		}
		outputs[pulumiName] = map[string]interface{}{
			sigKey:      secretSig,
			"plaintext": string(encoded),
		}
		resolved++
	}
	return resolved, nil
}

func resolveSecretInputs(
	r *NonImportableResource,
	inputs map[string]interface{},
	fields map[string]*SchemaFieldInfo,
	configSecrets map[string]string,
) (int, error) {
	pulumiToTF := PulumiToTFNames(fields)
	resolved := 0

	names := make([]string, 0, len(inputs))
	for name := range inputs {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if s, ok := inputs[name].(string); !ok || s != secretPlaceholder {
			continue
		}

		tfName, schemaMapped := pulumiToTF[name]
		if !schemaMapped {
			tfName = tfbridge.PulumiToTerraformName(name, nil, nil)
		}
		configKey, ok := r.RedactedAttributes[tfName]
		if !ok {
			if r.Attributes == nil {
				return 0, fmt.Errorf(
					"%s %q input %q is a masked secret, but the sidecar records no Terraform "+
						"attributes for this resource, so there is no way to tell whether a real "+
						"secret is being lost; re-run \"resolve tf\" to regenerate the sidecar",
					r.Type, r.Name, name)
			}
			value, present := r.Attributes[tfName]
			if present {
				if value != nil {
					return 0, fmt.Errorf(
						"%s %q input %q is a masked secret but the sidecar records no config key "+
							"for Terraform attribute %q; re-run \"resolve tf\" or set the value "+
							"by hand",
						r.Type, r.Name, name, tfName)
				}
				delete(inputs, name)
				continue
			}
			if !schemaMapped {
				return 0, fmt.Errorf(
					"%s %q input %q is a masked secret and no provider schema was loaded to map "+
						"it to a Terraform attribute name; the fallback guess %q matches nothing "+
						"in the sidecar, which is indistinguishable from a wrong guess. Load the "+
						"provider (pass --project-dir/--stack so the digest's providers resolve) "+
						"or set the value by hand",
					r.Type, r.Name, name, tfName)
			}
			delete(inputs, name)
			continue
		}
		value, ok := configSecrets[configKey]
		if !ok || value == "" {
			return 0, fmt.Errorf(
				"%s %q input %q needs the secret in stack config key %q, which is not set; "+
					"pass --project-dir and --stack so it can be read",
				r.Type, r.Name, name, configKey)
		}

		encoded, err := json.Marshal(value)
		if err != nil {
			return 0, fmt.Errorf("encoding secret for %s: %w", configKey, err)
		}
		inputs[name] = map[string]interface{}{
			sigKey:      secretSig,
			"plaintext": string(encoded),
		}
		resolved++
	}
	return resolved, nil
}

// resolveUnknownInputs replaces inputs the preview reported as unknown with
// the value from this resource's Terraform-derived outputs. Reading it from
// there rather than chasing the dependency edge sidesteps what the edge cannot
// answer: a preview step records WHICH resources a property depends on, not
// which of their properties produced the value.
func resolveUnknownInputs(r *NonImportableResource, inputs, outputs map[string]interface{}) error {
	names := make([]string, 0, len(inputs))
	for name := range inputs {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		s, ok := inputs[name].(string)
		if !ok || s != unknownPlaceholder {
			continue
		}
		value, ok := outputs[name]
		if !ok || value == nil {
			continue
		}
		if sv, ok := value.(string); ok && sv == unknownPlaceholder {
			continue
		}
		inputs[name] = value
	}
	return nil
}

func orderInjected(objs []map[string]interface{}) {
	n := len(objs)
	index := make(map[string]int, n)
	for i, obj := range objs {
		if urn, ok := obj["urn"].(string); ok {
			index[urn] = i
		}
	}

	deps := make([][]int, n)
	for i, obj := range objs {
		add := func(urn string) {
			j, ok := index[urn]
			if !ok || j == i {
				return
			}
			for _, existing := range deps[i] {
				if existing == j {
					return
				}
			}
			deps[i] = append(deps[i], j)
		}
		if ds, ok := obj["dependencies"].([]interface{}); ok {
			for _, d := range ds {
				if s, ok := d.(string); ok {
					add(s)
				}
			}
		}
		if parent, ok := obj["parent"].(string); ok {
			add(parent)
		}
	}

	const (
		unvisited = iota
		visiting
		done
	)
	state := make([]uint8, n)
	order := make([]int, 0, n)
	var visit func(i int)
	visit = func(i int) {
		if state[i] != unvisited {
			return
		}
		state[i] = visiting
		for _, j := range deps[i] {
			visit(j)
		}
		state[i] = done
		order = append(order, i)
	}
	for i := 0; i < n; i++ {
		visit(i)
	}

	sorted := make([]map[string]interface{}, n)
	for pos, i := range order {
		sorted[pos] = objs[i]
	}
	copy(objs, sorted)
}
