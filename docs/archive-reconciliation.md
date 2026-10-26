# Archive reconciliation: scenarios, expectations, and evidence

## Objective

Adopt deployed resources into a maintainable Pulumi program without changing the
deployed artifact. State patching must reconcile missing import information, not
hide a real difference between the program and the deployment. Completion is an
independent clean preview; an update is not the remedy for a bookkeeping mismatch.

Baseline under investigation: `main` at `67a0d40`, compared with the evaluated
release v0.3.0 (`d0236c743de253eda1e4736780ac00c16277d85b`).

## Distinctions that must not be conflated

- **Content identity:** ZIP entries have the same names and bytes.
- **Deployment identity:** the exact ZIP bytes match Lambda's `CodeSha256`.
  Repacking identical contents can change timestamps, compression, ordering and
  therefore the deployment hash.
- **Pulumi representation:** `FileArchive(zip)`, `FileArchive(directory)` and an
  embedded `AssetArchive` are not interchangeable serialized values or hashes.
- **State validity:** bridge `Recover` accepting a value is necessary, but does
  not establish a no-change provider `Diff` against the program.
- **Verification:** a patch that introduces no *new* operations may still leave
  the original archive diff unresolved. Report both facts separately.

For byte-preserving Lambda migration, prefer the original deployed ZIP and a
program that references it. Do not fabricate a hash to make a different archive
look identical. Where the program deliberately uses another representation,
diagnose that contract instead of silently changing its meaning.

## Scenario matrix

The expectations below are the acceptance criteria for this investigation, not
a claim that each row is already implemented or provider-verified.

| ID | Scenario | Expected behavior | Why |
|---|---|---|---|
| A1 | Local ZIP matches deployed hash; program uses `FileArchive(zip)` | Reuse exact bytes; emit the matching file archive; preview unchanged | No download or repacking is needed |
| A2 | Local ZIP exists but differs from deployed hash | Do not trust existence; recover deployed bytes or report mismatch | A stale build must not become the adopted deployment |
| A3 | Local ZIP missing; deployed download available | Recover exact ZIP to a durable artifact location and reference it | A temporary file deleted on exit is not a usable program artifact |
| A4 | Local ZIP invalid/unreadable | Diagnose separately from missing; deployed recovery may proceed when available | An invalid ZIP must not become a plain string in an archive field |
| A5 | No local ZIP and deployed download unavailable | Leave state untouched and report unresolved artifact | No data is better than invented state |
| A6 | Local ZIP present but no authoritative deployed hash/cloud access | Clearly distinguish unverified local reconstruction from verified deployed preservation | Offline operation cannot prove live artifact identity |
| A7 | Terraform source ZIP stale, program points to a separately recovered correct ZIP | Use/validate the program artifact rather than the stale Terraform path | Source-location convenience must not override migration intent |
| A8 | Local directory and same-stem `.zip` both exist | Respect the explicitly selected path; do not silently prefer the directory | A directory is not necessarily the ZIP's contents or bytes |
| A9 | Program uses `FileArchive(directory)` | Match its representation; do not claim byte identity with an original ZIP without evidence | Directory archiving is a distinct contract |
| A10 | Program intentionally uses embedded `AssetArchive` | Preserve binary-safe entries and its representation; distinguish equivalent contents from exact ZIP identity | Valid Pulumi programs need not all use file archives |
| A11 | ZIP contains binary entries, Unicode names or executable metadata | Preserve original ZIP bytes for file archives; never convert arbitrary binary bytes through UTF-8 strings | Text conversion/repacking can corrupt executable artifacts |
| A12 | ZIP contains unsafe names or duplicate entries | File archive preserves opaque ZIP; any extraction/conversion must reject unsafe/ambiguous cases | Conversion must not introduce traversal or silently discard entries |
| A13 | Relative, absolute, moved or cross-directory paths | Resolve against the declared source/program base; retain a durable usable destination path | Patching must not depend on a discarded working directory |
| S1 | Imported `code` is absent or malformed | Populate a validated archive in the required input/output locations | Import cannot read every write-only input |
| S2 | Existing archive already correct | Idempotent no-op | Repeated repair should not cause state churn |
| S3 | Existing archive is validly encoded but wrong/stale | Detect mismatch; repair only under an explicit supported reconciliation policy, otherwise diagnose | Sentinel shape alone is not correctness |
| S4 | ZIP corrected after an earlier bad patch | Do not silently report success while retaining the old artifact | This is the retry path the eval agents encountered |
| S5 | Inputs and outputs disagree | Reconcile the verified archive consistently or reject; do not leave split representations | Provider diff reconstructs prior Terraform state from outputs |
| S6 | Raw-state delta exists | Keep asset kind, archive format and hash-field metadata consistent; validate Recover and Diff | A syntactically valid delta can still yield a persistent diff |
| S7 | No raw-state delta (older/translated state) | Support the provider's legitimate legacy path or explain unsupported format | Do not invent an unverified delta |
| S8 | Program declares different bytes or omits `code` | Report real program/artifact mismatch; do not suppress it by state editing | Clean preview must not mean relinquishing code management |
| P1 | Terraform Lambda ZIP | Same preservation/representation rules as equivalent CFN migration | Source IaC language should not determine archive semantics |
| P2 | CFN Lambda ZIP or downloaded S3 code | Preserve existing correct CFN file-archive behavior | A Terraform fix must not regress the already-supported path |
| P3 | CFN inline source | Keep the documented generated-program representation and test it explicitly | Inline text and a recovered deployed ZIP are different inputs |
| P4 | Lambda container image | No ZIP reconstruction; preserve image-based configuration | Image deployments have a different artifact model |
| P5 | S3 object `FileAsset` | Preserve existing binary file-asset behavior | Archive-specific changes must not alter ordinary file assets |
| V1 | File mode | Produce a complete proposed state or an error; clearly label absent live verification | Writing JSON does not prove successful migration |
| V2 | Stack mode, no regression but original code diff remains | Keep the residual diff visible; do not claim zero-diff completion | Non-regression is weaker than resolution |
| V3 | Stack mode introduces a new change or verification fails | Restore the original state and report the failure | Preserve the existing rollback contract |
| V4 | Artifact changes between selection and verification | Detect inconsistency; avoid committing a state/hash pair for different bytes | Selection and verification must refer to the same artifact |

## Evidence plan

1. Deterministic local tests: source precedence, binary preservation, representation,
   hash identity, existing-state repair/no-op behavior and bridge Recover.
2. Provider-backed reproduction: one imported Lambda, pinned AWS provider 7.49.0,
   no physical updates; record detailed preview diff, relevant inputs/outputs and
   raw-state delta before and after patching. Verify deployed identity/hash stay
   unchanged. Compare file and embedded archive shapes independently.
3. Regression checks for CFN, directories, file assets and error/rollback behavior.
4. A small designated-tool agent trial only after the direct tool path converges.

## Findings so far

The v0.3.0 local reproduction confirms A2/A7 source-selection risk, differing file
and embedded hashes, and S4 retaining a previously populated stale sentinel.
Current `main` also reproduced A8 (a sibling directory overrode an existing ZIP),
A11 (binary ZIP entries became invalid UTF-8 text), and A13 (an absolute path was
incorrectly rebased). These three regression tests failed before the fix.

The AWS reproduction uses AWS provider **7.49.0**, Pulumi CLI **3.259.0**, and the
TypeScript SDK **3.255.0**. It creates one disposable Lambda and IAM role through
AWS APIs, then uses only Pulumi import, state import/export and preview. It never
calls `pulumi up`. Every preview compares live `CodeSha256`, `LastModified` and
`RevisionId` against the original deployment. The final fixture includes binary
ZIP content; all resources and local stack registrations were cleaned up.

**Experimental control:** the program retains the fixture's tags and explicitly
sets `publish: false`; imported `publish` inputs/outputs are normalized to false.
The raw native import is retained separately. An independent `publish` default
diff exists without that normalization and is **not fixed or concealed by this
archive change**. The first baseline run showed that substituting the matching
file archive removed `code`/`lastModified` diffs while leaving unrelated diffs.

### Recorded AWS results after the fix

| Experiment | Result |
|---|---|
| Native import with publication control, before archive repair | `code`/`lastModified` update remains (expected control) |
| Correct local ZIP through patched tool | Clean preview |
| Matching `FileArchive`, existing raw-state delta | Clean preview |
| Matching `FileArchive`, no raw-state delta | Clean preview |
| Stale local ZIP, no program evidence supplied | Explicit rejection; no output state written |
| Corrected ZIP after state was patched by the old tool | Clean preview |
| Missing local ZIP, deployed-code recovery | Clean preview; durable exact ZIP retained |
| Stale Terraform ZIP, correct program ZIP supplied via preview | Clean preview |
| Same case through stack mode | Clean preview; program path selected automatically |
| Program artifact changes between baseline and verifying preview | Rejected; prior Lambda state inputs/outputs restored |

All applied cases retained the original deployed hash/revision. Baseline and
fixed behavior are reproducible with `scripts/repro_lambda_archive.py`; it writes
a private `report.json`, command diagnostics, and before/after state/preview
snapshots. Supply AWS credentials, a separate Pulumi home and a trusted login
command. Build the old and new CLI binaries separately, then run:

```sh
python scripts/repro_lambda_archive.py \
  --tool /path/to/fixed-tool --legacy-tool /path/to/baseline-tool \
  --pulumi-home /path/to/isolated-home --login-command 'your-login-command' \
  --expect-fixed --output /path/to/new-private-output-directory
```

The legacy binary generates the populated stale archive for the repair case.
The output directory must be new. The script requires `boto3`, npm, AWS Lambda/IAM
fixture permissions, and a shell providing the specified login command. Raw
snapshots are investigation artifacts, not files to commit or publish.

### Matrix disposition and limits

| IDs | Coverage / disposition |
|---|---|
| A1, A2, A3, A7, S4, S6, S7, V1 | AWS results above plus deterministic regression tests |
| A4, A5 | Malformed ZIP and failed-download tests leave inputs/destination untouched; conversion failure is an error, never a raw string masquerading as an archive |
| A6 | Offline local reconstruction remains supported with an explicit unverified warning; it does not establish live artifact identity |
| A8 | Explicit-file precedence regression; legacy same-stem directory fallback only applies when the ZIP is missing |
| A9 | Directory representation is retained and unit tested. It cannot be called byte-identical to a deployed ZIP merely because files look equivalent; hash mismatch is diagnosed |
| A10 | **Not automatically supported for byte-preserving repair.** Selected embedded/non-file program archives are explicitly rejected when preview evidence is supplied. A separate content-equivalence contract would be needed; this patch does not synthesize embedded text assets |
| A11 | Binary byte preservation, hashes and file representation tested locally and with the final AWS fixture |
| A12 | Opaque ZIP handling does not extract or collapse duplicate entries; local regression covers duplicate names. ZIP structure is validated, not a new extraction/security subsystem |
| A13 | Absolute paths, relative program paths and ZIP content with `.jar` extension covered; relocation is explicit rather than guessing an alternate basename |
| S1, S2, S3, S5 | Typed archive reconstruction, idempotence, refusal of unverified replacement and output-only repair tests. Output-only repairs now inject delta metadata and run Recover validation |
| S8 | A selected program without a local archive is diagnosed instead of claiming a complete artifact migration. Image/S3-based programs without a Terraform filename do not take the ZIP path |
| P1, P2 | Shared file-archive handling; existing CFN local-ZIP regression passes. No new live CloudFormation stack was provisioned |
| P3 | Existing CFN inline-source path is not redesigned or newly live-validated |
| P4, P5 | Image Lambda no-ZIP and binary `FileAsset` regressions pass |
| V2 | Existing non-regression policy remains: outstanding changes must still be disclosed; it is not a completion guarantee |
| V3, V4 | Live introduced-change rollback passes; failed HTTP/checksum downloads preserve existing destinations. Arbitrary concurrent filesystem mutation is not an atomic snapshot guarantee |

Local ZIP verification uses `codeSha256` from the imported state snapshot. A
snapshot can itself be stale: the tool does not perform an additional AWS read
for every local file. Downloads do verify their bytes against `GetFunction`'s
returned hash. Keep the import snapshot current and use the final independent
preview; file mode alone cannot certify current cloud state.

Stack mode needs a runnable baseline. If the program's own archive is missing or
the old state is too malformed to preview, use file-mode recovery/repair first,
retain the recovered artifact and wire the program to it, then preview. The live
missing-source test exercises file-mode recovery, not automatic recovery from an
unrunnable stack-mode program.

`make check` and the focused archive/patch/preview tests pass. The full test suite
initially hit unrelated provider-download failures (including GitHub 504s); a
clean baseline reproduced the provider-time download failure. The final
`make test` run passed after downloads recovered. This does not imply that every
separately tagged AWS e2e suite was executed.

An unresolved row remains explicitly unresolved. Do not replace the deployed
artifact requirement with a passing preview obtained by removing `code`.
