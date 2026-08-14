# `patch-state` — eliminating post-import diffs

## Contents

- [The pipeline](#the-pipeline)
- [How it works](#how-it-works)
- [Falsy default suppression](#falsy-default-suppression)
- [Diagnosing "this field needs adding to the fields file"](#diagnosing-this-field-needs-adding-to-the-fields-file)
- [Injecting non-importable resources](#injecting-non-importable-resources)

After import, many fields show diffs because the provider's Read doesn't return
them — write-only fields and IaC-only defaults. `patch-state` fills them from the
TF digest using the curated fields file (`aws-import-diff-fields.json`, at the
tool repo root) plus stack-config secrets.

**`--fields` is the only mode.** The old `--schema-driven` flag and the v1 fields
format are gone; the file is now the flat v2 shape
(`{provider, falsyDefaultSuppression, fields}`) keyed by full Pulumi type token.
Schema-driven patching filled every nil schema-valid input, which *created*
phantom diffs for fields the program doesn't set (`sourceHash` on S3 objects,
explicit `tags` where the provider uses `defaultTags`). The curated file only
patches fields with known, verified behavior.

## The pipeline

```bash
# 1. Export with --show-secrets so null sentinels are visible as plaintext
pulumi stack export --show-secrets > .import/state.json

# 2. Patch — wrap in `pulumi env run` for cloud credentials (Lambda code download)
pulumi env run <esc-env> -- \
  pulumi plugin run import -- patch-state \
    --state .import/state.json \
    --digest .import/tf-digest.json \
    --fields <tool-repo>/aws-import-diff-fields.json \
    --mapping-file <workspace>-mappings.yaml \
    --project-dir . \
    --stack <stack-name> \
    --config-dir ../environments/<workspace> \
    --out .import/patched.json

# 3. Import the patched state (re-encrypts the plaintext sentinels)
pulumi stack import --file .import/patched.json
```

**Critical requirements:**

- **`--show-secrets` in step 1.** Without it, null sentinels in outputs are
  encrypted ciphertext and the patcher cannot detect or replace them.
- **Cloud credentials in step 2**, for downloading deployed Lambda code when the
  local artifact isn't available.
- **`--config-dir`**, pointing at the TF config directory, so asset file paths
  resolve (e.g. a static file referenced by an S3 object).
- **Per-workspace mappings.** Use the mappings file for the workspace you are
  patching — resource paths (SSM parameter names, bucket names) differ per
  environment.

Afterwards, run `pulumi preview` to confirm the diff count dropped, and classify
what's left with `diff-taxonomy.md`.

## How it works

For each resource in state whose type appears in the fields file:

1. **Defaults applied to all instances** — fields with a non-falsy default (IAM
   Role `path: "/"`, Secret `recoveryWindowInDays: 30`) are applied to every
   resource of that type regardless of digest match, because the bridge default
   issue affects all instances. *Falsy defaults are the exception — see below.*
2. **Digest values applied to matched instances** — per-resource values like
   Lambda `code` and S3 `source` are patched only where the resource matched a
   digest entry through the mappings.
3. **Asset sentinel construction** — for `FileAsset`/`FileArchive` fields, TF
   file paths become Pulumi asset/archive sentinels with SHA-256 hashes, falling
   back to downloading the deployed Lambda code.
4. **Secret resolution** — fields redacted as `"(sensitive)"` in the digest are
   resolved from the decrypted stack config.
5. **Output patching** — simple values and asset sentinels are mirrored into
   outputs, because the bridge reconstructs TF state from outputs when diffing.
6. **Delta injection** — asset delta entries go into `__pulumi_raw_state_delta`
   for correct bridge Recover behavior.
7. **Recover validation** — every patched resource is validated with the bridge's
   `Recover`. On failure, **both inputs and outputs are reverted** to their
   pre-patch values, so the resource keeps its original imported state. This
   prevents provider panics at preview time.

## Falsy default suppression

**Do not patch falsy defaults into state on modern providers — it creates the
very phantom diff you are trying to remove.**

Bridge v3.127.0+ (AWS provider ≥ 7.27.0) added
`shouldSuppressTFSchemaDefaultValue`, which suppresses **falsy** TF schema
defaults (`false`, `0`, `""` — `nil` is not falsy; it means "no default") during
`Check`:

- `Check` applies defaults twice: once without TF defaults (validation), then
  again with them for the returned inputs. The falsy ones are suppressed on that
  second pass, so **the program ends up sending `null`**.
- `MakeTerraformConfig` (the Diff path) has *always* set `DisableTFDefaults: true`.
- So if state holds `false` while the program sends `null`, you get a permanent
  `false -> null` diff. Leaving **both** null yields no diff.

Patching a falsy default *in* is therefore actively harmful on these providers —
and still **required** on older ones. `patch-state` decides automatically, gated
on the resource's actual provider version read from state:

```json
{
  "provider": "aws",
  "falsyDefaultSuppression": { "aws": "7.27.0" },
  "fields": {
    "aws:iam/role:Role": {
      "not_read": {
        "forceDetachPolicies": { "default": false },
        "path":                { "default": "/" }
      }
    }
  }
}
```

On AWS ≥ 7.27.0, `forceDetachPolicies` (falsy) is skipped while `path`
(non-falsy) is still patched; on an older provider both are patched. There is no
flag — declare the real TF SDK default and the tool decides.

**Two things get skipped, not one.** This trips people up:

1. The **default fallback** — don't write `false` when the field is nil.
2. **Digest values equal to the suppressed default.** The TF SDK stores schema
   defaults in state, so the digest for a `bridge_default` field almost always
   contains the default itself; patching it from the digest would reintroduce
   exactly the same phantom diff. A digest value that *differs* from the default
   (the module really set `true`) is still patched.

Verify from the run summary:

```
Skipped falsy suppressed: 53
Fields from defaults:      0
Fields from digest:       19
```

A high `Skipped falsy suppressed` on a modern provider is correct. If
`Fields from defaults` counts falsy values on AWS ≥ 7.27.0, either the
`falsyDefaultSuppression` entry is missing or the provider version wasn't
resolved from state.

## Diagnosing "this field needs adding to the fields file"

The most common reason `patch-state` leaves a diff is simply that the field — or
the whole resource type — is not in the fields file. The tool only patches what
the file lists.

**Signature** — all of these together:

- The diff is on a field the **program does not set** (grep it: absent).
- `pulumi stack export` shows the field nil/absent in `inputs` (often with a null
  sentinel in `outputs`).
- The **digest has a real value** for the corresponding TF attribute, or the TF
  schema defines a default.
- The field is **not** listed under that resource type in the fields file.
- The patch-state summary shows the resource under `No fields to patch`, or the
  type is absent from `fields` entirely.

That combination means the provider's Read did not populate it and nothing told
the tool to fill it — a `not_read` field awaiting an entry.

**Rule out the alternatives first:**

| Symptom | Not a fields-file problem |
|---|---|
| The program *does* set the value and it differs | Program/component bug — fix the code |
| The value comes from a data source or changed upstream | `input_driven` — expected drift |
| Same value, different shape or order | `provider_normalized` / `typeset_ordering` — cannot be patched away |
| Listed in the file but still diffing | Mapping problem — the resource didn't match a digest entry; check the mappings file and the `Digest mapped` count |

**Confirm the classification** by reading the provider's Read function (see
"Classifying an unknown field" in `diff-taxonomy.md`), then add the entry with
the correct `root_cause` and `verify` metadata. Keys are full Pulumi type tokens:

```json
"aws:ec2/securityGroup:SecurityGroup": {
  "not_read": { "revokeRulesOnDelete": { "default": false } }
}
```

Per-field metadata available: `default`, plus `asset` / `assetKind` /
`archiveFormat` / `hashField` for `FileAsset`/`FileArchive` fields such as Lambda
`code` and S3 `source`.

**You usually do not need to re-import.** Adding *new* field entries is safe to
apply against a fresh export, because those fields are still nil in state —
nothing to overwrite:

```bash
pulumi stack export --show-secrets > .import/state.json
# ... patch-state with the updated fields file ...
pulumi stack import --file .import/patched.json
pulumi preview
```

Re-import is required only when **URNs change** (a renamed resource, or a
component child becoming a bare resource).

Iterate one resource type at a time and re-preview — it keeps the cause of each
change in the diff count unambiguous.

## Injecting non-importable resources

`patch-state tf` also writes resources that `resolve tf` could not put in the
import file — those flagged `nonImportable` in the digest and recorded in a
`*.non-importable.json` sidecar — directly into state. Pass the sidecar with
`--non-importable`. Full background on why these resources exist and how the
tool detects them: `docs/non-importable-resources.md` in the tool repo.

Injection needs a preview to source each resource's URN, parent, provider and
dependencies from — the sidecar only carries the ID and Terraform attributes.
Where the preview comes from depends on which of two modes the command runs in:

- **File mode** — `--state` and `--out` are both set, same as ordinary
  patching. Supply the preview yourself with `--preview-json <file>`, produced
  by `pulumi preview --json > preview.json`. After patching, `pulumi stack
  import --file <out>` and `pulumi preview` are still your responsibility; a
  correct injection previews as zero operations.
- **Stack mode** — `--state` and `--out` are both omitted; `--project-dir` and
  `--stack` select the stack directly, and `--preview-json` is rejected (the
  command runs its own previews). In order: export the stack, write a
  timestamped backup and print its absolute path, take a baseline preview,
  patch and inject, import, take a verifying preview, and revert to the
  pre-mutation export if verification fails. `--backup-dir` chooses where the
  backup is written (default: the current directory).

  **The backup contains decrypted secrets** — the Automation API's export runs
  with `--show-secrets` underneath. Delete it once the change is confirmed.

  Verification compares the verifying preview against the baseline, not
  against an absolute "clean" bar: a stack mid-migration legitimately still has
  outstanding diffs, and `patch-state` is often run iteratively against
  exactly that stack. The gate is that every injected resource's URN must
  report `same`, and the count of non-`same` steps outside the injected set
  must not increase versus the baseline — regression, not residue, is what
  fails the run.

  Stack mode needs a runnable program and live credentials, since it runs
  `pulumi preview` itself.

`pulumi refresh` reporting "unchanged" is never a substitute for either preview
check: for these resource types it only proves the ID resolves, not that the
injected values are right — see `docs/non-importable-resources.md`.
