# Archive reconciliation: examples taken from tests

This is the worked-example companion to the [behavior walkthrough and scenario
matrix](archive-reconciliation.md). Each scenario below names the test that
supplies its fixture and checks its result. These are excerpts of those fixtures,
not customer state and not complete snapshots to import.

## Contents

- [Reading the examples](#reading-the-examples)
- [Artifact selection and preservation: A1–A13](#artifact-selection-and-preservation)
- [State repair: S1–S8](#state-repair)
- [Source and resource variants: P1–P5](#source-and-resource-variants)
- [Verification: V1–V4](#verification)
- [Running the tests and interpreting coverage](#running-the-tests-and-interpreting-coverage)

## Reading the examples

**Test sources:**

- [Archive unit/pipeline tests](../pkg/archive_reconciliation_test.go): `TestArchiveScenario...`
  and `TestArchiveScenarios...` functions named below. AWS calls use a local HTTP
  fixture where needed; these do not create cloud resources.
- [Command-level examples](../cmd/archive_examples_test.go): execute the actual
  Cobra command with temporary state/digest files and, for CFN, mocked Lambda
  endpoints. The displayed commands render the flags passed by those tests.
- [CFN patch tests](../pkg/state_patcher_cfn_test.go) and
  [preview verification tests](../pkg/state_stack_test.go).
- [AWS E2E tests](../test/e2e/e2e_test.go): require credentials and create fixtures.
- [Archive-specific live reproduction](../scripts/repro_lambda_archive.py): a
  separately invoked assertion-bearing experiment, **not part of ordinary Go CI**.

The common unit fixture makes a ZIP containing `bin/handler`, with mode `0755`,
using `writeArchiveScenarioZip`. Its payload is shown per case, such as the text
`deployed` or the binary bytes `00 ff fe 80`. Tests compute checksums from the
complete ZIP bytes, not from that payload alone:

```go
hash := sha256.Sum256(deployed)
importedCodeSha256 := base64.StdEncoding.EncodeToString(hash[:])
archiveHash := hex.EncodeToString(hash[:])
```

For readability, `B64(zip)` and `HEX(zip)` below mean those two encodings of the
same computed SHA-256 digest. They are **notation, not literal state values**.
`TMP`, `SOURCE`, and `PROJECT` stand for the tests' temporary directories.

`FA(path, hash)` abbreviates the actual serialized Pulumi file archive:

```json
{
  "4dabf18193072939515e22adb298388d": "0def7320c3a5731c473e5ecbe6d01bc7",
  "path": "<actual file path>",
  "hash": "<computed hexadecimal SHA-256>"
}
```

An embedded archive instead has an `assets` map. A StringAsset entry uses the
asset signature `c44067f5952c0a294b673a41bacd8c17` and a `text` field. **Both archive
representations are Pulumi concepts**; Terraform supplies a filename attribute.

The archive unit tests use resource `aws_lambda_function.fn`, mapped to
`urn:pulumi:dev::p::aws:lambda/function:Function::fn`. The curated rule is:

```json
{"code": {"asset": "FileArchive", "assetKind": 2, "archiveFormat": 3, "hashField": "source_code_hash"}}
```

A full state file also needs provider/structural metadata. The command tests
include a provider record and reference; abbreviated inputs/outputs below omit
that unrelated boilerplate. The live reproduction actually runs a TypeScript
Lambda program with this input:

```typescript
code: new pulumi.asset.FileArchive("./program.zip")
```

It also retains fixture tags and controls `publish: false` to isolate archive
behavior. It is not evidence that #122 resolves Lambda publication defaults.

## Artifact selection and preservation

### A1 — Correct local ZIP

Fixture from `TestArchiveScenariosA1S1S6S7PatchStateShapes/missing-code-with-delta`:

```text
code.zip: ZIP whose bin/handler payload is "deployed"
digest.attributes: {filename: "code.zip"}
inputs:  {}
outputs: {codeSha256: B64(code.zip), __pulumi_raw_state_delta: {obj: {ps: {}}}}
```

**After:** both `inputs.code` and `outputs.code` are
`FA(TMP/code.zip, HEX(code.zip))`; the imported checksum is unchanged. One resource
is counted as patched and its delta is validated. The second run changes nothing.
The live reproduction's `tool-correct-local` additionally asserts a clean preview.

### A2 — Stale local ZIP or malformed imported checksum

`TestArchiveScenarioA2RejectsStaleLocalZip` first makes ZIP bytes with payload
`deployed`, records their checksum, then overwrites the file with a ZIP containing
`stale`:

```text
digest.attributes: {filename: "code.zip"}
inputs:  {}
outputs: {codeSha256: B64(original deployed ZIP)}
local code.zip: different ZIP bytes
```

**Result:** `does not match deployed codeSha256`; no code input/output is added.
The test `TestArchiveScenarioA2MalformedImportedHashIsRejected` supplies
`"not-base64"` and base64 of `"short"` instead: both fail with
`invalid deployed codeSha256`. None of these cases substitutes a local hash for
the imported checksum. Live case: `tool-stale-local` rejects without output state.

### A3 — Missing local ZIP, deployed download available

`TestArchiveScenariosA3A4RecoveryUsesDeployedZip/missing-local` supplies:

```text
digest.attributes: {filename: "code.zip", function_name: "fn"}
inputs:  {}
outputs: {codeSha256: B64(deployed ZIP)}
local code.zip: absent
mock GetFunction: download URL + B64(deployed ZIP)
download body: ZIP containing binary bytes 00 ff fe 80
```

**After:** inputs/outputs reference
`FA(TMP/.pulumi-import-artifacts/HEX(deployed ZIP).zip, HEX(deployed ZIP))`.
The file still exists and exactly matches the downloaded bytes after patching.
`TestArchiveScenarioA3DownloadPreservesBinaryZip` also verifies repeated recovery
reuses the same content address. Live case: `tool-missing-local` reaches no diff.

### A4 — Malformed archive, with and without recovery

Three fixtures exercise distinct paths:

```text
local code.zip bytes: "not a ZIP"
1. No Lambda identity: reconstruction errors, inputs remain empty.
2. function_name="fn" and valid mocked deployment: recover the deployed ZIP.
3. Download body="not a ZIP", with its correct checksum: still reject the body.
```

Sources: `TestArchiveScenarioA4MalformedArchiveDoesNotBecomeStringInput`,
`TestArchiveScenariosA3A4RecoveryUsesDeployedZip/malformed-local`, and
`TestArchiveScenarioA4DownloadRejectsMalformedZipWithMatchingChecksum`.
In case 2 the invalid source build file stays untouched; the recovered file gets
its own durable path. In case 3 the existing destination still contains
`"keep original artifact"`. Checksum agreement alone does not establish ZIP structure.
These cover malformed data; they are not an exhaustive test of OS permission errors.

### A5 — Neither local reconstruction nor download succeeds

`TestArchiveScenarioA5UnavailableDownloadLeavesInputUntouched` uses:

```text
digest.attributes: {filename: "missing.zip", function_name: "fn"}
inputs: {}
mock download: HTTP 403, body "unavailable"
```

**Result:** an error naming HTTP 403 and no code input. The download failure is
not converted into a raw filename string or an invented archive.

### A6 — Local ZIP but no imported checksum

`TestArchiveScenarioA6MissingHashIsReportedAsUnverified` uses a ZIP whose payload
is `"local only"`:

```text
digest.attributes: {filename: "code.zip"}
inputs:  {}
outputs: {}
```

**After:** code can be reconstructed in inputs/outputs, but stderr must contain
`has no deployed codeSha256; local archive reconstruction is unverified`.
The test asserts no `codeSha256` is manufactured. This is reconstruction, not
proof the file matches the deployed function; conflicting populated archives
are handled separately in S3.

### A7 — Program ZIP overrides a stale Terraform filename

`TestArchiveScenarioA7ProgramArchiveOverridesStaleTerraformPath` uses:

```text
source.zip payload:  "stale"
program.zip payload: "deployed"
digest.attributes:   {filename: "source.zip"}
outputs:            {codeSha256: B64(program.zip)}
matching preview newState.inputs: {code: FA("program.zip", hash omitted)}
```

**After:** state references `TMP/program.zip`, not `source.zip`; the caller's
digest still says `source.zip`. An unrelated Lambda with an embedded archive in
the same preview must not interfere. Live cases exercise both explicit file-mode
preview input and automatic stack-mode baseline selection.

### A8 — ZIP and same-stem directory both exist

From `TestArchiveScenarioA8ExplicitZipWinsOverSiblingDirectory`:

```text
function.zip: ZIP with payload "deployed"
function/different.txt: "stale"
selected path: function.zip
```

**Result:** the archive path is `function.zip` and its hash is the ZIP's SHA-256.
The sibling directory is not silently selected.

### A9 — Directory representation and the content-equivalence limit

`TestArchiveScenarioA9ExplicitDirectoryRemainsDirectory` builds an archive from a
directory containing `index.js` with text `"code"`: its returned `path` remains
the directory and it has no embedded `assets` map.

`TestArchiveScenarioA9DirectoryDoesNotProveZipIdentity` then tests a harder case:

```text
function/bin/handler: "deployed", executable mode 0755
function.zip:        ZIP with the same handler payload and executable mode
digest.filename:     "function"
outputs.codeSha256:  B64(function.zip)
```

The fixture establishes different archive hashes despite equivalent file payloads.
**Result:** hash mismatch; no input code is added. This is explicit boundary
coverage, not successful directory-to-ZIP reconciliation. Feasibility is #121.

### A10 — Program intentionally declares an embedded archive

`TestArchiveScenariosA10S8ProgramEvidenceCannotHideMismatch` supplies matching
preview goals containing each of these actual fixture shapes:

```text
embedded-text:   archive.assets["index.js"] = StringAsset(text="deployed")
embedded-binary: archive.assets["handler"]  = FileAsset(path="handler.bin")
nested-archive:  archive.assets["nested"]   = FileArchive(path="source.zip")
unknown-code:   code = "[unknown]"
```

**Result:** embedded variants report `non-file archive`; the unknown variant
reports `not a known archive`. The full patch returns no candidate state and
does not rewrite the digest. The helper-only rejection test is
`TestArchiveScenarioA10EmbeddedProgramArchiveIsExplicitlyRejected`.
This tests rejection, not a claim that embedded archives are inherently invalid;
their reconciliation feasibility is #130.

### A11 — Binary bytes, Unicode names, and executable metadata

`TestArchiveScenarioA11TerraformPreservesBinaryZip` patches a ZIP containing
`00 ff fe 80 01 02`. It requires a file path/hash, no embedded `assets`, matching
inputs/outputs, and unchanged file bytes.

`TestArchiveScenarioA11PreservesUnicodeAndExecutableMetadata` adds:

```text
entry name: bin/处理器
mode:       0755
timestamp:  2020-01-01T00:00:00Z
comment:    "deployment metadata"
payload:    00 ff fe 80
```

**Result:** the entire ZIP remains byte-identical. Reopening it verifies the name,
mode, timestamp, and comment. The live ZIP fixture also contains `opaque.bin` with
binary bytes and an executable `index.js`.

### A12 — Duplicate names and traversal names are not extracted

Fixtures:

```text
duplicate.zip: two entries called "same-name", containing "first" and "second"
code.zip:      entry "../escaped.txt", containing "must remain inside ZIP"
```

`TestArchiveScenarioA12ZipEntriesAreNotExtractedOrCollapsed` checks the complete ZIP
hash and that no additional files appear. `TestArchiveScenarioA12UnsafeNamesAreNeverExtracted`
checks that `escaped.txt` is not written outside the artifact directory. The latter
explicitly permits such names in Go's ZIP reader to exercise the opaque path:
there is still **no extraction**. These tests do not certify that ZIP contents are
safe to execute or that arbitrary extraction would be safe.

### A13 — Absolute paths, distinct bases, non-.zip names, and missing moved files

The tests cover four concrete inputs:

```text
absolute: filename points into a different directory than --config-dir
two bases: SOURCE/code.zip="stale", PROJECT/code.zip="deployed";
           digest filename="code.zip", preview path="code.zip"
extension: handler.jar contains valid ZIP bytes
missing: filename="missing.zip"; only different.zip exists
```

Sources: `TestArchiveScenarioA13AbsolutePathIsNotRebased`,
`TestArchiveScenarioA13SeparateProgramAndSourceDirectories`,
`TestArchiveScenarioA13ZipContentWithJarExtension`, and
`TestArchiveScenarioA13MissingPathDoesNotGuessAnotherZip`.
**Results:** retain the absolute path; select `PROJECT/code.zip`; accept ZIP content
with a `.jar` extension; reject the missing path rather than guess another basename.
Move/copy an artifact explicitly and update the declared path; relocation is not automatic.

## State repair

### S1 — Missing or malformed code input

The `missing-code-*` and `malformed-code-with-delta` subtests of
`TestArchiveScenariosA1S1S6S7PatchStateShapes` start with:

```text
missing:   inputs={}, outputs={codeSha256: B64(deployed ZIP), ...}
malformed: inputs.code="code.zip", outputs.code="code.zip"
```

**After:** both code fields contain the proper `FA(TMP/code.zip, HEX(deployed ZIP))`
object, not the string filename. Snapshot checksum evidence remains unchanged.

### S2 — Already correct archive is a no-op

After the first successful patch, the same state/digest/artifact are patched again:

```text
inputs.code  = FA(TMP/code.zip, HEX(deployed ZIP))
outputs.code = the same value
outputs.codeSha256 = B64(deployed ZIP)
```

Both `TestArchiveScenariosA1S1S6S7PatchStateShapes` and
`TestArchiveScenarioS4RepairsVerifiedArchiveAndIsIdempotent` require zero additional
patches; the full-pipeline test also requires JSON-equivalent state.

### S3 — Conflicting existing archive without verification evidence

`TestArchiveScenarioS3UnverifiedReplacementIsRejected` records an archive when
`code.zip` contains `"old"`, then rewrites the ZIP with `"new"`:

```text
inputs.code = outputs.code = FA(TMP/code.zip, HEX(old ZIP))
local code.zip = new ZIP
outputs.codeSha256 = absent
```

**Result:** `refusing an unverified archive replacement`; the old state value is
retained. S4 shows the supported repair when independent checksum evidence exists.

### S4 — Corrected ZIP repairs a previous stale patch

`TestArchiveScenarioS4RepairsVerifiedArchiveAndIsIdempotent` records the old archive
from a ZIP containing `"stale"`, then replaces that file with the deployed ZIP:

```text
inputs.code = outputs.code = FA(TMP/code.zip, HEX(stale ZIP))
local code.zip = deployed ZIP
outputs.codeSha256 = B64(deployed ZIP)
```

**After:** both state values carry the new verified archive hash; a second pass is
a no-op. The live reproduction's `tool-corrected-retry` additionally uses state
produced by the old tool and requires a clean provider preview.

### S5 — Output-only repair, stale output, and unrelated fields

`TestArchiveScenarioS5OutputOnlyRepairValidatesDelta` starts with:

```text
inputs.code = FA(TMP/code.zip, HEX(local ZIP))
outputs = {__pulumi_raw_state_delta: {obj: {ps: {}}}}
```

**After:** output code matches input code, `patched=true`, and `deltaValidated=true`.
There is no imported cloud checksum in this fixture: Recover validation is not
the same as deployed-byte verification. The S4 test separately makes only
`outputs.code` stale and requires its repair.

`TestArchiveScenarioS5OtherFieldPatchCannotOverwriteUnchangedOutput` exercises:

```text
inputs.kept = outputs.kept = "current"
digest.kept = "old"
fields: missing has default "default"; kept reads the digest's kept attribute
```

Filling `missing` must leave both `kept` values at `"current"`. A resource-wide
"something changed" flag must not authorize replacing an unrelated output.

### S6 — Existing raw-state delta receives the archive metadata

The `*-with-delta` subtests of `TestArchiveScenariosA1S1S6S7PatchStateShapes` begin
with an empty `obj.ps` map. After repair, its `code` entry is asserted as:

```json
{"asset": {"kind": 2, "archiveFormat": 3, "hashField": "source_code_hash"}}
```

The tests also require one successful Recover validation and no delta failures.
The live reproduction's `canonical-file-with-delta` checks actual provider Diff.

### S7 — No raw-state delta is invented for legacy state

`TestArchiveScenariosA1S1S6S7PatchStateShapes/missing-code-without-delta` starts with
only `codeSha256` in outputs. **After:** code is reconstructed, no delta is added,
and no delta validation is claimed. The live `canonical-file-without-delta` case
requires a clean preview through the provider's legacy conversion path.

### S8 — A changed or omitted program archive is not patched into success

From `TestArchiveScenariosA10S8ProgramEvidenceCannotHideMismatch`:

```text
source.zip = deployed ZIP; imported hash agrees with it
changed-program-bytes: preview points to program.zip with payload "changed"
omitted-code:          preview inputs.code = null
```

**Results:** hash mismatch or `does not declare a local code archive`; no candidate
state is returned. The patcher must not fall back to the correct source ZIP to
hide a program that now wants different bytes or no local archive. Image-based
resources with no Terraform filename are a different case (P4).

## Source and resource variants

### P1 — Terraform filename becomes a Pulumi file archive

`TestArchiveScenarioA11TerraformPreservesBinaryZip` exercises the full Terraform
`PatchState` path with `attributes.filename="function.zip"`. **After:** both code
fields reference that ZIP as a Pulumi file archive, preserving binary bytes.
This is the same transformation shown in A1/A11, not a Terraform archive type.

### P2 — CloudFormation ZIP/S3 code uses the deployed download

`TestPatchStateFromCFN_LocalZipAssetPatch` covers local `fn.zip` through the shared
CFN patch function. The command test
`TestArchiveScenariosP2P3CFNDownloadIgnoresTemplatePackaging/s3-code` uses:

```json
{"logicalId":"fn","cfnType":"AWS::Lambda::Function","physicalId":"fn",
 "pulumiType":"aws:lambda/function:Function",
 "attributes":{"Code":{"S3Bucket":"source-bucket","S3Key":"function.zip"}}}
```

Equivalent tested command arguments:

```bash
pulumi plugin run import -- patch-state cfn \
  --state "$TMP/state.json" --out "$TMP/patched.json" \
  --digest "$TMP/digest.json" --fields "$TMP/fields.json" \
  --artifacts-dir "$TMP/artifacts"
```

The mock requires exactly one `GetFunction(fn)` and one ZIP download, signed for
the digest's `us-west-2` despite environment region `us-east-1`. A second digest
Lambda absent from state must not be downloaded. **After:** inputs/outputs reference
`TMP/artifacts/fn.zip`, whose bytes match the mocked deployment. This is a real
command with mocked AWS, not a newly provisioned CloudFormation stack.

### P3 — CFN inline source still adopts the deployed ZIP

`TestArchiveScenariosP2P3CFNDownloadIgnoresTemplatePackaging/inline-code` changes
the source property to:

```json
{"Code":{"ZipFile":"exports.handler = async () => ({ok: true});"}}
```

**After:** it still references the exact downloaded `fn.zip`. There is no `ZipFile`
property or embedded `assets` map in the resulting code value. The tool does not
reconstruct CloudFormation's ZIP packaging from the inline string. The destination
program must reference the recovered file archive; this does not establish an
embedded-archive reconciliation solution or a live CFN deployment result.

### P4 — Image Lambda does not take the ZIP path

`TestArchiveScenarioP4ImageLambdaDoesNotReconstructZip` uses:

```text
digest.attributes = {package_type: "Image", image_uri: "image:tag"}
inputs = outputs = {packageType: "Image", imageUri: "image:tag"}
preview inputs = the same image properties; no filename or local code archive
```

**Result:** zero patches and JSON-equivalent state. No ZIP reconstruction is attempted.

### P5 — Ordinary binary FileAsset stays a file asset

`TestArchiveScenarioP5BinaryFileAssetIsUnchanged` writes `object.bin` containing
`00 ff fe 80`, then requests `FileAsset`. **Result:** the asset signature (not
archive signature), original path, and SHA-256 of the file bytes. Archive fixes
must not turn ordinary S3 object file inputs into archives.

## Verification

### V1 — File mode proposes state but does not verify a live stack

`TestArchiveScenarioV1FileModeWritesOnlySuccessfulCandidate` invokes the TF command:

```bash
pulumi plugin run import -- patch-state tf \
  --state "$TMP/state.json" --out "$TMP/patched.json" \
  --digest "$TMP/digest.json" --fields "$TMP/fields.json" --config-dir "$TMP"
```

The fixture pre-populates `patched.json` with `"existing output"`.
With a matching ZIP, it becomes valid patched JSON and stderr says `NOT VERIFIED`.
With a stale ZIP, the command errors and that existing output remains untouched.
In both cases `state.json` is byte-identical afterward. No project/stack is selected.

### V2 — Non-regression does not mean the preview is clean

The actual generic verifier fixture in
`TestCheckInjectionVerification_BaselineDirtyPostIdentical` is:

```json
{"steps":[{"op":"update","urn":"urn:pulumi:dev::proj::aws:ec2/x:X::a"}]}
```

Using it for both baseline and verification produces no regression error.
`TestCheckPreviewClean_NonSameStepIsAProblem` requires an outstanding-change report
for that update. These are generic unit tests, not a Lambda fixture. The AWS E2E
`PatchOnlyStackModeWithOutstandingDiffs` tests the command's non-regression behavior;
the archive live reproduction separately records the unresolved `code`/`lastModified`
update in `import-with-publish-control`.

### V3 — New or more destructive operations trigger rejection/restoration

`TestCheckInjectionVerification_BaselineCleanPostDirtyIsAProblem` supplies:

```text
baseline: [{urn: "...aws:ec2/x:X::a", op: "same"}]
verify:   [{urn: "...aws:ec2/x:X::a", op: "update"}]
```

The verifier must report a problem. Escalation tests also cover changes becoming
more destructive. Actual restoration is exercised by the AWS E2E
`RevertRestoresStackExactly` subtest (`testRevertRestoresStackExactly`)
and the archive-specific live reproduction below; unit detection alone is not
proof that a stack was restored.

### V4 — Artifact mutation between previews and failed download replacement

The archive live reproduction prefixes its program with this exact test code:

```typescript
import * as fs from "fs";
const count = fs.existsSync("preview-count") ? Number(fs.readFileSync("preview-count", "utf8")) : 0;
fs.writeFileSync("preview-count", String(count + 1));
if (count === 1) { fs.copyFileSync("stale.zip", "program.zip"); }
```

The second program execution changes the local ZIP after baseline selection. The
`stack-verification-regression-rollback` assertions require rejection, restored
Lambda inputs/outputs, and unchanged live hash/revision. The script restores its
local program/ZIP itself afterward; this is not proof the tool rolls back files.

Separately, `TestArchiveScenarioV4DownloadFailurePreservesDestination` supplies a
wrong downloaded checksum or HTTP 403 while `keep.zip` contains
`"keep original artifact"`. Both errors must leave that destination unchanged.
These local tests cover failed replacement, not all concurrent filesystem races.

## Running the tests and interpreting coverage

Run the local behavior and command examples with:

```bash
go test ./pkg ./cmd -run 'TestArchive|TestPatchStateFromCFN_LocalZipAssetPatch|TestCheckInjectionVerification|TestCheckPreviewClean|TestPatchRevert' -count=1
```

Normal `make test` includes these tests; the tagged `make test-e2e` suite exercises
the shared live verification/rollback path. The separate archive reproduction
and its prerequisites are documented in [the evidence guide](archive-reconciliation.md#recorded-aws-results-after-the-fix).

Every one of the 30 matrix IDs has an explicit test/example mapping above.
**Coverage means the supported behavior or documented rejection boundary is
exercised**, not that every aspirational representation is implemented. Directory
equivalence (A9) and embedded archives (A10) remain investigations. P3 has mocked
command coverage, not a new live CFN run. V4's archive mutation-between-previews
case is the separately invoked live reproduction, not an ordinary unit/CI test.
No claim here expands the earlier provider-version or live-test evidence.
