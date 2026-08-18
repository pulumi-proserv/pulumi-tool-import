# Raw state delta — test coverage plan

Status: proposed, 2026-08-18. Nothing here is implemented.

## Why this needs a plan

The delta is not what this repo's issues have said it is. `makeTerraformStateViaUpgradeEnabled`
is consulted in `Diff`, `Read` **and** `Update` (`tfbridge/provider.go:1129`, `:1442`, `:1651`),
and `makeTerraformStateWithOpts` is documented as *"the old method used when
makeTerraformStateViaUpgrade is not available"*. So the delta is the **primary
Pulumi → Terraform state conversion path for every operation** on a resource that has one, with
the legacy lossy conversion as fallback. Surviving a provider version upgrade is a consequence,
not the purpose.

Two things follow, and they set the bar for coverage:

1. **Delta correctness is a per-operation concern.** A wrong delta produces wrong Terraform
   state on every `pulumi preview`, not at some future upgrade.
2. **The failure mode is a crash, not a degradation.** `makeTerraformStateViaUpgrade` wraps both
   `UnmarshalRawStateDelta` and `Recover` in `contract.AssertNoErrorf`. A delta that will not
   parse or will not apply **panics the provider process**. The `validateRecover` call injection
   performs before writing a delta is the only thing between a bad delta and that crash — it is
   load-bearing, and should be treated as such.

## What the bridge already tests — do not duplicate

`pulumi-terraform-bridge/v3@v3.121.0/pkg/tfbridge/rawstate_test.go` (~1300 lines):

| Test | Cases | Level |
|---|---|---|
| `Test_rawstate_delta_turnaround` | 27 | hand-built `schema.Schema` + PropertyValue/cty pairs |
| `Test_rawstate_against_MakeTerraformOutputs` | 20 | autogold snapshots of the produced delta |
| `Test_rawstate_delta_serialization` | 7 | JSON shape of each node kind |
| `Test_rawStateReducePrecision`, `Test_isSimilarNumber` | — | number handling |
| `Test_rawStateDelta_PropertyValue_serialization` | — | PropertyValue round trip |

Its turnaround cases already cover: nulls; numbers including unequal and big-int-as-string vs
as-f64; empty and unequal strings; bools; MaxItems=1 for object and set including nil variants;
empty set/list/map; nil-list-as-empty; null-vs-empty-map in both directions; values inside
list/set/map; objects with nulls missing on the Pulumi side; and objects with ignored Pulumi keys.

**That is the primitive/schema-shape space, and it is well covered.** Re-testing it here would
add maintenance cost and no signal.

## What is ours to cover — the seam

The bridge tests its own function against hand-built schemas. Nothing tests **our pipeline**:

```
Terraform state JSON  →  cty value  →  pulumiOutputsFromCty (MakeTerraformOutputs,
  supportsSecrets=false, assets=nil)  →  RawStateComputeDelta  →  json.Marshal
  →  SIDECAR FILE  →  LoadNonImportableFile  →  attachRawStateDelta (+ redaction,
  + envelope)  →  deployment JSON  →  engine  →  bridge Diff/Read/Update
```

Every arrow is ours. Failures found today lived at those arrows, not inside `RawStateComputeDelta`:
the `timeouts` type/value mismatch, `Marshal().Mappable()` corrupting Replace nodes, and the
missing secret envelope.

### Acceptance criterion

For every case: **the recovered raw state must equal the original attribute JSON exactly**, not
merely apply without error. `TestComputeInjectionState_NestedBlockDeltaRecovers` already sets this
standard; it should be the standard everywhere.

A second gate where affordable: hand the recovered raw state to the real provider's
`UpgradeResourceState` and require acceptance. Measured scope of that check —
it rejects wrong attribute types, non-object JSON and malformed JSON, but **accepts an empty
object and one missing required attributes**. A structural and type check, not an exhaustive one,
and it should not be described as more than that.

### On comparators — learned the hard way

A first pass at a breadth sweep reported 5 mismatches out of 10 resource types. A careful
per-attribute comparison of one of them showed **0 differing attributes of 23**. The finding was
an artifact of the comparator, not a defect. Before any sweep lands, its comparison must be
verified against a case known to be correct, and must report *which* attribute differs rather
than a boolean — a sweep that cries wolf is worse than no sweep.

## Tier 1 — one case per delta node kind

The node kinds, from `RawStateDelta`: `plu` (pluralize/MaxItems=1), `map`, `obj` (with `ignored`
and `renamed`), `arr`, `asset`, `num`, `replace`. Plus the absence of a node, which routes to
`rawStateRecoverNatural`.

Current coverage, measured:

| Node kind | Covered today | By what |
|---|---|---|
| `obj` | yes | RandomPet, nested-block, patch-group |
| `plu` | yes | nested-block (two levels of MaxItems=1) |
| `arr` | partial | nested-block (empty lists only) |
| `renamed` | incidental | observed in 4 of 10 probed types, never asserted |
| `map` | no | `tags` is empty `{}` in every fixture |
| `num` | no | never asserted |
| `asset` | unknown | `pulumiOutputsFromCty` passes `assets: nil` — **first establish whether our path can produce one at all** |
| `replace` | synthetic only | marshalling test uses a hand-written node |
| natural (no node) | yes | RandomPet |

Each gap gets one test at the seam: real 5.100.0 schema, realistic attributes, full sidecar JSON
round trip, exact-equality assertion.

`replace` deserves particular care — it is the node that carries verbatim provider bytes, it is
the one the bridge secrets, and it is produced when natural recovery *cannot* reproduce a value.
A realistic trigger is a JSON-valued string attribute such as an IAM policy document. We have
never produced one from a real computation.

## Tier 2 — the properties that have actually bitten

These are regressions-in-waiting, each traceable to a real incident:

1. **Large integers.** > 2^53 through the whole pipeline. Related: #29, and the measured table in
   the ledger (2^53+1 silently becomes 9007199254740992).
2. **Secrets and the delta.** A redacted attribute must never leave `(sensitive)` in a delta
   (covered by `attachRawStateDelta`'s `bytes.Contains` screen — assert it), and a `replace` node
   must arrive in state enveloped (covered by `envelopeReplaceNodes` — assert the round trip
   through `RemoveSecrets`).
3. **`ignored` / the `region` case.** A property present in Pulumi outputs but absent from
   Terraform. This is what #30's original theory was about, and the mechanism was ultimately shown
   to be handled naturally — pin that behaviour so it cannot silently change.
4. **`timeouts`.** Already covered in three subtests; keep, and extend to a type where timeouts
   are populated *and* nested blocks are present.
5. **Empty vs null**, at our seam specifically: JSON `null` vs absent key vs `[]` vs `{}` vs `""`,
   after the sidecar round trip. The bridge covers this at the schema level; we need it after
   `json.Marshal`/`Unmarshal`, which is where `omitempty` lives.

## Tier 3 — breadth sweep over injectable types

The population that matters is *non-importable* types, since those are the only ones this tool
computes deltas for. That set is enumerable offline: probe each type with `importsupport.Prober`.

Shape: for each type, synthesize a value from `sch.Block.ImpliedType()`, compute the delta,
round-trip through JSON, recover, compare per attribute, and report the differing attribute names.

Two known caveats before building it:

- **Synthetic values are weak evidence.** An all-null value exercises the walk but not the
  interesting shapes. Populating from the schema (a string for strings, one element for lists,
  one entry for maps) is more work and much better signal.
- **It must be honest about what it skipped.** Types that fail to marshal, or have no bridged
  schema, must be counted and reported, not silently dropped.

Run it behind a build tag or with an explicit cap, so it does not slow the default suite. It needs
no cloud credentials — only the provider binary — so it can run in CI alongside the existing
provider-cache job.

## Tier 4 — end to end

**The e2e already exercises delta consumption, and this was initially understated here.** The
post-injection preview runs `Diff` on every injected resource, `Diff` is one of the three sites
that consume the delta, and `contract.AssertNoErrorf` → `failfast` → `panic` is unconditional
(no build tag). The sdk-v2 shim implements `ProviderWithRawStateSupport`
(`sdk-v2/provider2.go:540`), so the path is available to bridged AWS resources rather than
silently skipped.

So runs 16 and 17 — `Deltas attached (injected): 11 of 11`, every non-importable resource
previewing `same`, no provider crash — are real evidence that all 11 deltas parse, apply, and
produce Terraform state that diffs clean. That is a stronger existing signal than the rest of this
plan assumed.

What it should add:

1. **Assert `Deltas attached (injected): X of Y` equals `Y of Y`.** Run 17 reports `11 of 11` and
   nothing checks it, so a regression to `0 of 11` would pass silently — every scenario would
   still go green, because a missing delta degrades to the legacy conversion rather than failing.
2. **Prove the mechanism is live, by deliberately corrupting a delta.** This is the load-bearing
   one. Without it, "previews as `same`" is consistent with the delta path never being taken at
   all — the same vacuity trap as the orphan sweep that silently checked nothing. A scenario that
   writes a knowingly-bad delta into injected state and asserts the next preview *fails* converts
   every other delta assertion from "consistent with correctness" to "sensitive to it".
   Expect a provider crash rather than a clean error, and assert on the failure, not its wording.
3. **A resource type whose delta is non-trivial.** Every fixture non-importable type is schema
   version 0 and structurally flat. `aws_ssm_patch_group` is the only AWS type that is both
   non-importable and schema-versioned (measured: 1 of 1526 in 5.100.0), and it is free and
   trivial to create — a good fixture addition.

## Explicitly out of scope

- Re-testing the bridge's primitive turnaround space.
- A real provider-version-pair upgrade test. `aws_ssm_patch_group` already had `SchemaVersion: 1`
  at terraform-provider-aws v4.0.0 (Feb 2022), so no modern `pulumi-aws` pair straddles the bump.
  Synthesizing the version gap is the only buildable form, and Tier 1/4 cover the reconstruction
  it would depend on.

## Order

Tier 1 first (cheapest, closes named gaps), then Tier 2 (regressions with known incidents),
then Tier 4 items 1–2 (small e2e assertions), then Tier 3 (most work, needs the comparator
solved first).
