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
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

func classifyArchiveEvidence(expected string, candidate map[string]interface{}) archiveEvidence {
	if expected == "" {
		return archiveHashMissing
	}
	decoded, err := base64.StdEncoding.DecodeString(expected)
	if err != nil || len(decoded) != sha256.Size {
		return archiveHashInvalid
	}
	if candidate["hash"] != hex.EncodeToString(decoded) {
		return archiveHashMismatch
	}
	return archiveHashMatches
}

func classifyArchiveRelation(existing, candidate interface{}) archiveRelation {
	if !isAssetOrArchiveSentinel(candidate) {
		return archiveRelationInvalid
	}
	if existing == nil {
		return archiveAbsent
	}
	// Preserve the patcher's treatment of both asset and archive sentinels.
	if !isAssetOrArchiveSentinel(existing) {
		return archiveNonArchive
	}
	if equalValues(existing, candidate) {
		return archiveMatchesCandidate
	}
	return archiveConflictsWithCandidate
}

func classifyOutputRelation(existing, candidate interface{}) outputRelation {
	if isEmptyValue(existing) {
		return outputEmpty
	}
	if isSecretSentinel(candidate) && (!isSecretSentinel(existing) || isNullSentinel(existing)) {
		return outputNeedsSecretRepair
	}
	if equalValues(existing, candidate) {
		return outputMatchesCandidate
	}
	return outputDiffersFromCandidate
}
