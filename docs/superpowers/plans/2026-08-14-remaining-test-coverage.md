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

## Gaps, in priority order

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

### 4. A stack with genuine pre-existing drift

**What.** Modify a resource out-of-band (or leave an unrelated diff outstanding), then run patch-only stack mode.

**Why.** `CheckInjectionVerification` compares a baseline preview against the post preview and requires that the run not make things worse. Every run so far started from a stack whose only diffs were the non-importable creates. **A dirty baseline has never been seen.** This is the guard that replaced a vacuous check, and its whole purpose is to tolerate exactly this case — the case it has never met.

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

- **`orderInjected` cycles** return silently and produce an order that fails `VerifyIntegrity` with an opaque message. Unreachable from a real preview, but untested.
- **`checkNoPlaceholders` depth** — the sweep recurses through maps and slices; no test uses a deeply nested placeholder.
- **The CFN patch path with `json.Number`** — `6ac03f6` changed the decode and `8d94094` fixed `isSimpleValue`, but no CFN test carries a large integer through `patchAndValidateResource`.
- **Delta correctness across more types.** The bridge's turnaround check is the arbiter, and it only runs at computation time. Coverage today is two types; the `timeouts` bug showed that a whole schema feature can silently suppress deltas.

## Properties that cannot be tested here, and should be said rather than implied

- **Value correctness against the cloud.** `pulumi preview` reporting zero operations is the strongest available signal, and it is what these tests assert. It does not prove the values match the cloud resource in every respect — only that the provider's diff is empty. `pulumi refresh` is not a substitute; it reports these types unchanged even when the values are wrong.
- **Integer precision above 2^53** on the injection path (#29). `resource.PropertyValue` has no exact-integer representation, so this is a property of the data model rather than a bug a test can drive.
- **A non-string sensitive attribute.** The digest's `ctyjson.Unmarshal` fails for one, so the resource silently gets no injection state. Worth a unit test asserting the degradation is reported; an e2e needs a type with a non-string sensitive field, which may not exist.

## Recommended order

1 and 2 next — both are cheap, both target failures with field evidence. 4 after them, since it validates a guard written to replace a defect. 5 and 6 are projects rather than scenarios and deserve their own scoping.
