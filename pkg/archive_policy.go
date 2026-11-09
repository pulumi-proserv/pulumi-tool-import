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
	archiveHashMissing archiveEvidence = iota
	archiveHashInvalid
	archiveHashMismatch
	archiveHashMatches
)

type archiveRejection int

const (
	archiveAccepted archiveRejection = iota
	archiveRejectInvalidHash
	archiveRejectMismatch
	archiveRejectConflict
)

type archiveState struct {
	InputIsArchive  bool
	OutputIsArchive bool
	InputMatches    bool
	OutputMatches   bool
}

type archiveDecision struct {
	Verified  bool
	Repair    bool
	Rejection archiveRejection
}

// decideArchiveRepair authorizes replacement from hash evidence and this field's state.
// The caller supplies evidence about the selected artifact, not merely its path.
//
// @ ensures decision.Verified == (evidence == archiveHashMatches)
// @ ensures decision.Repair == (evidence == archiveHashMatches && (!state.InputMatches || !state.OutputMatches))
// @ ensures evidence == archiveHashMatches ==> decision.Rejection == archiveAccepted
// @ ensures evidence == archiveHashMismatch ==> decision.Rejection == archiveRejectMismatch
// @ ensures evidence != archiveHashMissing && evidence != archiveHashMismatch && evidence != archiveHashMatches ==> decision.Rejection == archiveRejectInvalidHash
// @ ensures evidence == archiveHashMissing && ((state.InputIsArchive && !state.InputMatches) || (state.OutputIsArchive && !state.OutputMatches)) ==> decision.Rejection == archiveRejectConflict
// @ ensures evidence == archiveHashMissing && !(state.InputIsArchive && !state.InputMatches) && !(state.OutputIsArchive && !state.OutputMatches) ==> decision.Rejection == archiveAccepted
// @ ensures decision.Rejection != archiveAccepted ==> !decision.Verified && !decision.Repair
// @ decreases
func decideArchiveRepair(evidence archiveEvidence, state archiveState) (decision archiveDecision) {
	switch evidence {
	case archiveHashMatches:
		decision.Verified = true
		decision.Repair = !state.InputMatches || !state.OutputMatches
	case archiveHashMissing:
		if (state.InputIsArchive && !state.InputMatches) || (state.OutputIsArchive && !state.OutputMatches) {
			decision.Rejection = archiveRejectConflict
		}
	case archiveHashMismatch:
		decision.Rejection = archiveRejectMismatch
	default:
		decision.Rejection = archiveRejectInvalidHash
	}
	return decision
}

type outputRepairFacts struct {
	InputPatched       bool
	DigestPresent      bool
	OutputPresent      bool
	OutputEmpty        bool
	OutputBadPlain     bool
	OutputNullSentinel bool
	OutputMatches      bool
}

// needsOutputRepair decides whether this field's output needs a replacement candidate.
// Resource-wide patch history is deliberately absent from its inputs.
//
// @ ensures facts.OutputEmpty || facts.OutputBadPlain || facts.OutputNullSentinel ==> repair
// @ ensures !facts.OutputEmpty && !facts.OutputBadPlain && !facts.OutputNullSentinel ==> (repair == (facts.InputPatched && facts.DigestPresent && facts.OutputPresent && !facts.OutputMatches))
// @ ensures !facts.InputPatched && !facts.OutputEmpty && !facts.OutputBadPlain && !facts.OutputNullSentinel ==> !repair
// @ decreases
func needsOutputRepair(facts outputRepairFacts) (repair bool) {
	if facts.OutputEmpty || facts.OutputBadPlain || facts.OutputNullSentinel {
		return true
	}
	return facts.InputPatched && facts.DigestPresent && facts.OutputPresent && !facts.OutputMatches
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
