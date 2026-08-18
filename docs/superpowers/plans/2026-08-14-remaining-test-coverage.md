# Remaining test coverage for state injection

Companion to `2026-08-14-non-importable-state-injection.md`. That plan built the feature; this one records what its tests still do not cover, and why each gap is worth closing.

## Why this list exists

Nine per-task reviews, two whole-branch reviews on the most capable model, and a green unit suite did not find any of the following:

| Defect | Broke | Found by |
|---|---|---|
| `Base64SecretsProvider` rejects real secrets provider types | every real deployment | e2e run 1 |
| `region` absent from Terraform-derived outputs | every Pulumi-only property | e2e run 3 |
| `timeouts` type/value asymmetry | ~34% of AWS resource types | e2e run 5 + instrumentation |
| null sensitive attribute redacted but never written to config | every resource with an unset optional sensitive field | writing a second e2e scenario |

Each broke the feature outright for a whole class of input, and each was invisible to unit tests for the same structural reason: **the fixtures encoded assumptions about shapes nobody had checked against reality** — CLI output, deployment structure, provider schema across two versions, and the interaction between two sensitivity implementations.

The fourth is the most economical result on the list. It was found while *writing* a fixture, before any cloud run. Coverage that resembles production pays off at fixture-writing cost, not only at execution cost.

## What exists today

`test/e2e/` runs one `tofu apply` and shares it across scenarios (`provisionStack` gives each its own Pulumi stack, so the marginal cost of a scenario is seconds):

- injection: non-importable resources preview `create` before and `same` after
- revert: a deliberately corrupted injection reverts, and the export is compared to the backup
- idempotence: a second identical run
- file mode: `--state`/`--out` with `--preview-json`
- patch-only stack mode: no `--non-importable`
- secrets: `aws_iot_certificate`, sensitive attributes both populated and null
- nested blocks: `aws_vpclattice_target_group_attachment`
- classification: every diverted resource is confirmed to genuinely fail `pulumi import`
- pre-existing drift: a dirty baseline, closing gap 4 below

## Status as of 2026-08-18

This list predates e2e runs 11-18 and the delta coverage work. Current state:

| Gap | Status |
|---|---|
| 0. Secrets provider other than `passphrase` | **CLOSED** — `KMSSecretsProvider` scenario |
| 1. Multiple providers or regions | **PARTLY** — an aliased `us-east-1` provider is covered; a second *region of the same resources* is not |
| 2. Injected resources that depend on each other | **CLOSED** — `ProviderAndDependencyEdges`, and the dependency is what surfaced the unknown-sentinel bug in run 15 |
| 3. `for_each` rather than `count` | **CLOSED** — `each["alpha"]`/`each["beta"]` in the fixture |
| 4. Pre-existing drift | **CLOSED** — `InjectionSurvivesPreExistingDrift` |
| 5. Provider version upgrade | **NOT BUILDABLE as written** — `aws_ssm_patch_group` already had `SchemaVersion: 1` at terraform-provider-aws v4.0.0 (Feb 2022), so no modern `pulumi-aws` pair straddles the bump. Partly served instead by a real-provider `UpgradeResourceState` test |
| 6. The CFN path | **MOVED** — now #33 (no classification or injection at all) and #32 (shared runner) |
| 7. Component parents | **CLOSED** — `ComponentParent`, a `toolimport:index:Certs` component |
| 8. Interrupted run | **STILL OPEN** |
| Sensitive INPUT resolution | **LOGIC CLOSED, E2E OPEN** — covered synthetically end to end after `caPem` was removed in `addec9a`. AWS has no natural fixture candidate: of its 14 non-importable types exactly one has a Terraform sensitive input (`aws_cloudcontrolapi_resource.schema`) |
| Delta correctness across more types | **CLOSED** — `TestDeltaSweep` covers all 14 non-importable AWS types, 14/14 exact. See the delta coverage plan |

Two scenarios exist now that this list did not anticipate:

- `ClassificationIsNotOverBroad` — attempts `pulumi import` on each sidecar resource and fails if
  any imports AND previews as `same`. Nothing previously asserted that injection was NECESSARY
  rather than merely sufficient.
- `CorruptDeltaFailsPreview` — corrupts an injected delta and requires the next preview to fail,
  which is what makes every other delta assertion in the suite meaningful rather than merely
  consistent with correctness.

Delta-specific coverage has its own plan:
`docs/superpowers/plans/2026-08-18-raw-state-delta-coverage.md`.

**A caveat that applies to this whole list:** it is written against the AWS fixture, and the tool is
not AWS-only — Azure, GCP and Kubernetes are planned. Several gaps here are AWS-shaped ("no natural
candidate") rather than genuinely closed, and a new provider may reopen them.

## Gaps, in priority order

### 0. A secrets provider other than `passphrase`

**What.** The e2e end to end against a stack whose secrets provider is `service` (or `awskms`), not `passphrase`.

**Why first.** This is the exact class of bug that has already bitten, and the fixtures written to prevent it are the same kind of artifact that hid it. E2E run 1 found that `VerifyDeploymentIntegrity` used `stack.Base64SecretsProvider`, whose `OfType` errors for any type but `b64` — so the function had **never worked against a real deployment**, and nine per-task reviews, a whole-branch review and the full unit suite all missed it, because every unit fixture carried no `secrets_providers` block. The fix (`f11e849`) added passphrase/service/encrypted-value fixtures — hand-written ones. Real customers run `service`.

Note the variable is the **secrets provider**, not the backend. They are orthogonal: `passphrase` runs on the service backend and `service`/`awskms` run on either. Switching backends alone would prove nothing.

**Cost.** Awkward, and the reason this is not simply done. `service` needs a Pulumi Cloud org and token — precisely the dependency the file backend was chosen to avoid (`e2e_test.go:137-155`), and an earlier version of this test gated on `PULUMI_ACCESS_TOKEN` and silently skipped, which is a skip that reads as a pass. `awskms` avoids the cloud dependency and uses credentials we already have, but needs a KMS key, which is a real billed resource in the fixture. Either way this is a deliberate tradeoff rather than a free addition — decide the mechanism before writing it.

### 1. Multiple providers or regions

### 1. Multiple providers or regions

**What.** A fixture with two AWS provider configurations (two regions, or two aliased providers) and non-importable resources under each.

**Why first.** The provider reference is the one field the sidecar cannot carry — the uuid in `urn:…::pulumi:providers:aws::default_7_24_0::<uuid>` exists only in the target stack. Injection takes it from the preview's create step precisely so it does not have to guess, and the design explicitly claims this resolves correctly when several provider instances exist. **That claim has never been tested.** A wrong provider reference silently targets the wrong region or account, and it is the failure recorded as issue 3 of #11 — it has happened in the field.

**Cost.** Two regions of the existing cheap resources. No VPN connection needed in the second region.

### 2. Injected resources that depend on each other

**What.** Two non-importable resources where one references the other, so `orderInjected`'s topological sort actually has work to do.

**Why.** `VerifyIntegrity` rejects a resource whose dependency appears later in the array. The current fixture's four non-importable resources are mutually independent, so the sort has never been exercised against real dependency edges — the code path runs, but always on an already-valid order. The unit test covers it; nothing confirms the edges the engine actually emits look like the ones the sort expects.

**Cost.** Negligible if a suitable pair exists; needs a short search of non-importable types.

### 3. `for_each` rather than `count`

**What.** The same non-importable resources declared with `for_each` over a map.

**Why.** Terraform addresses differ (`prop["a"]` vs `prop[0]`), and those addresses flow through `flattenAddress` into stack config keys, through `resolve tf` into import-file names, and into the type+name matching that pairs a sidecar entry to a preview create step. The Pulumi YAML fixture already needed a bracket-name workaround for `count`; `for_each` produces quoted keys, which is a different shape again. A mismatch here fails loudly, but at a point far from its cause.

### 4. A stack with genuine pre-existing drift — CLOSED

**Closed by** `InjectionSurvivesPreExistingDrift`. The scenario adds a tag to the target group in the copied Pulumi program, asserts the baseline is genuinely dirty (so it cannot pass vacuously), then requires that the run succeed anyway, that the injected resources still settle to `same`, and that the pre-existing diff still be there afterwards — tolerated, not silently resolved.

Drift is introduced in the program rather than out-of-band because that is what a real mid-migration stack looks like: more program written than patched into state. It targets an importable resource, so it cannot interact with injection.

### 5. Provider version upgrade

**What.** Inject with provider version N, then upgrade to N+1 and preview.

**Why.** This is the *entire purpose* of the raw state delta. Everything about deltas — the `digest tf` provider work, `RawStateComputeDelta`, the `timeouts` fix, issue #30 — exists so an injected resource can be handed to a provider's state-upgrade function. Nothing has ever tested that it can. Note this only bites when a resource type's `SchemaVersion` actually changes between the two versions, so the fixture must pick a type where that is true.

**Cost.** Higher than the others: needs two provider versions and a type with a real schema migration.

### 6. The CFN path

**What.** Any end-to-end coverage of `digest cfn` → `resolve cfn` → `patch-state cfn`.

**Why.** It has none. It shares `patchAndValidateResource` with the tf path — including delta editing and `validateRecover` — and `6ac03f6` changed its digest decode, which surfaced the `isSimpleValue` bug. CFN targets aws-classic, the same bridged provider, so the same delta machinery applies. The one structural difference is that its live reads go through Cloud Control rather than a Terraform provider.

**Cost.** Higher: needs a deployed CloudFormation stack as the fixture.

### 7. Component parents

**What.** An injected resource whose parent is a component, not the stack root.

**Why.** The URN's qualified type becomes `parentType$childType`, and `VerifyIntegrity` warns (does not error) when a child's URN does not match its parent's. Injection takes the parent from the preview create step, so this should work — but "should" is what the last four defects had in common. Real migrations map Terraform modules to components, so this is the common shape, not an edge case.

### 8. Interrupted run

**What.** Kill the process between `Import(injected)` and the verifying preview, then confirm the printed recovery command actually restores the stack.

**Why.** The backup-before-mutation ordering and the absolute-path recovery command were designed for exactly this, and reviewed carefully. Neither has been executed. The recovery command in particular was found during review to be capable of importing into the *wrong stack* when it omitted `--cwd`/`--stack`; the fix is untested.

**Cost.** Awkward to automate — needs a process kill at a specific point. Possibly better as a documented manual procedure than a test.

## Sensitive INPUT resolution, end to end

**Gap.** No property in the e2e fixture exercises `resolveSecretInputs`
(`pkg/state_injector.go`) against a real preview any more. `caPem` did, while
`testdata/pulumi/Pulumi.yaml` declared it — but that made the program disagree
with the Terraform config it is supposed to be a translation of (`main.tf`
deliberately leaves `ca_pem` unset), and since `ca_pem` is ForceNew the
certificate previewed as `replace` and reverted every run. Removing it was the
right call; it took this coverage with it.

**What is still covered.** The unit tests
(`TestInjectNonImportable_ResolvesSecretFromConfig`, `_FillWrapsSecretInput`,
`_DropsMaskedSecretWithNoTerraformValue`) cover the resolution logic. What is
gone is the end-to-end path: a real `pulumi preview --json` masking a real
secret input as `[secret]`, resolved from real stack config, landing enveloped
in real state. That combination is what produced the 2026-08-14 failure, and
unit fixtures cannot produce it because they hand-write the preview.

**What it needs.** A non-importable resource with a Sensitive attribute that
is a genuine *input* holding a real value. The certificate's other three
Sensitive attributes are outputs, and the VPN connection's pre-shared keys sit
on an importable resource. This may need a fixture resource added for the
purpose rather than one already present.

## Unit-level gaps

- ~~**`orderInjected` cycles**~~ — CLOSED by `pkg/state_injector_order_test.go`, which also supplies the transitive chains, diamonds, parent edges, self-references and out-of-batch edges the sort had never been given. The cycle case pins the degradation rather than a correct answer (there isn't one): it must terminate, must not drop or duplicate a resource — `VerifyDeploymentIntegrity` is the backstop and can only work if everything is still present — and must leave at least one forward reference for that backstop to catch.
- ~~**`checkNoPlaceholders` depth**~~ — CLOSED by `TestCheckNoPlaceholders_NestedDepth`: maps inside arrays inside maps, four levels deep, arrays of arrays, and a placeholder in a later sibling after clean branches. Each asserts the reported **path**, not just detection — an operator told only "a placeholder is somewhere in this resource" cannot act on it. A clean-value case guards against a check that always errors.
- ~~**The CFN patch path with `json.Number`**~~ — CLOSED by `TestPatchStateFromCFN_LargeIntegerKeepsExactDigits`, using 2^53+1 (the smallest integer float64 cannot represent) and asserting on the raw output bytes rather than the decoded value, since the failure mode is re-serialization. Its `decodeWithUseNumber` helper exists because building attributes as Go literals tests a different type than production handles — which is how the `reflect.DeepEqual` regression fixed in `9d0c4bf` reached review.
- **Delta correctness across more types.** The bridge's turnaround check is the arbiter, and it only runs at computation time. Coverage today is two types; the `timeouts` bug showed that a whole schema feature can silently suppress deltas.

## Properties that cannot be tested here, and should be said rather than implied

- **Value correctness against the cloud.** `pulumi preview` reporting zero operations is the strongest available signal, and it is what these tests assert. It does not prove the values match the cloud resource in every respect — only that the provider's diff is empty. `pulumi refresh` is not a substitute; it reports these types unchanged even when the values are wrong.
- **Integer precision above 2^53** on the injection path (#29). `resource.PropertyValue` has no exact-integer representation, so this is a property of the data model rather than a bug a test can drive.
- **A non-string sensitive attribute.** The digest's `ctyjson.Unmarshal` fails for one, so the resource silently gets no injection state. Worth a unit test asserting the degradation is reported; an e2e needs a type with a non-string sensitive field, which may not exist.
- **Byte-exact state restoration.** `RevertRestoresStackExactly` can only ever assert that a reverted stack matches *decrypted*. `pulumi stack import` decrypts and re-encrypts unconditionally — verified locally: importing the *same ciphertext* twice yields different ciphertext each time, so this is not an artifact of `--show-secrets` or of the file backend. Every secrets provider uses a random nonce, so identical plaintext never re-encrypts to identical bytes. The stored bytes after a revert are therefore always different, and "the revert restored the stack exactly" can only mean "every decrypted value matches". That is a property of the system, not a gap to close. It is also why `canonicalStackExport` passes `--show-secrets`, and why the failure branch prints differing JSON paths and never values.

## Recommended order

Gap 4 and every unit-level gap but the last are now closed, as is the sensitive-input gap's unit half.

What remains, in order: **0** first if a mechanism can be agreed — it targets the only class of bug that has already reached a real deployment undetected, and the decision (cloud token vs. a billed KMS key) is a judgement call, not a coding one. Then **1** and **2**, both cheap once the fixture grows, both with field evidence behind them. **3** and **7** after. **5** and **6** are projects rather than scenarios and deserve their own scoping — **6** in particular is bounded by the shared-runner work in issue #32, since a CFN e2e wants the source interface to exist first rather than a second copy of this file.

Everything from 1 onward needs the real-AWS fixture to grow, which adds apply/destroy time, cost, and orphan risk to every run of the suite. That is worth deciding deliberately rather than accreting.
