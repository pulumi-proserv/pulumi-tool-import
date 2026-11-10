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

package pkg

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassifyArchiveRelation(t *testing.T) {
	candidate := map[string]interface{}{sigKey: archiveSig, "path": "code.zip", "hash": "deployed"}
	for _, tc := range []struct {
		name     string
		existing interface{}
		want     archiveRelation
	}{
		{name: "absent", want: archiveAbsent},
		{name: "empty string", existing: "", want: archiveNonArchive},
		{name: "plain path", existing: "code.zip", want: archiveNonArchive},
		{name: "empty object", existing: map[string]interface{}{}, want: archiveNonArchive},
		{name: "unrecognized signature", existing: map[string]interface{}{sigKey: "other"}, want: archiveNonArchive},
		{name: "matching copy", existing: deepCopyJSONValue(candidate), want: archiveMatchesCandidate},
		{name: "stale hash", existing: map[string]interface{}{sigKey: archiveSig, "path": "code.zip", "hash": "stale"}, want: archiveConflictsWithCandidate},
		{name: "same hash different path", existing: map[string]interface{}{sigKey: archiveSig, "path": "other.zip", "hash": "deployed"}, want: archiveConflictsWithCandidate},
		{name: "file asset", existing: map[string]interface{}{sigKey: assetSig, "path": "code.zip", "hash": "deployed"}, want: archiveConflictsWithCandidate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classifyArchiveRelation(tc.existing, candidate))
		})
	}
	assert.Equal(t, archiveRelationInvalid, classifyArchiveRelation(nil, nil))
	assert.Equal(t, archiveRelationInvalid, classifyArchiveRelation(candidate, "code.zip"))
	asset := map[string]interface{}{sigKey: assetSig, "path": "code.zip", "hash": "deployed"}
	assert.Equal(t, archiveMatchesCandidate, classifyArchiveRelation(deepCopyJSONValue(asset), asset))
}

func TestClassifyOutputRelation(t *testing.T) {
	secret := map[string]interface{}{sigKey: secretSig, "plaintext": `"value"`}
	archive := map[string]interface{}{sigKey: archiveSig, "path": "code.zip"}
	for _, tc := range []struct {
		name      string
		existing  interface{}
		candidate interface{}
		want      outputRelation
	}{
		{name: "absent", candidate: "value", want: outputEmpty},
		{name: "both absent", want: outputEmpty},
		{name: "empty string", existing: "", candidate: "", want: outputEmpty},
		{name: "empty list", existing: []interface{}{}, candidate: "value", want: outputEmpty},
		{name: "empty object", existing: map[string]interface{}{}, candidate: "value", want: outputEmpty},
		{name: "false is populated", existing: false, candidate: true, want: outputDiffersFromCandidate},
		{name: "zero is populated", existing: 0, candidate: 1, want: outputDiffersFromCandidate},
		{name: "numeric equality", existing: json.Number("0"), candidate: 0, want: outputMatchesCandidate},
		{name: "matching scalar", existing: "value", candidate: "value", want: outputMatchesCandidate},
		{name: "different scalar", existing: "old", candidate: "value", want: outputDiffersFromCandidate},
		{name: "matching archive", existing: deepCopyJSONValue(archive), candidate: archive, want: outputMatchesCandidate},
		{name: "plain secret", existing: "value", candidate: secret, want: outputNeedsSecretRepair},
		{name: "null secret", existing: map[string]interface{}{sigKey: secretSig, "plaintext": "null"}, candidate: secret, want: outputNeedsSecretRepair},
		{name: "empty secret", existing: map[string]interface{}{sigKey: secretSig, "plaintext": `""`}, candidate: secret, want: outputNeedsSecretRepair},
		{name: "matching secret", existing: deepCopyJSONValue(secret), candidate: secret, want: outputMatchesCandidate},
		{name: "different secret", existing: map[string]interface{}{sigKey: secretSig, "plaintext": `"old"`}, candidate: secret, want: outputDiffersFromCandidate},
		{name: "null secret without secret candidate", existing: map[string]interface{}{sigKey: secretSig, "plaintext": "null"}, candidate: "value", want: outputDiffersFromCandidate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classifyOutputRelation(tc.existing, tc.candidate))
		})
	}
}
