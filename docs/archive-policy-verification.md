# Archive policy verification

## Design

The archive patcher uses three decision functions in `pkg/archive_policy.go`.
Gobra verifies that same Go source file. Its `// @` comments hold the contracts;
the Go compiler ignores those comments. There is no separate implementation or generated copy.

The functions use scalar values and value structs. They have no imports, heap mutations,
I/O, or trusted declarations. This keeps the first proof independent of the Pulumi SDK
and the rest of the Go module. Gobra checks all three function bodies and termination.
No preconditions exclude unusual Boolean combinations or unknown evidence values.

| Function | Contract |
|---|---|
| `decideArchiveRepair` | Reject malformed, mismatching, and unknown hash evidence. Reject conflicting existing archives without hash evidence. Authorize stale-archive repair only with matching evidence. |
| `needsOutputRepair` | Existing valid outputs need a changed input for this same field before digest differences can authorize replacement. Empty or malformed outputs remain repair candidates. |
| `fieldPatchEffects` | Count input-only and output-only changes. Select changed asset fields with asset-kind metadata for subsequent delta handling. |

`patchResourceFields` supplies the facts and uses these decisions. Hash decoding
distinguishes missing, malformed, mismatching, and matching evidence. Matching inputs
and outputs do not request archive repair. Missing hashes still permit reconstruction
when no conflicting archive exists, as in PR #122.

Patch reporting accumulates across fields, but output-repair authorization does not.
The decision function accepts only the current field's `InputPatched` value.
Both input and output changes contribute to asset metadata selection.

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

The seven mutations check missing-hash trust, hash mismatch acceptance, unverified
replacement, unnecessary repair, missing field-local authorization, lost output-only
reporting, and missing output-only asset validation selection.

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
- All seven implementation mutations produced postcondition failures.
- Focused archive scenarios and evidence-adapter tests passed.
- Formatting, vet, lint, and catalog checks passed with `CGO_ENABLED=0` and golangci-lint v2.15.0.
- The initial full Go run found two remote-backend failures on the unchanged PR #122 base as well.
  Those tests used a fixed organization and the old API hostname.
- The single-workspace fixtures now create, seed, and delete temporary workspaces on `tf.pulumi.com`.
  Both passed in `team-ce`, and the full Go suite passed with `PULUMI_TEST_ORG=team-ce`.

This validation did not run live AWS migration experiments or establish a clean provider preview.
