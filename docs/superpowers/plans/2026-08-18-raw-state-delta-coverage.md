# Raw state delta — test coverage plan

Status: revised 2026-08-18 (twice). Tiers 1-4 substantially IMPLEMENTED; what remains is listed
under "Still open" at the end. Several of this plan's own first-draft claims were disproved by
measurement and have been corrected in place.

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
| `obj` | **done** | RandomPet, nested-block, patch-group |
| `plu` | **done** | nested-block, two levels |
| `map` | **done** | populated tags; every earlier fixture had empty maps |
| `arr` | **done** | populated lists, sets, and per-index element deltas via the nested test |
| `replace` | **done** | two routes covered: `DynamicPseudoType` (synthetic — Kubernetes's case) and precision loss. NOT triggered by JSON strings: a policy document is a `TypeString` and round-trips naturally to `{"obj":{}}`. This plan's first draft named policy documents as the trigger; that was wrong |
| `renamed` | **done** | schema-override rename, plus the pluralization case |
| natural | **done** | RandomPet |
| `num` | **UNREACHED** | needs a number-typed TF value against a string-typed Pulumi VALUE. Declaring the shim schema `TypeString` is not enough — `MakeTerraformOutputs` still yields the number. Recorded in the test rather than papered over |
| `asset` | **open** | `pulumiOutputsFromCty` passes `assets: nil`; reachability never established |

Also covered, and NOT in this plan's first draft:

- **Sets** — had zero coverage anywhere; ubiquitous (AWS security groups, Kubernetes, Azure) and the
  case most likely to break quietly, since Terraform treats a set as unordered while its JSON
  encoding is an ordered array.
- **Pluralization** — the bridge renames `cidr_block` to `cidrBlocks` and `PulumiToTerraformName`
  cannot invert it. 513 pulumi-aws attributes fail to round-trip through that transform, which is
  what made `resolveSecretInputs` silently delete program-declared inputs.
- **Nested combinations** — objects in lists in objects, a map two levels down, a set of objects.
  Where the mechanisms interact, which is where a depth-2 mistake hides from depth-1 tests.

`replace` via dynamic types was the highest-value gap and is now closed with a synthetic schema —
covered today rather than waiting for a Kubernetes fixture.

## Tier 2 — the properties that have actually bitten

1. **Large integers — done, and it refined #29.** Above 2^53 the *sidecar* keeps exact digits (the
   bridge emits a Replace node carrying them), but *recovery* loses them either way: plain decode
   gives a rounded number, `UseNumber` gives an exact **string**, because `json.Number` has no
   `PropertyValue` case. Neither reproduces a Terraform number, so #29 needs a representation
   change, not a decoding change.
2. **Secrets and the delta — done.** The `(sensitive)` screen in `attachRawStateDelta`, and a
   `replace` node arriving in state enveloped and surviving `RemoveSecrets`.
3. **`ignored` / the `region` case — done, and it corrected #30.** A Pulumi-only property is NOT
   dropped; `objDelta.Ignored` records keys absent at delta-*computation* time, and one added later
   by `fillOutputsFromInputs` was never there to record, so it recovers naturally — written straight
   into the reconstructed raw state. Harmless only because providers ignore undeclared attributes,
   which is now asserted against a real provider rather than assumed.
4. **`timeouts` — partly.** Three subtests including a populated block. Still open: combined with
   nested blocks.
5. **Empty vs null at our seam — done.** `null` vs `""` vs `[]` vs `{}` through the sidecar's JSON
   round trip, where `omitempty` and Go zero values blur what the bridge kept separate.
6. **Sensitive INPUT resolution — done, synthetically.** The full path: a Terraform-sensitive
   attribute redacted out of the digest, its real value written to stack config, and injection
   resolving it back as a Pulumi secret envelope — plus the failure direction when config lacks the
   key. This had no coverage at any level after `caPem` was removed in `addec9a`.

## Tier 3 — breadth sweep over injectable types — DONE

`TestDeltaSweep`, behind the `deltasweep` build tag. Enumerates every non-importable type via
`importsupport.Prober`, synthesizes a populated value from its schema, and requires exact
reconstruction. **14 of 1526 AWS types are non-importable; all 14 round-trip exactly.**

It names the differing attribute rather than returning a boolean, and accounts for what it did NOT
exercise. It found zero product defects and three harness bugs of mine, each of which would have
been a false alarm — including one where `NewPropertyMapFromMap` (no `json.Number` case) turned
every number into a String property before recovery. The rule that came out of it: **use the
production converters, not convenient equivalents.**

Run it per provider as Azure, GCP and Kubernetes are added. It is the cheapest way to learn what a
new provider's schemas do to this pipeline, and it needs no cloud credentials.

## Tier 4 — end to end — DONE

1. **`Deltas attached (injected): X of Y` is asserted.** Live in run 18: *"confirmed all 11 injected
   resource(s) carried a raw-state delta"*. A regression to `0 of 11` would otherwise pass every
   scenario silently.
2. **`CorruptDeltaFailsPreview` — done, and it passed live in run 18.** Corrupting one delta made
   the preview **panic the provider**:
   `Failed to parse raw state markers ... archiveFormat of type archive.Format`. That converts every
   other delta assertion in the suite from "consistent with correctness" to "sensitive to it", and
   confirms from production — not from reading source — that the delta path is live and that
   `contract.AssertNoErrorf` really panics.
3. **`aws_ssm_patch_group` as a fixture resource** — still open.

## Still open

| Item | Note |
|---|---|
| `asset` node | reachability never established — `pulumiOutputsFromCty` passes `assets: nil` |
| `num` node | confirmed unreachable by this pipeline; documented in the test rather than papered over |
| `timeouts` + nested blocks combined | Tier 2 #4 |
| `aws_ssm_patch_group` fixture | Tier 4 #3 |
| A sensitive-input **e2e** | the logic is covered synthetically; nothing exercises it against a live provider. AWS has no natural candidate — of its 14 non-importable types exactly one has a Terraform sensitive input (`aws_cloudcontrolapi_resource.schema`) — so this likely waits for a non-AWS fixture |
| Azure / GCP / Kubernetes fixtures | the synthetic harness covers the shapes; nothing exercises a real schema from those providers |

## Explicitly out of scope

- Re-testing the bridge's primitive turnaround space.
- A real provider-version-pair upgrade test: `aws_ssm_patch_group` already had `SchemaVersion: 1`
  at terraform-provider-aws v4.0.0 (Feb 2022), so no modern `pulumi-aws` pair straddles the bump.

## A note for whoever adds the next provider

Four times in one session, a measurement taken only against AWS was about to become a conclusion
about the tool. Each was caught, but the pattern is the lesson: **"unreachable in AWS" is a fact
about AWS.** `DynamicPseudoType` has zero instances in terraform-provider-aws and is what
`kubernetes_manifest` is built on. Sensitive inputs appear on one non-importable AWS type and are
common elsewhere. Reach for a synthetic schema before concluding a case cannot happen.
