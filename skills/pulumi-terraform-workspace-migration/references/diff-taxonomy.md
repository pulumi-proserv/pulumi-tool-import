# Post-import diff taxonomy

## Contents

- [Field-level categories](#field-level-categories)
- [not_read sub-causes](#not_read-sub-causes)
- [Input-driven diffs](#input-driven-diffs)
- [Classifying an unknown field](#classifying-an-unknown-field)
- [Probing the TF provider schema safely](#probing-the-tf-provider-schema-safely)
- [The zero-diff fix loop](#the-zero-diff-fix-loop)
- [Staged first `pulumi up`](#staged-first-pulumi-up)

Every post-import diff falls into one of the categories below.

**Run `patch-state` before classifying anything** (see `patch-state.md`). It
removes the entire `not_read` population — which in practice is most of the
post-import wall — so whatever still diffs afterwards is, by construction,
something you have to reason about rather than look up.

**`aws-import-diff-fields.json` is a `not_read` catalogue, not a general
classifier.** Every entry in it is a `not_read` field, because that is the only
class `patch-state` can patch. The other five categories below have no entries
and never will — assigning one of them is a judgement you make from the
provider's Read function, using the procedure in
[Classifying an unknown field](#classifying-an-unknown-field). Do not read a
field's absence from the file as "unclassified": absence is the normal case for
everything except `not_read`.

## Field-level categories

Intrinsic to the field, regardless of how it's used.

| Category | Root cause | Risk | What happens on `pulumi up` | Example fields |
|---|---|---|---|---|
| **not_read** | Per-field: `bridge_default` ([bridge#2436](https://github.com/pulumi/pulumi-terraform-bridge/issues/2436)), `aws_api_limitation`, or `provider_design` | Varies — check the field's `sent_to_aws_on_update` | Read doesn't populate it; check per-field metadata for lifecycle and verification | `forceDestroy` (bridge_default, delete-only), `code` (aws_api_limitation, sent on update), `acl` (provider_design, sent on update) |
| **read_filtered** | Provider design | Safe | Value is set explicitly, but the cloud already has it via different source tracking | RDS ClusterParameterGroup `parameters` |
| **provider_normalized** | Provider design | Safe | No-op — the provider rewrites the input into its normalized form | Listener `defaultActions` |
| **typeset_ordering** | Bridge bug ([bridge#3324](https://github.com/pulumi/pulumi-terraform-bridge/issues/3324)) | Safe | Entries rewritten in program order; resolves after the first up, recurs only on refresh | WAF WebAcl `rules` |
| **computed_cascade** | Consequence | Safe | An output-only field updating because something else changed | Lambda `qualifiedArn` / `version`, S3 `versionId` |
| **default_tags_migration** | Provider difference | Safe | Tags move from explicit `tags` to `tagsAll` via `defaultTags` — no tag is lost | `tags` / `tagsAll` on any resource |

Each field also carries lifecycle metadata (`sent_to_aws_on_create/update/delete`)
telling you whether applying it makes a real cloud API call or only writes state.

## `not_read` sub-causes

- **`bridge_default`** — a TF SDK schema default the bridge applies to null
  import state. The program doesn't set the field. Verify the default against the
  TF code and TF state (the digest).
- **`aws_api_limitation`** — the cloud API does not return this data at all. The
  program must supply the value. Verify against TF state, or the deployed
  resource where possible.
- **`provider_design`** — the API returns the data but the provider's Read
  doesn't populate it. The program must supply the value; verify against the
  deployed resource via CLI (see `verify.aws_cli` in the field metadata).

### Verifying write-only fields before the first `up`

For fields the import cannot read back, confirm the program's value matches
what's deployed **before** applying:

- Lambda code — download the deployed zip (`aws lambda get-function`) and compare
  byte for byte
- S3 object content — compare config secret values against the deployed objects
- Passwords — verify the config secret matches the digest value
- Certificates — verify body, chain, and key

Record the evidence in the migration decisions document.

## Input-driven diffs

Some diffs are **not** provider behavior at all: the field reads and writes
correctly, but the program computes a value that differs from what was imported.
Do **not** add these to `aws-import-diff-fields.json` — the field isn't the
problem, its wiring is. Document them per-resource instead.

| Cause | Example | Resolution |
|---|---|---|
| **Data-source drift** | `aws.getIpRanges` returns current CIDRs; imported state holds the stale set from the last TF apply | Expected — the first `up` refreshes them. Document it. |
| **Unapplied TF code** | The HCL declares 6 roles but state has 4: the code changed and was never applied | Match deployed state, not TF code. Document the discrepancy. |
| **Manual drift** | Tags or security-group rules added by hand outside TF | Include them in the program to preserve them. Document as manual drift. |
| **Wrong resource reference** | An SSM parameter wired to a filesystem ARN instead of an access-point ARN because the component doesn't expose the right output | Fix the reference; add the missing component output. Verify against the deployed value. |

## Classifying an unknown field

1. Check `aws-import-diff-fields.json` — if listed, use its category.
2. If not, read the TF provider's Read function for that resource:
   - Never appears in `d.Set()` → `not_read` (`bridge_default` if the schema has
     a default, else `aws_api_limitation`)
   - In `d.Set()` but the API doesn't return the data → `not_read`
     (`aws_api_limitation`)
   - In `d.Set()` but Read chooses not to populate it → `not_read`
     (`provider_design`)
   - In `d.Set()` but filtered by metadata → `read_filtered`
   - Returned in a rewritten form → `provider_normalized`
3. If the field is a TypeSet in the TF schema → likely `typeset_ordering`.

Then add the entry with the correct `root_cause` and `verify` metadata.

## Probing the TF provider schema safely

The Pulumi schema (`pulumi package get-schema`) does **not** expose TF SDK
defaults. To find them, either read the provider's Go source for
`schema.Schema{Default: ...}`, or dump the schema with `tofu providers schema
-json` (needs `tofu init`).

> **`tofu init` rewrites `.terraform.lock.hcl` in place.** It swaps every provider
> source from `registry.terraform.io` to `registry.opentofu.org` and replaces all
> the hashes. You are inspecting a **live Terraform workspace** that still runs
> Terraform — committing the rewritten lock file forces a re-init or fails
> provider verification on the next run, for no migration benefit.
>
> Copy the config directory and init the copy:
>
> ```bash
> cp -R environments/<workspace> .import/schema-probe
> (cd .import/schema-probe && tofu init -backend=false && tofu providers schema -json > schema.json)
> ```
>
> If you already ran it in place, restore the file before committing:
>
> ```bash
> git restore environments/<workspace>/.terraform.lock.hcl
> ```
>
> **This damage can hide.** Where a workspace does not track its lock file, the
> rewrite is invisible in `git status` — so check every workspace you probed, not
> only the ones showing as modified:
>
> ```bash
> grep -rl "opentofu.org" environments/*/.terraform.lock.hcl
> ```

## The zero-diff fix loop

1. **Patch first, then look.** Run `patch-state` (see `patch-state.md`) before
   investigating anything. Everything it clears was a `not_read` field you would
   otherwise have spent time classifying by hand.
2. **Survey what survived.** `pulumi preview --json > .import/preview.json`, then
   summarize it with the `jq` snippets in the main skill's Phase 6 to see which
   fields and resource types still diff, and how often. A field diffing across
   many resources of one type is usually one root cause, not many.
3. **Investigate each remaining diff** across four sources:
   - **Pulumi state** (`pulumi stack export`) — what was imported: `inputs` and
     `outputs` for the resource
   - **The digest** — TF state's attribute values, the source of truth for what
     was deployed at import time
   - **The TF module code** — always the version resolved into the workspace's
     `.terraform/modules/`, not the module repo at some other ref
   - **The deployed resource** via cloud CLI, when state and digest don't settle
     it (object ACLs, Lambda code, …)
4. **Fix** the component or the program, tracing every value back to its TF
   source (see the value-tracing table in the main skill).
5. **Rebuild** the component if it changed, then reinstall it in the Pulumi
   project.
6. **Re-preview scoped:** `pulumi preview --json --target <urn>
   --target-dependents`, adding `--diff` for human-readable field changes.
7. **Commit** once the scoped preview confirms the fix; run a full preview
   periodically to track the total.

## Staged first `pulumi up`

Once nothing is unclassified, apply in stages grouped by category. For each
stage: review the code producing the values → targeted `--diff` preview,
inspecting every value → confirm the change is expected → targeted `up --yes` →
targeted preview again to confirm zero diffs.

| Stage | Category | Risk | Verify before applying |
|---|---|---|---|
| 1 | not_read, `sent_to_aws_on_update=false` | Safe — state only | Filter to those fields; verify defaults against TF code/state |
| 2 | default_tags_migration | Safe — cosmetic | Tags move `tags` → `tagsAll`; nothing is removed from the cloud |
| 3 | typeset_ordering | Safe — same content | Same entries, different order; confirm names/priorities match |
| 4 | provider_normalized | Safe — equivalent | Format rewrite only, no functional change |
| 5 | read_filtered | Safe — already active | The value is already in effect; the explicit set changes source tracking |
| 6 | input_driven | Expected change | Verify new values come from the data source, not a hardcode; compare counts and added/removed entries |
| 7 | computed_cascade | Auto-resolves | Apply after the upstream not_read diffs; these clear on their own |
| 8 | not_read, `sent_to_aws_on_update=true` | **Dangerous — real API calls** | Verify the deployed content matches the program value first, using `verify.aws_cli` from the field metadata |
| 9 | Intentional new changes | Varies | Confirm the change is desired, not a migration artifact |

**Verification procedure for `not_read` fields** — each entry in the fields file
carries a `verify` object:

1. `verify.check_tf_code` — confirm the value matches what the TF code sets (or
   doesn't set; most `bridge_default` fields are implicit SDK defaults absent
   from the HCL).
2. `verify.check_tf_state` — check the digest for fields that persist in TF state
   from the initial create (e.g. `recoveryWindowInDays=30`); the previewed value
   should match.
3. `verify.check_aws` — for `sent_to_aws_on_update=true` fields, confirm the
   deployed value matches the program value using `verify.aws_cli`.
4. Consult the TF provider schema for defaults — see the `tofu init` warning above.
5. **Start with one resource.** Apply it, compare `pulumi stack export` before and
   after to confirm the field landed in state, and check the next preview is
   clean.

**Common issues during a staged up:**

- **`WAFOptimisticLockException`** — concurrent modification by another process
  (commonly a Lambda updating an IPSet from events). Transient; retry.
- **A missing program input surfacing mid-preview** — e.g. a regional WAF missing
  its whitelist input, showing addresses being emptied. That is a program bug,
  not an expected diff: fix it and re-preview before applying.
- **A resource another system maintains between deploys** — if a Lambda adds
  entries to an IPSet, the program's data source may legitimately return fewer.
  Confirm the TF and Pulumi data sources use the same parameters, then expect the
  external process to re-add its entries.
