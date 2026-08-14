# How schema and state travel through the import pipeline

A reference for the two things the tool moves: **state** (resource values, from
Terraform to a Pulumi deployment) and **schema** (provider metadata, needed to
rename and reshape those values). It records, at each hop, what representation
the data is in, what is added, what is dropped, and what cannot be recovered
afterwards.

This is a diagnostic document. Where a stage is lossy or a dependency is
awkward, it says so; where a claim could not be established from the source, it
says that instead of guessing. Line numbers are against
`feat/inject-non-importable-state` and against
`github.com/pulumi/pulumi-terraform-bridge/v3@v3.121.0` /
`github.com/pulumi/pulumi/pkg/v3@v3.222.0` for the vendored modules.

Background that is *not* repeated here:
[`docs/non-importable-resources.md`](non-importable-resources.md),
[`docs/aws-import-diff-fields.md`](aws-import-diff-fields.md), and the design
spec `docs/superpowers/specs/2026-08-13-non-importable-state-injection-design.md`.

## Orientation

The pipeline is five commands and one document that survives between them:

```
terraform.tfstate ──digest tf──▶ tf-digest.json ──resolve tf──▶ imports-ready.json
                                       │                        imports-ready.non-importable.json
                                       │                                │
                                       │                            import
                                       │                                ▼
                                       └──────patch-state tf──▶ pulumi stack export
                                                                        │
                                                              pulumi stack import
```

`tf-digest.json` is the pivot. It is read by `resolve tf`
(`cmd/import_id_match.go:84`) and again by `patch-state tf`
(`cmd/patch_state_tf.go:78`), and everything either command knows about
Terraform values comes from it. Nothing downstream re-reads Terraform state.

Two provider loaders run, for different reasons, and they never see each other's
results except through `pulumiProviders` being passed into `populateInjectionState`:

| Loader | Started by | Protocol | Purpose |
|---|---|---|---|
| `tfprovider.LoadProvider` (`pkg/tfprovider/loader.go:65`) | `importsupport.Prober` (`pkg/importsupport/prober.go:184`), `BuildSensitivityMap` (`pkg/provider_schema.go:77`) | go-plugin, real Terraform provider binary | `ImportResourceState` probe; `GetProviderSchema` for cty types |
| `PulumiProvidersForTerraformProviders` (`pkg/pulumi_providers.go:75`) | `GenerateModuleMap` (`pkg/generate_module_map.go:116`, `:131`) | Pulumi plugin gRPC, `GetMapping("terraform")` | Pulumi type tokens and property names |

See [#26](https://github.com/pulumi-proserv/pulumi-tool-import/issues/26).

---

## State, stage by stage

### S1 — Terraform state file → in-memory state

Two entry formats, detected by the presence of `format_version`
(`pkg/tofu_eval.go:53`).

**Raw `.tfstate`** goes through OpenTofu's own reader,
`statefile.Read` (`pkg/tofu_eval.go:147`), producing `*states.State`. Each
instance keeps:

- `inst.Current.AttrsJSON` — the **original bytes**, untouched. Number
  fidelity is intact at this point.
- `inst.Current.AttrSensitivePaths` — `[]cty.PathValueMarks`, Terraform's own
  record of which attribute paths are sensitive.
- the instance key (`res.Instances` is keyed by `addrs.InstanceKey`).

**`tofu show -json`** goes through `json.Unmarshal` into `tfjson.State`
(`pkg/generate_module_map.go:125`), then `rawStateFromTfjson`
(`pkg/generate_module_map.go:259`) rebuilds a synthetic `*states.State` by
re-marshalling `r.AttributeValues` (`:296`).

That second path loses three things at once:

1. **Numbers.** `json.Unmarshal` without `UseNumber` turns every JSON number
   into `float64`; re-marshalling a 19-digit integer yields a rounded value
   (`1234567890123456789` → `1234567890123456800`). This is the first hop of a
   number-fidelity problem that recurs throughout (see
   [Weaknesses](#weaknesses-and-open-questions)).
2. **Sensitivity.** `tfjson.StateResource.SensitiveValues`
   (`terraform-json@v0.27.1/state.go:164`) is never read.
   `ResourceInstanceObjectSrc` is built with only `AttrsJSON`
   (`pkg/generate_module_map.go:302`), so `AttrSensitivePaths` is empty. Every
   downstream consumer of sensitivity — `redactSensitivePaths`,
   `DiscoverSensitiveSecrets`, `redactedAttributeKeys` — silently finds nothing
   on this path. **Secrets are written to the digest in plaintext.**
3. **Instance keys.** The resource instance is always registered under
   `addrs.NoKey` (`pkg/generate_module_map.go:301`), and
   `tfjson.StateResource.Index` (`state.go:142`) is not read. Two instances of a
   counted or `for_each` resource in the same module collide on the same key;
   the last one visited wins.

`LoadTerraformState` (`pkg/tofu/loader.go:73`) is a third entry point that
shells out to `tofu`; it is used by the `--state-file`-less flows and does a
`registry.terraform.io/` → `registry.opentofu.org/` textual rewrite of the state
JSON (`pkg/tofu/loader.go:285`) when OpenTofu cannot resolve Terraform-registry
provider references. That rewrite is applied to the whole document as a string,
so it would also rewrite the substring inside an attribute value. In practice
that string appears only in provider references; it has not been observed to
corrupt an attribute, and no guard exists.

### S2 — in-memory state → `ModuleMap` (the digest)

`matchResources` (`pkg/module_map.go:327`) walks the state and emits one
`ModuleResource` (`pkg/module_map.go:53`) per current instance.

| Field | Source | Notes |
|---|---|---|
| `TerraformAddress` | `res.Addr` + instance key + module addr (`:356-362`) | The join key for everything downstream. |
| `ImportID` | `attrs["id"]` via `fmt.Sprintf("%v", id)` (`:370`) | Stringified. For a numeric id decoded as `float64`, this yields scientific notation. |
| `Attributes` | `json.Unmarshal(AttrsJSON)` (`:368`), then redacted | **No `UseNumber`.** |
| `TranslatedURN` | `buildResourceURN` (`:525`) | Needs the Pulumi provider mapping; falls back to the raw TF address when absent (`:533`, `:539`, `:544`). |
| `Mode` | `managed` / `data` (`:376`) | Data sources get no URN and are never imported. |
| `NonImportable` | `importChecker.Check` (`:406`) | See S2b. |
| `PulumiOutputs`, `RawStateDelta`, `SchemaVersion` | `populateInjectionState` (`:434`) | Only for non-importable resources. |

**Redaction.** `redactSensitivePaths` (`pkg/module_map.go:1021`) replaces each
sensitive attribute's value with the literal string `(sensitive)`
(`:1034`). It handles **top-level paths only** — `len(pvm.Path) == 1`
(`:1032`); a sensitive value nested inside a block or map is left in the digest
in plaintext, and the code says so (`:1036`). Redaction happens *before*
`populateInjectionState`, deliberately, so the raw state delta can never embed a
secret (`:394-400`).

**Where the real value goes.** Separately, `DiscoverSensitiveSecrets`
(`pkg/module_map.go:698`) re-parses the same `AttrsJSON` (`:731`) and reads the
*unredacted* value (`:751`), keying it by `flattenAddress(address, attribute)`
(`:767`, definition at `:818`). `SetSecretsFromState` (`:967`) writes those into
Pulumi stack config as secrets. So after `digest tf`:

- the digest holds `(sensitive)`;
- the stack config holds the real value under a flattened key;
- the mapping between them is *not written down anywhere* — it is recomputed by
  calling `flattenAddress` again, in `redactedAttributeKeys`
  (`pkg/import_filler.go:102`) and in `patchResourceFields`
  (`pkg/state_patcher.go:554`).

`flattenAddress` is therefore a load-bearing pure function whose output is a
cross-command contract. It also dedups colliding keys by appending `_2`, `_3`
(`pkg/module_map.go:776`) — and the dedup counter lives only in
`DiscoverSensitiveSecrets`. The two later call sites recompute the *undeduped*
key, so **the second and later resources that collide on a key can never have
their secret resolved.** A warning is printed at digest time (`:778`) and
nothing checks later.

`DiscoverSensitiveSecrets` also stringifies with `fmt.Sprintf("%v", value)`
(`:751`), so a numeric secret loses fidelity the same way `ImportID` does.

`BuildSensitivityMap` / `RedactSensitiveAttributes` (`pkg/provider_schema.go:44`,
`:235`) implement a second, schema-driven redaction mechanism that reads
`Sensitive` off the *live* provider schema and handles nested paths
(`findSensitiveAttributes`, `:155`). Nothing outside tests calls either. It is
the mechanism that would fix the nested-path and `tofu show -json` gaps above.

### S2b — the non-importable enrichment (new on this branch)

When `Check` returns `Unsupported`, `populateInjectionState`
(`pkg/module_map.go:434`) computes three extra fields. It needs **both**
loaders at once:

- the live Terraform provider, obtained by type-asserting the
  `ImportSupportChecker` to `ProviderAccessor` (`:443`, interface at `:99`,
  implementation at `pkg/importsupport/prober.go:160`) — for the cty type;
- the Pulumi bridge mock, from `pulumiProviders` (`:458-464`) — for Pulumi
  naming.

`ComputeInjectionState` (`pkg/raw_state_delta.go:43`) then does:

```
sch := prov.GetProviderSchema(ctx).ResourceTypes[tfType]   // :51
ty  := sch.Block.ImpliedType()                             // :57  cty.Type
val := ctyjson.Unmarshal(attrsJSON, ty)                    // :58  cty.Value
props := MakeTerraformOutputs(..., schemaMap, schemaInfos)  // :110 PropertyMap
delta := RawStateComputeDelta(ctx, schemaMap, schemaInfos,
             props, FromCtyType(stripTimeouts(ty)), FromCtyValue(val))  // :69
version := sch.Version                                     // :76 / :84
```

Three notes on this hop:

- The value round-trips through `ctyjson.Marshal` → `json.Unmarshal`
  (`pkg/raw_state_delta.go:100-108`) with no `UseNumber`, and
  `MakeTerraformOutputs` produces `resource.PropertyValue` numbers, which are
  `float64` by definition. **Integer fidelity beyond 2^53 cannot survive into
  `PulumiOutputs` at all**, whatever the decoder does. `RawStateDelta` is
  computed from the cty value, so the delta itself is exact; the outputs it
  applies to are not.
- `stripTimeouts` (`:126`) replicates a bridge behaviour that has no zclconf
  equivalent. It is a copy, and will drift.
- Failure is silent by design: a delta that cannot be computed returns `nil`
  and no error (`:75-77`), a panic is caught and turned into "no fields"
  (`safeComputeInjectionState`, `pkg/module_map.go:501`), and a missing schema
  map returns early (`:466-477`). The digest is written either way, so a
  consumer cannot distinguish "this resource needs no delta" from "computing it
  blew up".

### S3 — digest → import file

`FillImportFile` (`pkg/import_filler.go:113`) matches digest resources to
placeholder entries in a `pulumi preview --import-file` skeleton and writes
`entry.ID = tfRes.ImportID` (`:280`). **Only the ID crosses.** Attributes,
outputs, deltas — none of it is in the import file, because `ImportEntry`
(`:23`) has nowhere to put it and `pulumi import` would discard it anyway (see
the spec's first finding: `ImportStep.Apply` calls `prov.Read` and writes the
*provider's* values).

Matching is by Pulumi type plus name suffix, with a "exactly one candidate of
this type" fallback (`matchChildren`, `:326`, fallback at `:364`). The fallback
is the only place where a wrong resource can be silently assigned an ID.

`TranslateImportIDs` (`:452`) then rewrites IDs for ~16 hardcoded AWS types
whose Pulumi import format differs from Terraform's, reading fields back out of
`tf.Attributes` (`:481`). This is the one place where digest *attributes*
influence the import file. It reads `from_port`/`to_port` through
`fmt.Sprintf("%v", …)` (`:521-522`), which is float-formatted; ports are small
enough that this is currently harmless.

Non-importable resources are diverted here rather than filled: `assign`
(`:254`) appends a `NonImportableResource` (`:52`) and marks the entry dropped
(`:277`); dropped entries are removed from the file (`:221-230`).

### S3b — digest → non-importable sidecar

`writeNonImportable` (`cmd/non_importable.go:38`) writes
`<out>.non-importable.json`. This carries everything S3 could not:
`Attributes`, `RedactedAttributes`, `PulumiOutputs`, `RawStateDelta`,
`SchemaVersion` (`pkg/import_filler.go:265-276`).

The digest was read at `cmd/import_id_match.go:84` with plain `json.Unmarshal`
and the sidecar is written with `json.MarshalIndent` (`cmd/non_importable.go:55`),
so **every number in the sidecar has been through `float64` even though
`LoadNonImportableFile` (`pkg/non_importable_file.go:36`) carefully reads it back
with `UseNumber`.** The care is applied at the wrong end.

Nothing consumes the sidecar yet. `LoadNonImportableFile`,
`MapTFAttributesToPulumi` (`:54`), `PulumiToTFNames` (`:71`),
`ParsePreviewJSON` (`pkg/preview.go:54`) and `VerifyDeploymentIntegrity`
(`pkg/state_verify.go:34`) are all built and tested but not wired to any
command — the in-progress half of
[#22](https://github.com/pulumi-proserv/pulumi-tool-import/issues/22).

### S4 — import file → `pulumi import` → deployment

`pkg/batchimport` decodes the file straight into `[]*optimport.ImportResource`
(`pkg/batchimport/file.go:29`) and hands it to `auto.Stack.ImportResources`
(`pkg/batchimport/stack.go:55`), in batches, with `Protect(false)` and
`GenerateCode(false)`.

This stage is genuinely simple from the tool's side: it passes type, name, ID,
parent and provider through and the engine does the rest. What comes back is
whatever the provider's `Read` returned, marshalled by the bridge — including
`__meta` and `__pulumi_raw_state_delta` in the outputs, written by the bridge,
never by this tool.

The state as it exists after this stage is the *only* state the tool ever sees
in Pulumi form. Everything `patch-state` does is repair work on it.

### S5 — deployment → `patch-state tf` → patched deployment

`PatchState` (`pkg/state_patcher.go:691`) reads `pulumi stack export` output
with `UseNumber` (`:705`), walks `deployment.resources`, and for each custom
resource whose short Pulumi type appears in the fields file, builds
`patchFieldDescriptor`s (`:812`) and calls `patchAndValidateResource` (`:825`).

Per field, `patchResourceFields` (`:495`):

1. **Reads the digest value** by TF attribute name (`:511`) and camelCases
   nested keys (`camelCaseKeys`, `:515` → `:1497`).
2. **Builds asset sentinels** for asset-typed fields (`:527-550`), which may
   reach out to AWS and download Lambda code (`:540`).
3. **Resolves `(sensitive)`** by recomputing `flattenAddress` (`:554`) and
   looking it up in `configSecrets`, then wrapping the value in Pulumi's secret
   envelope with the signature written as a string literal (`:561`).
4. **Writes inputs** only when the existing input is empty or of the wrong
   shape (`:576`), preferring the digest value, falling back to the schema/file
   default (`:602`).
5. **Writes outputs** only for simple values and asset sentinels (`:622`) —
   arrays and objects are deliberately not patched into outputs, because the
   bridge may have reshaped them.

Then, in `patchAndValidateResource` (`:637`):

- `injectAssetDeltas` (`:663` → `:1164`) adds `{"asset": …}` entries to
  `__pulumi_raw_state_delta.obj.ps` for each patched asset field;
- `validateRecover` (`:671` → `:1448`) runs the bridge's own
  `UnmarshalRawStateDelta` + `Recover` over the patched outputs, and **on
  failure reverts inputs and outputs wholesale** (`:672-676`).

The revert is the tool's only value-level safety net, and it is structural: it
proves outputs and delta are mutually consistent, not that either is right.

The result is re-serialized with `json.MarshalIndent` (`:855`) and written by
`cmd/patch_state_tf.go:148`. The operator runs `pulumi stack import`.

**Dead or near-dead machinery in this file.** `updateDeltaForPatchedOutputs`
(`:1205`) and `patchedOutputFieldInfo` (`:1146`) have no callers at all.
`conformToDelta` (`:1331`) is called only from `pkg/state_patcher_test.go`.
`PatchStateFromSchema` (`:1608`) — the schema-driven alternative to the curated
fields file — is called only from tests; no command wires it up. That is
consistent with the finding in [S-schema](#schema-forms-and-their-consumers)
that its default-fallback path cannot work in production.

### S6 — re-imported state

`pulumi stack import` runs `Snapshot.VerifyIntegrity`
(`pulumi/pkg/v3@v3.222.0/resource/deploy/snapshot.go`). The tool has an
in-process copy of that check, `VerifyDeploymentIntegrity`
(`pkg/state_verify.go:34`), with an extra pre-check for an empty provider
reference on a resource that has an ID (`:47-52`) — but nothing calls it. It
is staged for stack mode in #22.

### S7 — injection (designed, not wired)

Per the design spec, injection takes each sidecar entry, matches it to a
`create` step from `pulumi preview --json`, and appends a state object built
from `newState` plus the sidecar's `id`, `outputs`, `__pulumi_raw_state_delta`
and `__meta`. `pkg/preview.go` (parsing, `CreatesByTypeName` at `:67`) and
`pkg/state_verify.go` exist; `pkg/state_injector.go` and `pkg/state_stack.go` do
not. See [#22](https://github.com/pulumi-proserv/pulumi-tool-import/issues/22).

---

## The three bridge reserved keys

Defined in `pulumi-terraform-bridge/v3@v3.121.0/pkg/reservedkeys/keys.go`:
`Meta = "__meta"` (`:19`), `Defaults = "__defaults"` (`:26`),
`RawStateDelta = "__pulumi_raw_state_delta"` (`:30`).

**This repo never imports `reservedkeys`.** Every occurrence is a string
literal: `pkg/state_patcher.go:661`, `:663`, `:670`, `:1332`, `:1376`, `:1449`.
`__meta` and `__defaults` appear only in comments
(`pkg/module_map.go:72`, `pkg/import_filler.go:83`) — nothing in the tool reads
or writes either today.

| Key | Written by | Read by this tool | Mutated by this tool |
|---|---|---|---|
| `__pulumi_raw_state_delta` | The bridge, during `pulumi import`. Also computed by `ComputeInjectionState` (`pkg/raw_state_delta.go:69`) into the digest and sidecar. | `validateRecover` (`pkg/state_patcher.go:1449`), `conformToDelta` (`:1332`, tests only) | `injectAssetDeltas` (`:1164`) adds asset entries. `updateDeltaForPatchedOutputs` (`:1205`) would rebuild array deltas but is uncalled. |
| `__meta` | The bridge (schema version + private state). | Nothing. | Nothing. `SchemaVersion` is carried in the digest (`pkg/module_map.go:74`) for a future injector to write. |
| `__defaults` | The bridge, on inputs. | Nothing. | Nothing. The design calls for injecting `__defaults: []`. |

The delta's contract, per the bridge's own `turnaroundCheck`
(`rawstate.go:522`), is that `Recover` must reproduce the raw state byte for
byte. That is why `patch-state` must edit it when it edits a value, and why
`validateRecover` reverting on failure is the correct conservative behaviour.
`Recover`'s empty-delta path, `rawStateRecoverNatural` (`rawstate.go:234`),
refuses object values outright (`:263`), which is why an absent delta and an
empty delta are not the same thing.

[#25](https://github.com/pulumi-proserv/pulumi-tool-import/issues/25) covers the
raw state delta gap.

---

## Schema forms and their consumers

Schema enters in **four** distinguishable forms.

### 1. The Terraform protocol schema (live provider)

`providers.GetProviderSchema(ctx)` over go-plugin, from
`tfprovider.LoadProvider` (`pkg/tfprovider/loader.go:65`). Note the package: the
bridge's *vendored* OpenTofu
(`pulumi-terraform-bridge/v3/pkg/vendored/opentofu/providers`), whose method
takes a `context.Context` — not `github.com/pulumi/opentofu/providers`, which
this repo also depends on for state parsing.

Carries: `Block` (→ `ImpliedType()`, a `cty.Type`), `Version`, per-attribute
`Sensitive`, `Required`/`Optional`/`Computed`.

Consumers:
- `ComputeInjectionState` — `sch.Block.ImpliedType()` and `sch.Version`
  (`pkg/raw_state_delta.go:57`, `:76`). Nothing else can supply these.
- `BuildSensitivityMap` — `attr.Sensitive` (`pkg/provider_schema.go:95`,
  `:166`). Unused outside tests.

Cost: downloads and runs the real provider binary. Requires a locked version
from `.terraform.lock.hcl` (`pkg/importsupport/prober.go:176`).

### 2. Provider *behaviour*, probed rather than read

Importability is not in any schema. `Prober.Check`
(`pkg/importsupport/prober.go:98`) calls `ImportResourceState` with a dummy ID
(`:120`) and classifies the error (`:126`). Memoized per provider+type
(`:104`). This is a schema consumer only in the sense that it needs the same
running provider.

Fallback when no provider can be loaded: the curated
`pkg/importsupport/fallback.json` (`:199`), which answers `Unsupported` for
types it lists and `Unknown` for everything else.

### 3. The bridge mapping mock (`GetMapping("terraform")`)

`PulumiProvidersForTerraformProviders` (`pkg/pulumi_providers.go:75`) installs
and runs the **Pulumi** provider binary, calls `GetMapping`
(`pkg/bridgedproviders/mapping.go:93`), unmarshals a
`info.MarshallableProvider` (`:125`) and calls `.Unmarshal()` (`:129`). Results
are cached to `~/.pulumi/mapping-cache` (`pkg/pulumi_providers.go:174`).

What the mock is: `MarshallableResourceShim` is `map[string]*MarshallableSchemaShim`
(`info/info.go:995`), and its `Unmarshal` returns
`(&schema.Resource{Schema: s}).Shim()` (`:1011-1017`) — **schema map only**.
Consequently, on this mock:

| Method | Result | Why |
|---|---|---|
| `InstanceState` | hard error, `"mock schema does not support instance states"` | `tfshim/schema/resource.go:55` |
| `SchemaType()` | `nil` | `resource.go:47` reads `V.SchemaType`, never populated |
| `SchemaVersion()` | `0` for every type | `resource.go:35` reads `V.SchemaVersion`, never populated |
| `Importer()` | `nil` for every type | `resource.go:39`, never populated |
| `Schema().Default()` | **`nil` for every field** | `MarshallableSchemaShim` (`info/info.go:949`) has no `Default` field, and `Unmarshal` (`:979`) does not set one |
| `Schema().Sensitive()` | correct | carried at `info/info.go:958` |

The last row is not documented anywhere else and matters: `GetSchemaFieldInfo`
sets `HasDefault` from `schema.Default()` (`pkg/schema_fields.go:97`), so
**`HasDefault` is always false in production**, and `PatchStateFromSchema`'s
default-fallback branch (`pkg/state_patcher.go:602`, fed at `:1716-1717`) can
never fire against a real provider. It fires in tests, which construct
`schema.Schema{Default: …}` directly. This is very likely why the curated
`data/aws-import-diff-fields.json` exists and why `PatchStateFromSchema` was
never wired to a command.

Consumers of the mock:
- `bridge.PulumiTypeToken` (`pkg/bridge/pulumi_type_token.go:28`) — Pulumi type
  token for a TF type, used to build URNs (`pkg/module_map.go:542`).
- `GetSchemaFieldInfo` (`pkg/schema_fields.go:72`) — TF→Pulumi names via
  `tfbridge.TerraformToPulumiNameV2` (`:93`), input/computed classification
  (`:103-104`), asset overlay (`:107-113`).
- `ComputeInjectionState` — passes `schemaMap` and `schemaInfos` straight into
  `MakeTerraformOutputs` and `RawStateComputeDelta`
  (`pkg/module_map.go:456-465` → `pkg/raw_state_delta.go:69`, `:110`).

Note `PulumiTypeToken` calls `camelPascalPulumiName`, which contains a
`contract.Assertf` on the resource-type prefix
(`pkg/bridge/pulumi_type_token.go:42`). That is a **panic**, not an error, and
`buildResourceURN` (`pkg/module_map.go:542`) only handles the error return. A
provider whose resources do not all share its `GetResourcePrefix()` would abort
the digest.

### 4. Curated data files

`data/aws-import-diff-fields.json`, loaded by `LoadFieldsFile`
(`pkg/state_patcher.go:1774`), and `pkg/importsupport/fallback.json`. These
encode facts about provider *Go code* — `Default`, `Read` behaviour, `Importer`
— that no schema exposes. `docs/aws-import-diff-fields.md` explains the
categories; `docs/non-importable-resources.md:135-145` explains why `Read`
semantics cannot be probed.

### Property naming: three mechanisms that can disagree

| Mechanism | Where | Basis |
|---|---|---|
| `tfbridge.TerraformToPulumiNameV2(tfName, schemaMap, fieldInfos)` | `pkg/schema_fields.go:93` | Schema-aware; handles pluralization from `MaxItems`, and `info.Schema.Name` overrides |
| `snakeToCamel` | `pkg/state_patcher.go:1484`, applied recursively by `camelCaseKeys` (`:1497`) | Pure string transform, no schema |
| `tfToPulumiField` / `pulumiToTFField` | `pkg/state_patcher.go:121`, `:147` | A 23-entry hand-written table |
| `tfbridge.MakeTerraformOutputs` | via `pkg/raw_state_delta.go:110` | The bridge's own conversion, schema-driven at every nested level |

`MapTFAttributesToPulumi` (`pkg/non_importable_file.go:54`) uses the first when
a field is in the schema and falls back to the second when it is not (`:60-63`),
which is the only place the two are reconciled explicitly.

They disagree in predictable cases:

- **Pluralization.** `TerraformToPulumiNameV2` turns a `MaxItems != 1` list
  attribute `ingress_rule` into `ingressRules`; `snakeToCamel` yields
  `ingressRule`. `patchResourceFields` uses `camelCaseKeys` for *nested* digest
  values (`pkg/state_patcher.go:515`), so nested list-of-object fields patched
  from the digest get unpluralized names inside a correctly-named top-level
  property.
- **Name overrides.** `info.Schema.Name` (an explicit rename in the provider's
  bridge metadata) is honoured only by the first mechanism. The table at `:121`
  encodes one such override by hand — `"filename": "code"` (`:126`) and
  `"parameter": "parameters"` (`:134`).
- **`PatchState` vs `PatchStateFromSchema`.** The former derives the TF name
  from the Pulumi name through the hand table (`:814`); the latter takes both
  from the schema (`:1714-1715`). A field present in the fields file but absent
  from the table gets `TFName: ""` and is therefore never matched to a digest
  value at all (`:510`) — it can only ever be patched from its default.

---

## Representations and conversions

| # | Representation | Produced by | Converted to | By |
|---|---|---|---|---|
| 1 | `.tfstate` bytes | Terraform | `*states.State` | `statefile.Read`, `pkg/tofu_eval.go:147` |
| 1a | `tofu show -json` bytes | `tofu` | `tfjson.State` → `*states.State` | `pkg/generate_module_map.go:125`, `:259` |
| 2 | `AttrsJSON` (`[]byte`, exact) | Terraform | `map[string]interface{}` (float64) | `json.Unmarshal`, `pkg/module_map.go:368` |
| 3 | `[]cty.PathValueMarks` | Terraform | `(sensitive)` placeholders | `redactSensitivePaths`, `pkg/module_map.go:1021` |
| 3b | same | | stack config secrets | `DiscoverSensitiveSecrets` → `SetSecretsFromState`, `:698`, `:967` |
| 4 | `ModuleResource` | `matchResources`, `:327` | `tf-digest.json` | `WriteModuleMap`, `:1098` |
| 5 | `AttrsJSON` (redacted, re-marshalled) | `pkg/module_map.go:479` | `cty.Value` | `ctyjson.Unmarshal`, `pkg/raw_state_delta.go:58` |
| 6 | `cty.Value` | | `resource.PropertyMap` | `MakeTerraformOutputs`, `pkg/raw_state_delta.go:110` |
| 7 | `cty.Value` + `PropertyMap` | | `RawStateDelta` | `RawStateComputeDelta`, `:69` |
| 8 | `RawStateDelta` | | `map[string]interface{}` | `delta.Marshal().Mappable()`, `:79` |
| 9 | `ModuleResource` | | `ImportEntry.ID` (string only) | `fillState.assign`, `pkg/import_filler.go:280` |
| 10 | `ModuleResource` | | `NonImportableResource` | `pkg/import_filler.go:265` |
| 11 | `ImportFile` | | `[]*optimport.ImportResource` | `pkg/batchimport/file.go:39` |
| 12 | — | `pulumi import` | deployment JSON (`apitype.DeploymentV3`) | the engine + bridge |
| 13 | deployment JSON | `pulumi stack export` | `map[string]interface{}` with `json.Number` | `pkg/state_patcher.go:705` |
| 14 | `map[string]interface{}` | `patchResourceFields`, `:495` | patched deployment JSON | `json.MarshalIndent`, `:855` |
| 15 | deployment JSON | | `deploy.Snapshot` | `stack.DeserializeDeploymentV3`, `pkg/state_verify.go:54` (uncalled) |
| 16 | `pulumi preview --json` | | `PreviewDigest` with `json.Number` | `ParsePreviewJSON`, `pkg/preview.go:54` (uncalled) |

`shim.InstanceState` appears nowhere in this table. The tool never constructs
one and cannot: the only `shim.Resource` it holds is the mock, whose
`InstanceState` is a hard error (`tfshim/schema/resource.go:55`). This is the
reason `RawStateComputeDelta` (which needs only a type and a value) is used
instead of `RawStateInjectDelta` (`rawstate.go:458`, which needs an instance
state).

---

## Weaknesses and open questions

Ordered roughly by how much damage each can do.

### 1. `UseNumber` is applied at the reading end, never at the producing end

`UseNumber` appears in five places: `pkg/preview.go:57`,
`pkg/non_importable_file.go:43`, `pkg/state_patcher.go:705` and `:1623`,
`pkg/state_patcher_cfn.go:42` and `:102`. All six of those read documents the
tool did not write, or wrote through a path that had already lost precision.

Every place a value is *produced* uses plain `json.Unmarshal`:

| Site | Consequence |
|---|---|
| `pkg/module_map.go:368` | Digest `attributes` are float64. A 19-digit integer is rounded before it is ever written. |
| `pkg/module_map.go:370` | `ImportID` is `fmt.Sprintf("%v", float64)` → `1.2345678901234568e+18` for a large numeric id. |
| `pkg/module_map.go:731`, `:751` | Secret values written to stack config go through the same float64 stringification. |
| `pkg/generate_module_map.go:125`, `pkg/tofu/loader.go:82` | The `tofu show -json` path loses precision before the digest is even built. |
| `cmd/import_id_match.go:84` | Digest read as float64, then re-marshalled into the sidecar — defeating `LoadNonImportableFile`'s `UseNumber`. |
| `cmd/patch_state_tf.go:78` | Digest read as float64, then written into a deployment that was carefully read with `UseNumber`. **This is the direct contradiction:** the state is protected, the values being written into it are not. |
| `pkg/raw_state_delta.go:106` | Outputs pass through float64; unavoidable, since `resource.PropertyValue` numbers are float64. |

Verified empirically: `json.Unmarshal` + `json.Marshal` maps
`1234567890123456789` to `1234567890123456800`, and `fmt.Sprintf("%v", …)` on
the intermediate `float64` gives `1.2345678901234568e+18`.

The cheap fix is a single `pkg` helper — decode-with-`UseNumber` — used at every
`json.Unmarshal` that touches state or digest data. The expensive part is that
`resource.PropertyValue` cannot represent an exact large integer at all, so
`PulumiOutputs` and anything derived from a `PropertyMap` has a hard ceiling
regardless.

### 2. Two provider loaders, neither able to do the other's job (#26)

`populateInjectionState` (`pkg/module_map.go:434`) is the clearest symptom: it
needs the live provider for the cty type *and* the bridge mock for Pulumi
naming, and bails out entirely if either is missing (`:445`, `:449`, `:466`).
The comment at `:466-477` documents that these are "two different loaders with
different failure modes" and that a mismatch produces a silently
under-populated digest.

The mock's specific gaps, all verified above: no `Default` (so
`PatchStateFromSchema` is unusable), `SchemaVersion` always 0, `Importer`
always nil (hence the probe), `SchemaType` nil, `InstanceState` a hard error
(hence `RawStateComputeDelta` over `RawStateInjectDelta`).

Two directions worth evaluating, neither traced here:

- Extend `MarshallableSchemaShim` upstream to carry `Default` and
  `SchemaVersion`. That is a bridge change and would help every consumer of
  `GetMapping`, not just this tool.
- Derive Pulumi names from the live Terraform schema plus the mapping's
  `info.Schema` overlay, and drop the mock's schema map entirely. Whether
  `MakeTerraformOutputs` and `RawStateComputeDelta` can be driven from a
  schema map built that way is **not established** — they take
  `shim.SchemaMap`, so it would mean constructing `schema.Schema` values from
  the protocol schema, and it is not obvious that `MaxItems`/`Elem` survive
  that translation faithfully.

### 3. Sensitivity handling has three independent implementations and two blind spots

- `redactSensitivePaths` (`pkg/module_map.go:1021`) handles top-level paths
  only; nested sensitive values reach the digest in plaintext (`:1036`).
- The `tofu show -json` path carries no sensitivity at all
  (`pkg/generate_module_map.go:302` vs `terraform-json/state.go:164`), so on
  that path *nothing* is redacted and no secret is written to config.
- `BuildSensitivityMap` (`pkg/provider_schema.go:44`) — the schema-driven
  implementation that handles nesting — is unused.
- The digest↔config link is the pure function `flattenAddress`
  (`pkg/module_map.go:818`), recomputed at three sites
  (`:767`, `pkg/import_filler.go:102`, `pkg/state_patcher.go:554`), and only
  the first applies dedup suffixes (`:776`). Colliding keys are therefore
  unresolvable downstream, with only a digest-time warning.
- The design spec adds a *second* placeholder to worry about: `[secret]`, from
  `MassageSecrets` in `stateForJSONOutput`, on the preview path.

Recording `RedactedAttributes` in the digest itself (rather than recomputing it
in `resolve tf` from a string match against `"(sensitive)"`,
`pkg/import_filler.go:98`) would make the link explicit and dedup-safe.

### 4. `patch-state` can only repair what the digest kept, and only by name

`PatchState` matches state resources to digest resources by Pulumi resource
*name* (`pkg/state_patcher.go:781`, built by `BuildDigestNameMap` at `:307`),
with a chain of fallbacks ending in "exactly one unused candidate of this type"
(`:412`, `:461`). A mismatch is reported only as an aggregate `NoMatch` count
(`:844`) — there is no per-resource report of what went unmatched, and no way to
assert that a resource the operator cares about was matched.

The same fallback exists in `FillImportFile` (`pkg/import_filler.go:364`) with
a warning, and in `matchChildren`'s normalized-name pass
(`pkg/state_patcher.go:389-400`). Three near-identical matchers with slightly
different rules is a simplification opportunity: `FillImportFile` and
`BuildDigestNameMap` are the same algorithm over different input shapes.

### 5. Dead and half-wired code obscures which path is real

- `updateDeltaForPatchedOutputs` (`pkg/state_patcher.go:1205`): no callers.
- `conformToDelta` (`:1331`): tests only.
- `PatchStateFromSchema` (`:1608`): tests only, and its default path is
  unreachable in production (see §2).
- `BuildSensitivityMap`, `RedactSensitiveAttributes` (`pkg/provider_schema.go`):
  tests only.
- `ParsePreviewJSON`, `VerifyDeploymentIntegrity`, `LoadNonImportableFile`,
  `MapTFAttributesToPulumi`, `PulumiToTFNames`: built for #22, not yet wired.

The last group is expected work-in-progress. The first four are not, and
`PatchStateFromSchema` in particular reads like a supported alternative to the
curated fields file when it cannot currently work.

### 6. The digest is the only inter-command contract, and it is untyped at the boundaries

`ModuleMap` is decoded with `json.Unmarshal` into a struct whose `Attributes`
is `map[string]interface{}`. There is no schema version on the file, no
checksum, and no record of which provider versions produced it — only
`mm.Providers`, a map whose values are always the empty string
(`pkg/module_map.go:154`). A digest built against AWS provider 5.x and consumed
by a `patch-state` run against 7.x is indistinguishable from a matched pair.

`ImportSupportChecked` (`:49`) is the one piece of provenance that *is*
recorded, and `resolve tf` uses it well (`cmd/import_id_match.go:171`). The
same treatment for provider versions and for "injection state was computed /
was attempted and failed" would let consumers tell absence from failure —
today `PulumiOutputs == nil` means both.

### 7. Verification is structural everywhere it exists

`validateRecover` (`pkg/state_patcher.go:1448`) checks delta↔outputs
consistency. `VerifyDeploymentIntegrity` (`pkg/state_verify.go:34`) checks
snapshot structure. Neither checks a value against the cloud, and per the design
spec's third finding, `refresh` cannot either for the types that matter. The
only real check is `pulumi preview` reporting zero operations, and no command
runs it — it is a documented manual step. That gap is the whole content of the
stack-mode half of #22.

### 8. Related, already tracked

- [#22](https://github.com/pulumi-proserv/pulumi-tool-import/issues/22) —
  non-importable state injection; S3b and S7 above.
- [#24](https://github.com/pulumi-proserv/pulumi-tool-import/issues/24) —
  splitting large workspaces into shard stacks. Relevant here because it needs
  the same export/verify/import helpers as #22, and because sharding multiplies
  the digest-provenance problem in §6.
- [#25](https://github.com/pulumi-proserv/pulumi-tool-import/issues/25) — the
  raw state delta gap.
- [#26](https://github.com/pulumi-proserv/pulumi-tool-import/issues/26) — the
  two loaders; §2.

### Not traced

- What the engine and bridge write into `__meta` and `__defaults` during
  `pulumi import`, in detail. The tool reads neither, so their contents were
  taken from the `reservedkeys` doc comments rather than from the write sites.
- The CFN half of the pipeline (`pkg/state_patcher_cfn.go`, `pkg/cfn`). It has
  its own state representation and its own `UseNumber` discipline
  (`pkg/state_patcher_cfn.go:42`, `:102`), and shares only
  `patchResourceFields` with the TF path.
- Whether `loadStateWithRewrite`'s textual
  `registry.terraform.io/` → `registry.opentofu.org/` substitution
  (`pkg/tofu/loader.go:285`) can corrupt an attribute value in practice.
- Whether a `shim.SchemaMap` reconstructed from the live Terraform protocol
  schema would drive `MakeTerraformOutputs` and `RawStateComputeDelta`
  correctly — the key question for collapsing the two loaders (§2).
