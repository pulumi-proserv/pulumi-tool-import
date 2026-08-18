# Raw state delta — test coverage plan

Status: revised 2026-08-18 after measuring several of its own first-draft assumptions and finding
them wrong. Tier 1 and Tier 2 partly implemented; the rest proposed.

## Why this needs a plan

The delta is not what this repo's issues have said it is. `makeTerraformStateViaUpgradeEnabled`
is consulted in `Diff`, `Read` **and** `Update` (`tfbridge/provider.go:1129`, `:1442`, `:1651`),
and `makeTerraformStateWithOpts` is documented as *"the old method used when
makeTerraformStateViaUpgrade is not available"*. The delta is the **primary Pulumi → Terraform
state conversion path for every operation** on a resource that has one, with the legacy lossy
conversion as fallback. Surviving a provider version upgrade is a consequence, not the purpose.

Two things follow, and they set the bar:

1. **Delta correctness is a per-operation concern.** A wrong delta produces wrong Terraform state
   on every `pulumi preview`, not at some future upgrade.
2. **The failure mode is a crash.** `makeTerraformStateViaUpgrade` wraps both
   `UnmarshalRawStateDelta` and `Recover` in `contract.AssertNoErrorf`, which panics
   unconditionally. The `validateRecover` call injection performs before writing a delta is the
   only thing between a bad delta and that crash — load-bearing, not belt-and-braces.

## Scope: this tool is not AWS-only

AWS is the only provider with an e2e fixture today, and **Azure, GCP and Kubernetes are planned**.
Coverage must be driven by what the delta format can express, not by what one provider happens to
exercise. An earlier draft of this plan made that mistake — it dismissed cases as unreachable
after measuring only `terraform-provider-aws`.

Concretely, measured against terraform-provider-aws 5.100.0 and worth recording as *context*
rather than as scope:

| Case | AWS 5.100.0 | Why it still needs coverage |
|---|---|---|
| `DynamicPseudoType` (→ `replace` node) | **0** of 1526 resource types | Kubernetes is built on it — `kubernetes_manifest` is dynamic by design |
| Large integers | rare | GCP project numbers, quotas, byte counts |
| Deep `MaxItems=1` nesting | present | Azure is dense with it |
| Large maps / set semantics | tags only | Kubernetes labels and annotations |

### The vehicle: synthetic schemas, not provider hunting

`ComputeInjectionState` needs exactly one thing from the provider — `GetProviderSchema`. So a test
fake can embed `providers.Interface` and override that single method:

```go
type fakeProvider struct {
    providers.Interface   // embedded: satisfies the type, panics if anything else is called
    schema providers.GetProviderSchemaResponse
}
func (f *fakeProvider) GetProviderSchema(context.Context) providers.GetProviderSchemaResponse
func (f *fakeProvider) Name() string
func (f *fakeProvider) Version() string
```

That is ~8 lines and unlocks arbitrary shapes — including dynamic types AWS does not have —
with **no provider binary, no network and no cloud**. It is also how the bridge tests its own
delta code. Real-provider tests stay for the cases where the real schema is the point (nested
blocks, `timeouts`), but they should not be the only tool available.

## What the bridge already tests — do not duplicate

`pulumi-terraform-bridge/v3@v3.121.0/pkg/tfbridge/rawstate_test.go` (~1300 lines):

| Test | Cases | Level |
|---|---|---|
| `Test_rawstate_delta_turnaround` | 27 | hand-built `schema.Schema` + PropertyValue/cty pairs |
| `Test_rawstate_against_MakeTerraformOutputs` | 20 | autogold snapshots of the produced delta |
| `Test_rawstate_delta_serialization` | 7 | JSON shape of each node kind |
| `Test_rawStateReducePrecision`, `Test_isSimilarNumber` | — | number handling |

Its cases already cover nulls; numbers including big-int-as-string vs as-f64; empty and unequal
strings; bools; `MaxItems=1` for object and set; empty set/list/map; null-vs-empty-map both ways;
values inside list/set/map; objects with nulls missing on the Pulumi side; and ignored Pulumi keys.

**That primitive space is well covered.** Re-testing it costs maintenance and yields nothing.

## What is ours to cover — the seam

```
Terraform state JSON → cty → pulumiOutputsFromCty (MakeTerraformOutputs, supportsSecrets=false,
  assets=nil) → RawStateComputeDelta → json.Marshal → SIDECAR → LoadNonImportableFile →
  attachRawStateDelta (+redaction, +envelope) → deployment JSON → engine → bridge Diff/Read/Update
```

Every arrow is ours, and every failure found this week lived at one: the `timeouts` type/value
mismatch, `Marshal().Mappable()` corrupting Replace nodes, the missing secret envelope.

### Acceptance criterion

**The recovered raw state must equal the original attributes exactly**, not merely apply without
error. Where affordable, a second gate: the real provider's `UpgradeResourceState` must accept the
reconstruction. Measured scope of that gate — it rejects wrong attribute types, non-object JSON and
malformed JSON, but **accepts an empty object and one missing required attributes**. Structural and
type checking, not exhaustive, and it should not be described as more.

### On comparators — this went wrong twice in one sitting

The shared helper `assertDeltaRecoversExactly` was wrong two different ways before it was right,
and both would have produced **passing tests that proved nothing**:

1. It compared a 4-attribute fixture against schema-complete recovered state, so it failed on
   absent-but-null attributes rather than on anything real.
2. It decoded both sides with plain `json.Unmarshal`, turning `9007199254740993` into a float64 on
   *both* sides — silently destroying the precision the test existed to check.

The two rules that fixed it, and that any future sweep must also follow: **decode with `UseNumber`
on both sides**, and **compare against schema-complete state** — every attribute the fixture
specifies must match exactly, any extra recovered attribute must be null. A comparator must also
name the differing attribute, never return a boolean: a first breadth-sweep prototype reported 5
mismatches of 10 types, and careful per-attribute comparison of one showed 0 differing attributes
of 23. That finding was withdrawn, not filed.

## Tier 1 — one case per delta node kind

Node kinds: `plu` (MaxItems=1), `map`, `obj` (with `ignored`/`renamed`), `arr`, `asset`, `num`,
`replace`, plus absence-of-node routing to `rawStateRecoverNatural`.

| Node kind | State | Notes |
|---|---|---|
| `obj` | covered | RandomPet, nested-block, patch-group |
| `plu` | covered | nested-block, two levels |
| `map` | **done** | populated tags; every earlier fixture had empty maps |
| `arr` | partial | only empty lists so far |
| `replace` | **partly done** | reached via precision loss, NOT via JSON strings — a policy document is a `TypeString` and round-trips naturally, producing `{"obj":{}}`. The plan's first draft named policy documents as the trigger; that was wrong. The other trigger, `schType.IsDynamicType()`, needs a **synthetic schema** — see below |
| `num` | not covered | needs a number-typed TF value against a string-typed Pulumi value; ordinary integers round-trip naturally |
| `asset` | not covered | `pulumiOutputsFromCty` passes `assets: nil`; establish reachability before writing a test |
| `renamed` | incidental | observed in 4 of 10 probed types, never asserted |
| natural | covered | RandomPet |

**`replace` via dynamic types is the highest-value gap**, because Kubernetes is coming and
`kubernetes_manifest` is dynamic throughout. A synthetic schema with a `cty.DynamicPseudoType`
attribute tests it today, without waiting for a Kubernetes fixture.

## Tier 2 — the properties that have actually bitten

1. **Large integers — done, and it refined #29.** Above 2^53 the *sidecar* keeps exact digits (the
   bridge emits a Replace node carrying them), but *recovery* loses them either way: plain decode
   gives a rounded number, `UseNumber` gives an exact **string**, because `json.Number` has no
   `PropertyValue` case. Neither reproduces a Terraform number. So #29 needs a representation
   change, not a decoding change — adding `UseNumber` downstream cannot help.
2. **Secrets and the delta.** Assert `attachRawStateDelta`'s `(sensitive)` screen, and assert a
   `replace` node arrives in state enveloped and survives `RemoveSecrets`.
3. **`ignored` / the `region` case.** Pin the behaviour #30 theorised about, so it cannot silently
   change.
4. **`timeouts`.** Covered in three subtests; extend to a type with populated timeouts *and*
   nested blocks.
5. **Empty vs null at our seam** — `null` vs absent key vs `[]` vs `{}` vs `""` *after* the sidecar
   round trip, which is where `omitempty` lives. The bridge covers this at schema level, not after
   JSON.

## Tier 3 — breadth sweep over injectable types

Population: non-importable types, enumerable offline via `importsupport.Prober`. For each,
synthesize a value from `sch.Block.ImpliedType()`, compute, round-trip, recover, compare per
attribute, report differing attribute names.

Prerequisites, both now met or known: the comparator rules above, and honest accounting of what was
skipped (types that fail to marshal, or have no bridged schema, must be counted and reported).
Populate values from the schema rather than using all-null — an all-null value exercises the walk
but almost nothing else. Run behind a build tag or a cap; it needs the provider binary but no cloud.

Worth running per provider as Azure, GCP and Kubernetes are added — it is the cheapest way to learn
what a new provider's schemas do to this pipeline.

## Tier 4 — end to end

**The e2e already exercises delta consumption**, which the first draft understated: the
post-injection preview runs `Diff`, `Diff` consumes the delta, the sdk-v2 shim implements
`ProviderWithRawStateSupport` (`sdk-v2/provider2.go:540`), and the assertion panics unconditionally.
Runs 16 and 17 (`Deltas attached (injected): 11 of 11`, all `same`, no crash) are real evidence.

1. **Assert `Deltas attached (injected): X of Y` equals `Y of Y`.** Nothing checks it, so a
   regression to `0 of 11` would pass silently — a missing delta degrades to the legacy conversion
   rather than failing.
2. **`CorruptDeltaFailsPreview` — done.** Corrupts one injected resource's delta and requires the
   next preview to fail. Without it, "previews as same" was equally consistent with the delta path
   never being taken. The payload's potency is pinned offline so the scenario cannot quietly become
   a false alarm.
3. **A resource type whose delta is non-trivial.** Every fixture non-importable type is schema
   version 0 and structurally flat. `aws_ssm_patch_group` is the only AWS type that is both
   non-importable and schema-versioned, and is free and trivial to create.

## Explicitly out of scope

- Re-testing the bridge's primitive turnaround space.
- A real provider-version-pair upgrade test: `aws_ssm_patch_group` already had `SchemaVersion: 1`
  at terraform-provider-aws v4.0.0 (Feb 2022), so no modern `pulumi-aws` pair straddles the bump.

## Order

Remaining, cheapest first: Tier 1 `num`/`asset`/`renamed`/`arr` and the **dynamic-type `replace`
case** (synthetic schemas, no cloud) → Tier 2 items 2–5 → Tier 4 item 1 → Tier 3.
