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

type archiveEvidence int

const (
	archiveHashInvalid archiveEvidence = iota
	archiveHashMissing
	archiveHashMismatch
	archiveHashMatches
)

type archiveRelation int

const (
	archiveRelationInvalid archiveRelation = iota
	archiveAbsent
	archiveNonArchive
	archiveMatchesCandidate
	archiveConflictsWithCandidate
)

type archiveRepairFacts struct {
	Evidence archiveEvidence
	Input    archiveRelation
	Output   archiveRelation
}

type archiveAction int

const (
	archiveRejectInvalidState archiveAction = iota
	archiveRejectInvalidEvidence
	archiveRejectHashMismatch
	archiveRejectUnverifiedConflict
	archiveAllowUnverifiedReconstruction
	archiveKeepVerified
	archiveRepairVerified
)

// decideArchiveRepair authorizes replacement from hash evidence and this field's state.
//
// @ ensures archiveRejectInvalidState <= action && action <= archiveRepairVerified
// @ ensures (action == archiveRejectInvalidState) == (facts.Input < archiveAbsent || facts.Input > archiveConflictsWithCandidate || facts.Output < archiveAbsent || facts.Output > archiveConflictsWithCandidate)
// @ ensures action != archiveRejectInvalidState ==> ((action == archiveRejectInvalidEvidence) == (facts.Evidence != archiveHashMissing && facts.Evidence != archiveHashMismatch && facts.Evidence != archiveHashMatches))
// @ ensures action != archiveRejectInvalidState ==> ((action == archiveRejectHashMismatch) == (facts.Evidence == archiveHashMismatch))
// @ ensures action != archiveRejectInvalidState ==> ((action == archiveRejectUnverifiedConflict) == (facts.Evidence == archiveHashMissing && (facts.Input == archiveConflictsWithCandidate || facts.Output == archiveConflictsWithCandidate)))
// @ ensures action != archiveRejectInvalidState ==> ((action == archiveAllowUnverifiedReconstruction) == (facts.Evidence == archiveHashMissing && facts.Input != archiveConflictsWithCandidate && facts.Output != archiveConflictsWithCandidate))
// @ ensures action != archiveRejectInvalidState ==> ((action == archiveKeepVerified) == (facts.Evidence == archiveHashMatches && facts.Input == archiveMatchesCandidate && facts.Output == archiveMatchesCandidate))
// @ ensures action != archiveRejectInvalidState ==> ((action == archiveRepairVerified) == (facts.Evidence == archiveHashMatches && (facts.Input != archiveMatchesCandidate || facts.Output != archiveMatchesCandidate)))
// @ decreases
func decideArchiveRepair(facts archiveRepairFacts) (action archiveAction) {
	if facts.Input < archiveAbsent || facts.Input > archiveConflictsWithCandidate ||
		facts.Output < archiveAbsent || facts.Output > archiveConflictsWithCandidate {
		return archiveRejectInvalidState
	}
	switch facts.Evidence {
	case archiveHashMatches:
		if facts.Input == archiveMatchesCandidate && facts.Output == archiveMatchesCandidate {
			return archiveKeepVerified
		}
		return archiveRepairVerified
	case archiveHashMissing:
		if facts.Input == archiveConflictsWithCandidate || facts.Output == archiveConflictsWithCandidate {
			return archiveRejectUnverifiedConflict
		}
		return archiveAllowUnverifiedReconstruction
	case archiveHashMismatch:
		return archiveRejectHashMismatch
	default:
		return archiveRejectInvalidEvidence
	}
}

type outputRelation int

const (
	outputRelationInvalid outputRelation = iota
	outputEmpty
	outputNeedsSecretRepair
	outputMatchesCandidate
	outputDiffersFromCandidate
)

type outputAction int

const (
	outputRejectInvalidState outputAction = iota
	outputKeep
	outputConsiderRepair
)

// decideOutputRepair selects a replacement candidate using only this field's input change.
//
// @ ensures outputRejectInvalidState <= action && action <= outputConsiderRepair
// @ ensures (action == outputRejectInvalidState) == (relation < outputEmpty || relation > outputDiffersFromCandidate)
// @ ensures (action == outputConsiderRepair) == (relation == outputEmpty || relation == outputNeedsSecretRepair || (relation == outputDiffersFromCandidate && inputPatched && digestPresent))
// @ ensures relation == outputMatchesCandidate ==> action == outputKeep
// @ ensures relation == outputDiffersFromCandidate && (!inputPatched || !digestPresent) ==> action == outputKeep
// @ decreases
func decideOutputRepair(inputPatched, digestPresent bool, relation outputRelation) (action outputAction) {
	switch relation {
	case outputEmpty, outputNeedsSecretRepair:
		return outputConsiderRepair
	case outputMatchesCandidate:
		return outputKeep
	case outputDiffersFromCandidate:
		if inputPatched && digestPresent {
			return outputConsiderRepair
		}
		return outputKeep
	default:
		return outputRejectInvalidState
	}
}

// fieldPatchEffects includes output-only changes in patch reporting and asset validation selection.
//
// @ ensures changed == (inputPatched || outputPatched)
// @ ensures recordAsset == (changed && assetCandidate && hasAssetKind)
// @ ensures outputPatched ==> changed
// @ ensures outputPatched && assetCandidate && hasAssetKind ==> recordAsset
// @ decreases
func fieldPatchEffects(inputPatched, outputPatched, assetCandidate, hasAssetKind bool) (changed, recordAsset bool) {
	changed = inputPatched || outputPatched
	recordAsset = changed && assetCandidate && hasAssetKind
	return changed, recordAsset
}
