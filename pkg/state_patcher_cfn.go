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
	"encoding/json"
	"fmt"
	"strings"
)

// logicalIDFromName extracts the CFN logical ID from a Pulumi resource name,
// mirroring pkg/cfn's unexported suffix() helper: the last "-"-separated
// segment. State resource names produced by `resolve cfn` look like
// "<prefix>-<logicalId>".
func logicalIDFromName(name string) string {
	if i := strings.LastIndex(name, "-"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// StateLogicalIDsByType returns the set of CFN logical IDs (the last "-"-segment
// of each resource name) of custom resources in the exported state whose Pulumi
// type matches pulumiType (compared by short type token). patch-state cfn uses
// it to download Lambda code only for functions actually present in the migrated
// program, not every function in the digest.
func StateLogicalIDsByType(stateData []byte, pulumiType string) (map[string]bool, error) {
	var state map[string]interface{}
	dec := json.NewDecoder(strings.NewReader(string(stateData)))
	dec.UseNumber()
	if err := dec.Decode(&state); err != nil {
		return nil, fmt.Errorf("parsing state: %w", err)
	}
	ids := map[string]bool{}
	deployment, ok := state["deployment"].(map[string]interface{})
	if !ok {
		return ids, nil
	}
	resources, ok := deployment["resources"].([]interface{})
	if !ok {
		return ids, nil
	}
	want := shortPulumiType(pulumiType)
	for _, raw := range resources {
		rMap, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if custom, _ := rMap["custom"].(bool); !custom {
			continue
		}
		resType, _ := rMap["type"].(string)
		if shortPulumiType(resType) != want {
			continue
		}
		urn, _ := rMap["urn"].(string)
		ids[logicalIDFromName(urnName(urn))] = true
	}
	return ids, nil
}

// PatchStateFromCFN patches not_read fields from a CFN digest into imported
// state. It mirrors PatchState's resource-walking loop, but resolves values
// from a nameMapByLogicalID (built by the caller from a cfn.StackDigest)
// instead of a TF ModuleMap, and matches state resources to digest resources
// by the CFN logical ID embedded in the resource name (suffix after the last
// "-"), rather than by TF address mapping.
//
// Unlike PatchState (which maps Pulumi fields to TF snake_case attribute
// names), the CFN nameMap's Attributes are keyed directly by Pulumi field
// name — so each field descriptor's source-attribute name is the identity of
// its Pulumi field name.
//
// configSecrets is an optional map of config key → decrypted value, used to
// resolve sensitive fields. configDir is used to resolve asset file paths
// (e.g. pre-downloaded Lambda code zips) for fields with an asset type.
func PatchStateFromCFN(
	stateData []byte,
	nameMapByLogicalID map[string]*ModuleResource,
	fieldsFile *FieldsFile,
	configSecrets map[string]string,
	configDir string,
) ([]byte, *PatchStateResult, error) {
	// Parse state using a decoder with UseNumber to preserve exact number
	// representations. Without this, large integers (like AWS account IDs)
	// become float64 and may re-serialize as scientific notation (e.g.,
	// "5399223e-54"), which Pulumi's state parser rejects.
	var state map[string]interface{}
	dec := json.NewDecoder(strings.NewReader(string(stateData)))
	dec.UseNumber()
	if err := dec.Decode(&state); err != nil {
		return nil, nil, fmt.Errorf("parsing state: %w", err)
	}

	deployment, ok := state["deployment"].(map[string]interface{})
	if !ok {
		return nil, nil, fmt.Errorf("state missing deployment")
	}

	resourcesRaw, ok := deployment["resources"].([]interface{})
	if !ok {
		return nil, nil, fmt.Errorf("state missing resources")
	}

	notReadByType := buildNotReadByType(fieldsFile)
	providerVersions := buildProviderVersions(resourcesRaw)

	result := &PatchStateResult{DigestMapped: len(nameMapByLogicalID)}

	for i, raw := range resourcesRaw {
		rMap, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		custom, _ := rMap["custom"].(bool)
		if !custom {
			continue
		}
		urn, _ := rMap["urn"].(string)
		resType, _ := rMap["type"].(string)
		name := urnName(urn)

		st := shortPulumiType(resType)
		notReadFields, hasFields := notReadByType[st]
		if !hasFields {
			result.NoFields++
			continue
		}

		logicalID := logicalIDFromName(name)
		digResource := nameMapByLogicalID[logicalID]

		inputsRaw, _ := rMap["inputs"].(map[string]interface{})
		outputsRaw, _ := rMap["outputs"].(map[string]interface{})
		if inputsRaw == nil {
			inputsRaw = map[string]interface{}{}
		}
		if outputsRaw == nil {
			outputsRaw = map[string]interface{}{}
		}

		// Determine if this resource's provider suppresses falsy defaults.
		suppressFalsy := false
		if fieldsFile.FalsyDefaultSuppression != nil {
			pkgName := providerPackage(resType)
			if minVersion, ok := fieldsFile.FalsyDefaultSuppression[pkgName]; ok {
				providerRef, _ := rMap["provider"].(string)
				provVersion := lookupProviderVersion(providerRef, providerVersions)
				if provVersion != "" && semverAtLeast(provVersion, minVersion) {
					suppressFalsy = true
				}
			}
		}

		// Build field descriptors from the fields file. The CFN nameMap's
		// Attributes are keyed by Pulumi field name, so TFName is set to the
		// Pulumi field name itself (identity), unlike PatchState which maps
		// through pulumiToTFField.
		var fields []patchFieldDescriptor
		for pulumiField, meta := range notReadFields {
			suppressDefault := suppressFalsy && meta.Default != nil && isFalsyDefault(meta.Default)
			if suppressDefault {
				result.SkippedFalsySuppressed++
			}
			fields = append(fields, patchFieldDescriptor{
				PulumiName:              pulumiField,
				TFName:                  pulumiField,
				Default:                 meta.Default,
				HasDefault:              meta.Default != nil,
				SuppressDefaultFallback: suppressDefault,
				AssetType:               meta.Asset,
				AssetKind:               meta.AssetKind,
				ArchiveFormat:           meta.ArchiveFormat,
				HashField:               meta.HashField,
				// CFN Lambda code is authored as FileArchive("<fn>.zip"); emit the
				// matching file-based archive sentinel (not the TF embedded form).
				ZipAsFileArchive: true,
			})
		}

		patchResult, inputsRaw, outputsRaw, err := patchAndValidateResource(
			urn, name, fields, inputsRaw, outputsRaw, digResource, configSecrets, configDir,
		)
		if err != nil {
			return nil, nil, err
		}
		result.FieldsFromDigest += patchResult.fieldsFromDigest
		result.FieldsFromDefaults += patchResult.fieldsFromDefaults
		result.SkippedSensitive += patchResult.skippedSensitive
		if patchResult.patched {
			result.Patched++
		}
		if patchResult.deltaValidated {
			result.DeltaValidated++
		}
		if patchResult.deltaFailed {
			result.DeltaFailed++
		}
		if !patchResult.patched && digResource == nil {
			result.NoMatch++
		}

		rMap["inputs"] = inputsRaw
		rMap["outputs"] = outputsRaw
		resourcesRaw[i] = rMap
	}

	deployment["resources"] = resourcesRaw
	state["deployment"] = deployment

	out, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("marshaling patched state: %w", err)
	}

	return out, result, nil
}
