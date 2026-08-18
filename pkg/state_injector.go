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

// unknownPlaceholder is how the engine serializes a value that is not yet known
// at preview time (plugin.UnknownStringValue; the engine writes it out via
// pulumi/pkg/v3 resource/stack/deployment.go, and preview steps are built with
// stack.SerializeResource).
//
// It reaches injection because an injected resource can depend on another
// injected resource: at the preview that drives injection the dependency is not
// in state yet, so its outputs are unknown and the dependent's create-step
// inputs carry this sentinel. The e2e fixture does exactly this — a policy
// attachment referencing a certificate, both non-importable. orderInjected
// exists precisely because those edges occur, so the shape is expected rather
// than exotic; what is not acceptable is copying the sentinel into state, where
// it becomes an ordinary-looking string that no later operation can distinguish
// from a real value.
const unknownPlaceholder = "04da6b54-80e4-46f7-96ec-b56ff0331ba9"

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
	// DeltaAttached counts injected resources that ended up WITH a usable raw
	// state delta.
	//
	// Reported positively, rather than left to be inferred from the three
	// absence counters below all being zero, because those two readings are
	// not the same: "every injected resource has a delta" and "nothing went
	// wrong enough to be counted" look identical when the totals are zero, and
	// only the first is a statement about the artifact.
	//
	// It is also deliberately distinct from PatchStateResult.DeltaValidated,
	// which counts PATCHED resources — those were imported, and their deltas
	// were written by the bridge itself during "pulumi import" (it calls
	// RawStateInjectDelta from Create, Read and Update). Injected resources
	// never go through the provider at all, so nothing would produce a delta
	// for them and this tool computes it in "digest tf". Two populations, two
	// producers, two counters.
	DeltaAttached int
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
	// DeltaAbsentNotes, DeltaDroppedNotes, and DeltaDroppedSensitiveNotes name,
	// one line each, the resources counted in DeltaAbsentFromSidecar,
	// DeltaDroppedUnrecoverable, and DeltaDroppedSensitive respectively — the
	// first naming the resource, the second also carrying the Recover error
	// (the single most useful fact for deciding whether the delta could be
	// repaired), the third naming the resource so a reader can go find which
	// field carried the unresolvable "(sensitive)" placeholder.
	DeltaAbsentNotes           []string
	DeltaDroppedNotes          []string
	DeltaDroppedSensitiveNotes []string
	URNs                       []string
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
		// Verify even though nothing was injected. The caller's fallback check
		// is guarded by "injectResult == nil", and this path returns a NON-nil
		// (empty) result, so returning early skipped both this function's own
		// verification AND the caller's — the only path through patch-state
		// that verified nothing at all, after which stack mode imports the
		// state into the live stack. Reachable from a sidecar with
		// "resources": [], which is what a run that found nothing writes.
		//
		// Verifying here keeps the contract this function's callers rely on:
		// the bytes it returns have been checked, whether or not it changed
		// them.
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
// deltaAbsent, deltaDroppedUnrecoverable, and deltaDroppedSensitive — a
// one-line, resource-naming note explaining it (carrying the Recover error
// for deltaDroppedUnrecoverable).
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
	// A custom resource with no ID passes every check this tool makes and then
	// panics the engine on the NEXT operation: VerifyDeploymentIntegrity only
	// rejects the inverse case (a non-custom resource that HAS an ID), and the
	// provider plugin asserts on it — contract.Assertf(req.ID != "", "Diff
	// requires an ID"). That is reachable, not theoretical: some resource types
	// are simultaneously id-less (plugin-framework types with no implicit "id"
	// attribute) and non-importable, which is exactly the population injected
	// here. In file mode nothing previews the result, so the corrupt state is
	// simply written out.
	if r.ID == "" {
		return nil, 0, deltaOK, "", fmt.Errorf(
			"%s %q has no import ID: the sidecar records no top-level \"id\" attribute for it, "+
				"and a custom resource without an ID corrupts the deployment — the next "+
				"operation against this stack fails inside the engine rather than reporting "+
				"the missing value. Set the resource's ID in the sidecar by hand, or exclude it",
			r.Type, r.Name)
	}
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

	// Resolve any input the preview could not evaluate. Must run BEFORE
	// checkNoPlaceholders, which is what turns an unresolved one into an error.
	if err := resolveUnknownInputs(r, inputs, outputs); err != nil {
		return nil, 0, deltaOK, "", err
	}

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
//
// Two things this function does not check, deliberately:
//
//  1. It fills unconditionally, with no check against the target provider's
//     Terraform schema. An input with no corresponding Terraform attribute in
//     that schema would still be copied into outputs and emitted into raw
//     state by Recover — and only rejected later, when ctyjson.Unmarshal
//     decodes that raw state back into a cty value. validateRecover does not
//     catch this: Recover itself succeeds, since it does not consult the
//     schema either; only the read path's decode would fail. The backstop
//     for that case is the verify-and-revert preview after injection, not
//     any local check here. This function trusts that a copied input is
//     schema-backed.
//  2. Copied inputs are screened for the secret placeholders ("[secret]",
//     "(sensitive)", via checkNoPlaceholders after this runs) but not for the
//     engine's unknown-value sentinel. That is not reachable in the current
//     flow — non-importable resources by construction only reference
//     resources that have already been imported, so their inputs cannot
//     carry an unresolved unknown — but it is an assumption of this
//     function, not something it enforces.
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
// attachRawStateDelta returns the resulting deltaOutcome and, for deltaAbsent,
// deltaDroppedUnrecoverable, and deltaDroppedSensitive, a one-line note
// identifying the resource (and, for deltaDroppedUnrecoverable, also carrying
// the Recover error) for the command's per-resource summary output.
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
		if r.RawStateDeltaReason != "" {
			return deltaAbsent, fmt.Sprintf(
				"%s (%s %q): sidecar carried no raw-state delta: %s",
				r.TerraformAddress, r.Type, r.Name, r.RawStateDeltaReason)
		}
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
	// PropertyValue with deltaPropertyValue, which deliberately does not
	// interpret Pulumi sentinel maps, so it must see the plain form. What gets
	// persisted is the enveloped one.
	outputs[rawStateDeltaKey] = envelopeReplaceNodes(r.RawStateDelta)
	return deltaOK, ""
}

// envelopeReplaceNodes wraps every Replace node in a raw state delta in the
// Pulumi secret envelope, so the engine encrypts it at rest.
//
// This is what the bridge does for the deltas it writes itself: Marshal()
// returns a PropertyValue in which any map carrying a "replace" key has been
// wrapped in resource.MakeSecret (rawstate.go), and the engine then persists
// that as ciphertext. Injection cannot store a PropertyValue — a sidecar is
// JSON — so the equivalent has to be re-applied here, or the same payload ends
// up encrypted when the bridge writes it and plaintext when we do.
//
// It round-trips: UnmarshalRawStateDelta calls propertyvalue.RemoveSecrets on
// the value before decoding, so a secreted node is unwrapped on read. That is
// not incidental — it is the bridge accommodating its own Marshal.
//
// A Replace node carries the provider's verbatim raw state for an attribute,
// which is why the bridge treats it as sensitive: it is the one part of a delta
// that can hold real values rather than structural information.
func envelopeReplaceNodes(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		// Mirrors the bridge's own test — a map carrying "replace", checked
		// before recursing so a nested Replace inside one is not double-wrapped.
		if _, isReplace := val["replace"]; isReplace {
			encoded, err := json.Marshal(val)
			if err != nil {
				// Unreachable in practice: this value was decoded from the
				// sidecar's JSON moments ago. Returning it unchanged keeps the
				// delta correct and merely unencrypted, which is what the
				// previous behaviour was for every node.
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
			sigKey:      secretSig,
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

		tfName, schemaMapped := pulumiToTF[name]
		if !schemaMapped {
			// No loaded schema (or the schema does not describe this field):
			// fall back to the bridge's own generic PascalCase/camelCase to
			// underscore_case conversion, the same transform it uses when it
			// has no schema-derived name to prefer.
			tfName = tfbridge.PulumiToTerraformName(name, nil, nil)
		}
		configKey, ok := r.RedactedAttributes[tfName]
		if !ok {
			// A property the Pulumi provider marks secret in its schema is
			// masked by "pulumi preview --json" whether or not it holds a
			// value, while the Terraform side is value-driven: the digest's
			// redactSensitivePaths skips a null attribute, so no config key
			// exists for one. When Terraform has no value either, there is no
			// secret to recover and nothing was lost — drop the property.
			// Leaving the literal "[secret]" in inputs would write a
			// known-wrong value into state.
			//
			// This is deliberately narrow. If Terraform DOES have a value for
			// the attribute, a missing config key means the digest and the
			// sidecar genuinely disagree, and dropping it would silently
			// discard a real secret — so that stays a hard error below.
			//
			// "Terraform has no value" is only a conclusion the sidecar can
			// support. With no Attributes at all there is nothing to consult,
			// so every masked input would look valueless and be dropped.
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
				// Present and explicitly null — Terraform genuinely holds no
				// value, so there is no secret to recover. Finding the key at
				// all corroborates tfName even when it was guessed, which is
				// what makes dropping safe here.
				delete(inputs, name)
				continue
			}
			// tfName matches nothing in the sidecar. That is the write-only
			// case ONLY if tfName is trustworthy. When it came from
			// PulumiToTerraformName rather than the provider schema it is a
			// guess, and a wrong guess produces exactly this — the bridge
			// pluralises list and set names and that transform cannot invert
			// them, so hundreds of real properties do not round-trip. Dropping
			// here would silently delete a program-declared input.
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
// the resource's real value, taken from its Terraform-derived outputs.
//
// An injected resource may reference another injected resource — the fixture's
// IoT policy attachment takes its target from a certificate that is also
// non-importable — and at the preview that drives injection the dependency is
// not in state yet, so the engine serializes the referring input as its
// unknown sentinel. Injecting that verbatim writes a placeholder into state
// that nothing downstream can distinguish from a real value.
//
// The value is nonetheless knowable, and from the most authoritative source
// available: Terraform already created both resources, so the dependency's
// real value is recorded in the sidecar as this resource's own output. That is
// the same substitution fillOutputsFromInputs performs in the other direction,
// and it is why the sidecar carries outputs at all.
//
// Falling back to the output rather than chasing the dependency edge also
// sidesteps a problem the edge cannot solve: a preview step records WHICH
// resources a property depends on, but not which of their properties produced
// the value, so "target came from the certificate" does not say whether it was
// the certificate's arn or its id.
//
// An unknown with no corresponding output stays unresolved, and
// checkNoPlaceholders then rejects it. That is the correct stop: it means the
// program references something Terraform has no record of, so injection would
// be inventing the value.
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
			// Left in place deliberately: checkNoPlaceholders reports it with
			// the full path, which is more useful than a second error here.
			continue
		}
		if sv, ok := value.(string); ok && sv == unknownPlaceholder {
			continue
		}
		inputs[name] = value
	}
	return nil
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
