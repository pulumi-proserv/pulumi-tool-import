# Archive policy verification

## Design

The archive patcher uses three decision functions in `pkg/archive_policy.go`.
Gobra verifies that same Go source file. Its `// @` comments hold the contracts;
the Go compiler ignores those comments. There is no separate implementation or generated copy.

The functions use scalar values and value structs. They have no imports, heap mutations,
I/O, or trusted declarations. This keeps the first proof independent of the Pulumi SDK
and the rest of the Go module. Gobra checks all three function bodies and termination.
Archive inputs have named states: absent, non-archive, matching candidate, or conflicting candidate.
The archive decision returns one action, so it cannot simultaneously authorize repair and report rejection.
Output classification similarly distinguishes empty values, secret-repair cases, matches, and differences.
The zero state is invalid. The proof requires rejection of invalid or unrecognized state and evidence values.
The remaining Boolean inputs describe independent facts, such as whether this field's input changed.
The proof covers every value of those inputs without preconditions.

| Function | Contract |
|---|---|
| `decideArchiveRepair` | Reject invalid states and hash evidence. Reject conflicting existing archives without hash evidence. Authorize stale-archive repair only with matching evidence. |
| `decideOutputRepair` | Reject invalid output states. Existing populated outputs need a changed input for this same field before digest differences can authorize replacement. Empty and secret-repair states remain repair candidates. |
| `fieldPatchEffects` | Count input-only and output-only changes. Select changed asset fields with asset-kind metadata for subsequent delta handling. |

The classifiers in `pkg/archive_policy_inputs.go` translate actual resource values into these states.
`patchResourceFields` classifies input and output separately against the same candidate, then applies the selected action.
Hash decoding distinguishes missing, malformed, mismatching, and matching evidence. Matching inputs
and outputs do not request archive repair. Missing hashes still permit reconstruction
when no conflicting archive exists, as in PR #122.

Patch reporting accumulates across fields, but output-repair authorization does not.
The decision function accepts only the current field's `inputPatched` value.
Both input and output changes contribute to asset metadata selection.

Classification preserves the existing patcher semantics. A nil value is absent;
an empty string is a non-archive value for archive comparison. For output repair,
nil, empty strings, empty lists, and empty maps are empty, while zero and false are populated.
Asset sentinels receive the same archive-comparison treatment as archive sentinels.
These classifications describe repair policy, not complete validation of serialized resource values.

Named types prevent contradictory flags, but cannot detect incorrect classification.
Adapter tests cover real sentinel shapes, secret wrappers, and asymmetric input/output states.
The classifiers and their calls remain outside the Gobra proof.

## Run the proof

Install Python 3 and Podman. On macOS, initialize and start a Podman machine first.
The VM must support the upstream image's `linux/amd64` architecture.

```sh
make verify-archive-policy
make verify-archive-policy-mutations
```

An alternative OCI runtime can use the same pinned image:

```sh
CONTAINER_RUNTIME=docker make verify-archive-policy
```

`CONTAINER_RUNTIME` names one executable, not a shell command with arguments.
The runner pins Gobra v26.09 by image digest, including Java and Z3.
It mounts source read-only and disables container networking during verification.
The runtime needs network access for the initial image pull.
Proof reports stay in a temporary container filesystem.

The mutation target first requires the production proof to pass. It then changes
implementation statements in temporary copies while keeping their contracts fixed.
Each copy must fail a postcondition. A parser error, timeout, or container failure
does not count as a rejected mutation. Temporary copies are removed afterward.

The ten mutations check missing-hash trust, hash mismatch acceptance, unverified
replacement, unnecessary repair, missing field-local authorization, lost output-only
reporting, missing output-only asset validation selection, and acceptance of invalid
archive states, output states, or evidence values.

CI runs both the proof and mutations on pull requests and pushes to `main`.
The normal Go tests also exercise the production adapters with real archive sentinels.

## Proof boundary

This is a proof of repair decisions, not the complete archive reconciliation pipeline.
The adapters must classify evidence accurately and supply facts for the correct field.
The proof does not verify those adapters, `equalValues`, sentinel recognition, JSON,
ZIP parsing, SHA-256, provider behavior, or filesystem operations.

Matching evidence means that the computed hash matches the imported snapshot's hash.
It does not prove that the snapshot describes the current cloud resource.
Hash equality is not a mathematical proof of byte equality.
Concurrent file changes remain outside this proof.

The proof selects fields for validation; it does not prove that the caller executes
bridge validation or that a provider preview is clean. Existing integration tests
remain necessary for those claims and for source-path and representation behavior.

Review contracts and adapters when requirements change. Do not weaken a contract
merely to obtain a passing proof. A passing mutation suite checks selected mistakes;
it does not establish that the specification contains every requirement.

## References

- [PR #122](https://github.com/pulumi-proserv/pulumi-tool-import/pull/122)
- [Archive scenario matrix](archive-reconciliation.md)
- [Gobra v26.09 tutorial](https://github.com/viperproject/gobra/blob/v26.09/docs/tutorial.md)

## Initial validation

- Podman ran the pinned Gobra image successfully on macOS ARM64.
- Gobra reported zero errors for the production policy, including after Go comment formatting.
- All ten implementation mutations produced postcondition failures. The last case ran separately after the terminal timed out during the combined run.
- Classifier tests cover actual values; asymmetric adapter tests check that input and output are classified separately.
- Focused archive scenarios and evidence-adapter tests passed.
- Formatting, vet, lint, and catalog checks passed with `CGO_ENABLED=0` and golangci-lint v2.15.0.
- The full Go run found two remote-backend failures on the unchanged PR #122 base as well.
  Those tests use a fixed organization and the old API hostname. Their fixture fixes are maintained in a separate PR.

This validation did not run live AWS migration experiments or establish a clean provider preview.
