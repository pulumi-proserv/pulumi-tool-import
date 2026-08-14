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
	"sort"
	"strconv"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg/providermap"
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge"
)

// secretPlaceholder is what "pulumi preview --json" substitutes for a secret
// input value (MassageSecrets in pulumi/pkg/v3/backend/display/json.go). It must
// never reach state.
const secretPlaceholder = "[secret]"

// rawStateDeltaKey and metaKey mirror the bridge's reserved output keys
// (reservedkeys.RawStateDelta, reservedkeys.Meta). Hardcoded here to match the
// rest of this package (see pkg/state_patcher.go), which does the same rather
// than importing the bridge's internal reservedkeys package.
const (
	rawStateDeltaKey = "__pulumi_raw_state_delta"
	metaKey          = "__meta"
)

// InjectResult reports what injection did, for the command's summary output.
type InjectResult struct {
	Injected        int
	SecretsResolved int
	// DeltaAbsentFromSidecar counts resources injected without a raw state
	// delta because the sidecar carried none in the first place — "digest tf"
	// never produced one. There is nothing here for patch-state to repair;
	// this points at "digest tf" itself.
	DeltaAbsentFromSidecar int
	// DeltaDroppedSensitive counts resources whose sidecar delta was dropped
	// because it embedded an unresolvable "(sensitive)" placeholder that
	// substituting the real secret into outputs would not fix.
	DeltaDroppedSensitive int
	// DeltaDroppedUnrecoverable counts resources whose sidecar delta was
	// dropped because it no longer validated (bridge Recover) against the
	// resource's outputs. DeltaDroppedNotes carries the Recover error for
	// each one.
	DeltaDroppedUnrecoverable int
	// DeltaAbsentNotes and DeltaDroppedNotes name, one line each, the
	// resources counted in DeltaAbsentFromSidecar and
	// DeltaDroppedUnrecoverable respectively — the former naming the
	// resource, the latter also carrying the Recover error, since that error
	// is the single most useful fact for deciding whether the delta could be
	// repaired.
	DeltaAbsentNotes  []string
	DeltaDroppedNotes []string
	URNs              []string
}

// InjectNonImportable appends the sidecar's resources to an exported deployment.
//
// Everything but the resource ID and outputs comes from the program: a preview
// reports these resources as creates, and each create's newState carries the
// URN, parent, provider reference, protect flag, inputs, and dependency edges
// the engine computed. Copying that is what makes injection correct without
// inferring anything from the deployment.
//
// InjectNonImportable does not load a Terraform provider. Everything
// provider-derived (Pulumi property names, output shapes, the raw state delta)
// was already computed by "digest tf" while a live provider was open, and
// travels in the sidecar; providers is used only to look up schema field
// metadata already loaded elsewhere in the command (attribute renaming), never
// to start a new provider.
func InjectNonImportable(
	stateData []byte,
	sidecar *NonImportableFile,
	preview *PreviewDigest,
	providers map[providermap.TerraformProviderName]*ProviderWithMetadata,
	configSecrets map[string]string,
) ([]byte, *InjectResult, error) {
	if sidecar == nil || len(sidecar.Resources) == 0 {
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

		newState, ok := creates[key]
		if !ok {
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
		case deltaAbsent:
			result.DeltaAbsentFromSidecar++
			result.DeltaAbsentNotes = append(result.DeltaAbsentNotes, note)
		case deltaDroppedSensitive:
			result.DeltaDroppedSensitive++
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

// deltaOutcome distinguishes why a resource ends up without a usable raw
// state delta (or that it has one) — see InjectResult's per-cause counters.
type deltaOutcome int

const (
	// deltaOK means the resource has a usable raw state delta attached.
	deltaOK deltaOutcome = iota
	// deltaAbsent means the sidecar carried no delta at all: "digest tf"
	// never produced one for this resource.
	deltaAbsent
	// deltaDroppedSensitive means the sidecar's delta embedded an
	// unresolvable "(sensitive)" placeholder and was dropped.
	deltaDroppedSensitive
	// deltaDroppedUnrecoverable means the sidecar's delta failed
	// validateRecover against the resource's outputs and was dropped.
	deltaDroppedUnrecoverable
)

// buildInjectedResource copies the preview's newState and fills in the parts
// only the sidecar knows. It returns the number of secret placeholders it
// resolved (across inputs and outputs), the raw-state-delta outcome, and — for
// deltaAbsent and deltaDroppedUnrecoverable — a one-line, resource-naming note
// explaining it (carrying the Recover error for the latter).
func buildInjectedResource(
	r *NonImportableResource,
	newState map[string]interface{},
	typeMap map[string]string,
	providers map[providermap.TerraformProviderName]*ProviderWithMetadata,
	configSecrets map[string]string,
) (map[string]interface{}, int, deltaOutcome, string, error) {
	obj := make(map[string]interface{}, len(newState)+3)
	for k, v := range newState {
		// The delta belongs to the sidecar, not the program's preview; a create
		// step from the engine never carries one anyway, but this guards
		// against copying a stray key verbatim.
		if k == rawStateDeltaKey {
			continue
		}
		obj[k] = v
	}
	obj["custom"] = true
	obj["id"] = r.ID

	// Field info lets attributes be renamed with the provider's own mapping
	// rather than a camelCase guess. Absent a loaded schema the fallback applies.
	var fields map[string]*SchemaFieldInfo
	if prov, tfType, found := LookupProviderForPulumiType(r.Type, typeMap, providers); found {
		fields = GetSchemaFieldInfo(prov, tfType)
	}

	// Outputs come from what "digest tf" computed with a live provider open
	// (Task 2b) when available; sidecars written before that change carry only
	// the raw Terraform attributes, which are renamed the same way an import
	// would rename them.
	var outputs map[string]interface{}
	if r.PulumiOutputs != nil {
		outputs = make(map[string]interface{}, len(r.PulumiOutputs))
		for k, v := range r.PulumiOutputs {
			outputs[k] = v
		}
	} else {
		outputs = MapTFAttributesToPulumi(r.Attributes, fields)
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

	// __defaults records which properties came from schema defaults. The engine's
	// Check usually supplies it already; only add it when missing, since an empty
	// list would otherwise discard what Check worked out.
	if _, ok := inputs[reservedDefaultsKey]; !ok {
		inputs[reservedDefaultsKey] = []interface{}{}
	}

	// A successfully created or imported resource's outputs are normally a
	// superset of its inputs. Injection builds outputs purely from Terraform
	// state (above), so any property the Pulumi provider models but
	// Terraform does not — "region" on the AWS provider is the case that
	// surfaced this: per-resource in the Pulumi provider (v7+, bridging
	// terraform-provider-aws v6+), but provider-level configuration in
	// Terraform AWS 5.x, so it never appears in the sidecar's attributes —
	// ends up missing from outputs. A missing output that the program's
	// inputs do carry is exactly what the next preview diffs against,
	// reporting a spurious "update". Fill any such gap from inputs, now that
	// inputs are fully built and its secrets are resolved and enveloped
	// (this must run after resolveSecretInputs so a copied secret carries
	// the envelope, never a bare placeholder or bare plaintext).
	fillOutputsFromInputs(inputs, outputs)

	// Run after the fill, not before: the delta was computed by "digest tf"
	// against the Terraform-derived outputs, and adding properties here
	// could in principle invalidate it. In practice Recover
	// (tfbridge.RawStateDelta.Recover, confirmed by reading
	// pkg/tfbridge/rawstate.go's Obj case) walks the *outputs* object and
	// looks up each key's delta by name, defaulting to the zero
	// RawStateDelta{} for any key it has never seen — which recovers via
	// the natural encoding rather than failing — so a plain scalar like
	// "region" does not by itself break recovery. But that is not a
	// guarantee for every possible filled value, so the fill still runs
	// before this validation rather than after: if some future case does
	// break Recover, the existing drop-the-delta path below (validateRecover
	// fails -> delete the delta, report deltaDroppedUnrecoverable) is what
	// catches it, instead of writing a delta that only fails at the next
	// preview.
	outcome, note := attachRawStateDelta(r, obj, outputs)

	// Backstop: the targeted resolution above only catches a placeholder where
	// it can correctly map a property name back to a redactedAttributes entry.
	// "[secret]" can appear nested inside an array- or object-valued input
	// (massagePropertyValue, pulumi/pkg/v3/backend/display/json.go, recurses
	// into them), and "(sensitive)" can survive in outputs whenever the
	// Pulumi name this code derives for a redacted attribute does not match
	// the name the placeholder actually landed under. Either placeholder
	// reaching state is worse than failing the whole injection, so this walks
	// both property bags after every targeted fix has had its chance and
	// hard-errors on anything left.
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

// checkNoPlaceholders recursively walks a property value — including nested
// objects and arrays, which is exactly what the targeted resolvers above do
// not do — and returns an error naming the resource and the property path if
// it finds the literal "[secret]" or "(sensitive)" anywhere. It is the
// backstop for targeted resolution, not a replacement for it: it does not
// depend on any name mapping being correct.
func checkNoPlaceholders(r *NonImportableResource, kind string, v interface{}, path string) error {
	switch val := v.(type) {
	case string:
		if val == secretPlaceholder || val == redactedPlaceholder {
			return fmt.Errorf(
				"%s %q %s %s still contains the placeholder %q after secret resolution; "+
					"the sidecar or stack config is missing the real value",
				r.Type, r.Name, kind, path, val)
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

// fillOutputsFromInputs copies any property present in inputs but absent from
// outputs, taking the value from inputs. It never overwrites a property
// outputs already has — the Terraform-derived value is authoritative for
// anything Terraform has an opinion on; this only fills in what Terraform has
// no opinion on at all.
//
// Skips the bridge's three reserved keys (mirroring
// reservedkeys.IsBridgeReservedKey): __defaults belongs to inputs only (it
// records which inputs came from schema defaults, meaningless as an output),
// while __meta and __pulumi_raw_state_delta are outputs-only bookkeeping that
// attachRawStateDelta owns and that inputs never legitimately carries anyway.
//
// A value copied from inputs is copied as-is, including a resolved secret's
// envelope map ({sig, "plaintext": ...}, see resolveSecretInputs): by the
// time this runs, resolveSecretInputs has already replaced every "[secret]"
// placeholder in inputs with that envelope, so there is nothing left to
// unwrap or re-wrap here — copying the map verbatim keeps it wrapped.
func fillOutputsFromInputs(inputs, outputs map[string]interface{}) {
	for k, v := range inputs {
		if k == reservedDefaultsKey || k == metaKey || k == rawStateDeltaKey {
			continue
		}
		if _, exists := outputs[k]; exists {
			continue
		}
		outputs[k] = v
	}
}

// attachRawStateDelta adds the sidecar's raw state delta and __meta to outputs
// when they are usable.
//
// A delta computed from redacted attributes can embed the literal string
// "(sensitive)" in its raw JSON, where the bridge fell back to a Replace node.
// Substituting the real secret into outputs does not change what such a node
// recovers, so a delta like that is dropped rather than repaired: writing it
// would reconstruct Terraform state containing the placeholder. Whatever
// survives that check is then validated with the bridge's own Recover, which
// catches a delta that no longer applies to these outputs for any other
// reason (validateRecover, pkg/state_patcher.go).
// attachRawStateDelta returns the resulting deltaOutcome and, for deltaAbsent
// and deltaDroppedUnrecoverable, a one-line note identifying the resource
// (and, for the latter, carrying the Recover error) for the command's
// per-resource summary output.
func attachRawStateDelta(r *NonImportableResource, obj, outputs map[string]interface{}) (deltaOutcome, string) {
	// __meta records the provider's schema version independently of whether a
	// raw state delta exists: ComputeInjectionState (pkg/raw_state_delta.go)
	// can report a schema version even when it could not compute a delta at
	// all, and a later provider upgrade still needs that version to run the
	// right state upgraders.
	if r.SchemaVersion != 0 {
		if payload, ok := metaPayload(r.SchemaVersion); ok {
			outputs[metaKey] = payload
		}
	}

	if r.RawStateDelta == nil {
		return deltaAbsent, fmt.Sprintf(
			"%s (%s %q): sidecar carried no raw-state delta; check whether \"digest tf\" produced one",
			r.TerraformAddress, r.Type, r.Name)
	}

	// Checked unconditionally, not just when redactedAttributes is non-empty:
	// a delta can embed "(sensitive)" from a nested path the digest never
	// recorded there (redactedAttributeKeys only tracks top-level attributes),
	// and the placeholder must never survive by any route.
	deltaJSON, err := json.Marshal(r.RawStateDelta)
	if err == nil && bytes.Contains(deltaJSON, []byte(redactedPlaceholder)) {
		return deltaDroppedSensitive, ""
	}

	outputs[rawStateDeltaKey] = r.RawStateDelta

	urn, _ := obj["urn"].(string)
	if err := validateRecover(urn, outputs); err != nil {
		delete(outputs, rawStateDeltaKey)
		return deltaDroppedUnrecoverable, fmt.Sprintf(
			"%s (%s %q): raw-state delta dropped, failed validation: %v",
			r.TerraformAddress, r.Type, r.Name, err)
	}
	return deltaOK, ""
}

// metaPayload builds the bridge's __meta JSON string from a schema version,
// mirroring tfbridge.MakeTerraformResult: the default "schema_version: 0"
// payload is omitted, since a later provider needs no upgrade from that.
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

// resolveOutputSecrets substitutes each redacted attribute's real value into
// the resource's outputs.
//
// redactedAttributes maps a Terraform attribute name to the stack config key
// holding its real value, while outputs is keyed by Pulumi property name — the
// digest computed it from the same redacted attributes, so the placeholder
// lands there under the Pulumi name. An unresolvable placeholder is a hard
// error, the same as for inputs: writing "(sensitive)" into state would be
// worse than refusing to write anything.
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

		// Wrapped in Pulumi's secret envelope, the same as resolveSecretInputs
		// does for inputs: the design's Sensitive values section says outputs
		// get the envelope too. This is a known secret being deliberately
		// reintroduced (a value redacted by "digest tf" and resolved back out
		// of stack config), unlike patch-state's own outputsRaw assignment
		// (state_patcher.go, patchAndValidateResource) which unwraps to a bare
		// value — that path is patching an incidental output the API already
		// returned, not reinstating a secret on purpose. Leaving this one bare
		// would write a VPN pre-shared key, or similar, into the deployment's
		// outputs in plaintext and unmarked in the state backend.
		encoded, err := json.Marshal(value)
		if err != nil {
			return 0, fmt.Errorf("encoding secret for %s: %w", configKey, err)
		}
		outputs[pulumiName] = map[string]interface{}{
			sigKey:      "1b47061264138c4ac30d75fd1eb44270",
			"plaintext": string(encoded),
		}
		resolved++
	}
	return resolved, nil
}

// resolveSecretInputs replaces "[secret]" placeholders with the real value from
// stack config, wrapped in Pulumi's secret envelope. An unresolvable placeholder
// is a hard error: writing the placeholder would put a known-wrong value into
// state, which is worse than refusing to write anything.
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

		tfName, ok := pulumiToTF[name]
		if !ok {
			// No loaded schema (or the schema does not describe this field):
			// fall back to the bridge's own generic PascalCase/camelCase to
			// underscore_case conversion, the same transform it uses when it
			// has no schema-derived name to prefer.
			tfName = tfbridge.PulumiToTerraformName(name, nil, nil)
		}
		configKey, ok := r.RedactedAttributes[tfName]
		if !ok {
			return 0, fmt.Errorf(
				"%s %q input %q is a masked secret but the sidecar records no config key for "+
					"Terraform attribute %q; re-run \"resolve tf\" or set the value by hand",
				r.Type, r.Name, name, tfName)
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
			sigKey:      "1b47061264138c4ac30d75fd1eb44270",
			"plaintext": string(encoded),
		}
		resolved++
	}
	return resolved, nil
}

// orderInjected sorts injected resources so that one depending on another
// comes after it. VerifyIntegrity rejects a resource whose parent or
// dependency appears later in the array.
//
// Only edges between two resources in this same batch matter: a reference to
// a resource already in the deployment is necessarily earlier in the array
// already, and needs no reordering here.
//
// Only "dependencies" and "parent" are consulted — not "propertyDependencies"
// or "deletedWith", which the same create step can also carry. This is safe
// so long as "dependencies" is a superset of them, which it normally is: the
// engine populates "dependencies" as the union of every edge a resource
// carries (parent, property-level, and deletedWith all get folded into it),
// so a missed propertyDependencies-only or deletedWith-only edge that is not
// already covered by "dependencies" would be unusual. If a batch ever
// contains an edge that "dependencies" misses, this function will not
// reorder for it — but that does not fail silently: VerifyIntegrity
// (VerifyDeploymentIntegrity, called at the end of InjectNonImportable)
// rejects a forward reference loudly, so an ordering gap surfaces as a hard
// error here rather than as corrupted state discovered later.
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

	// DFS post-order topological sort: a node is appended to the order only
	// after everything it depends on, and visiting in original order keeps
	// unrelated resources in the sidecar's own order.
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
