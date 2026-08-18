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
and [`docs/aws-import-diff-fields.md`](aws-import-diff-fields.md).

## Orientation

The pipeline is five commands and two documents that survive between them — the
digest, and the non-importable sidecar:

```
terraform.tfstate ──digest tf──▶ tf-digest.json ──resolve tf──▶ imports-ready.json
                                       │                        imports-ready.non-importable.json
                                       │                                │             │
                                       │                            import            │
                                       │                                ▼             │
                                       └──────patch-state tf──▶ pulumi stack export   │
                                                     ▲                  │             │
                                       pulumi preview --json  ──────────┼─────────────┘
                                                                        ▼
                                                              pulumi stack import
```

`tf-digest.json` is the pivot. It is read by `resolve tf`
(`cmd/import_id_match.go:85`) and again by `patch-state tf`
(`cmd/patch_state_tf.go:164`), and everything either command knows about
Terraform values comes from it. Nothing downstream re-reads Terraform state.

`patch-state tf` has two modes (`cmd/patch_state_tf.go:74`). **File mode**
(`--state` + `--out`) reads and writes files and leaves `pulumi stack import` to
the operator. **Stack mode** (`--project-dir` + `--stack`, neither `--state` nor
`--out`) drives the whole export → patch → inject → import → verify cycle
through the Automation API (`pkg/state_stack.go`). Injection of non-importable
resources (`--non-importable`) works in both, but file mode additionally needs
`--preview-json` because the program metadata cannot come from anywhere else
(`cmd/patch_state_tf.go:86`).

Two provider loaders run, for different reasons, and they never see each other's
results except through `pulumiProviders` being passed into `populateInjectionState`:

| Loader | Started by | Protocol | Purpose |
|---|---|---|---|
| `tfprovider.LoadProvider` (`pkg/tfprovider/loader.go:65`) | `importsupport.Prober` (`pkg/importsupport/prober.go:184`), `BuildSensitivityMap` (`pkg/provider_schema.go:77`) | go-plugin, real Terraform provider binary | `ImportResourceState` probe; `GetProviderSchema` for cty types |
| `PulumiProvidersForTerraformProviders` (`pkg/pulumi_providers.go:75`) | `GenerateModuleMap` (`pkg/generate_module_map.go:116`, `:131`); `loadProvidersForDigest` (`cmd/patch_state_tf.go:429`) | Pulumi plugin gRPC, `GetMapping("terraform")` | Pulumi type tokens and property names |

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
   (`1234567890123456789` → `1234567890123456800`). The loss is silent — it
   takes an integer above 2^53 to trigger it, and scientific notation only
   appears at ≥1e21 — so nothing downstream can detect that it happened. The
   raw `.tfstate` path was fixed on this branch (`decodeAttrs`,
   `pkg/module_map.go:432`); **this path was not**. See
   [#27](https://github.com/pulumi-proserv/pulumi-tool-import/issues/27) and
   [Weaknesses §1](#weaknesses-and-open-questions).
2. **Sensitivity.** `tfjson.StateResource.SensitiveValues`
   (`terraform-json@v0.27.1/state.go:164`) is never read.
   `ResourceInstanceObjectSrc` is built with only `AttrsJSON`
   (`pkg/generate_module_map.go:302`), so `AttrSensitivePaths` is empty. Every
   downstream consumer of sensitivity — `redactSensitivePaths`,
   `DiscoverSensitiveSecrets`, `redactedAttributeKeys` — silently finds nothing
   on this path. **Secrets are written to the digest in plaintext.** See
   [#28](https://github.com/pulumi-proserv/pulumi-tool-import/issues/28).
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

`matchResources` (`pkg/module_map.go:328`) walks the state and emits one
`ModuleResource` (`pkg/module_map.go:53`) per current instance.

| Field | Source | Notes |
|---|---|---|
| `TerraformAddress` | `res.Addr` + instance key + module addr (`:357-363`) | The join key for everything downstream. |
| `ImportID` | `attrs["id"]` via `formatImportID` (`:372`, definition `:446`) | Stringified with `%v`. Now safe: `json.Number` is a string type, so `%v` prints the original digits. |
| `Attributes` | `decodeAttrs(AttrsJSON)` (`:369`, definition `:432`), then redacted | Decodes with `UseNumber`, so integers above 2^53 survive as `json.Number`. Fixed on this branch. |
| `TranslatedURN` | `buildResourceURN` (`:551`) | Needs the Pulumi provider mapping; falls back to the raw TF address when absent (`:559`, `:565`, `:570`). |
| `Mode` | `managed` / `data` (`:378`) | Data sources get no URN and are never imported. |
| `NonImportable` | `importChecker.Check` (`:408`) | See S2b. |
| `PulumiOutputs`, `RawStateDelta`, `SchemaVersion` | `populateInjectionState` (`:460`) | Only for non-importable resources. See S2b. |

**Redaction.** `redactSensitivePaths` (`pkg/module_map.go:1047`) replaces each
sensitive attribute's value with the literal string `(sensitive)`
(`:1060`). It handles **top-level paths only** — `len(pvm.Path) == 1`
(`:1058`); a sensitive value nested inside a block or map is left in the digest
in plaintext, and the code says so (`:1062`). Redaction happens *before*
`populateInjectionState`, deliberately, so the raw state delta can never embed a
secret (`:396-403`).

**Where the real value goes.** Separately, `DiscoverSensitiveSecrets`
(`pkg/module_map.go:724`) re-parses the same `AttrsJSON` (`:757`) and reads the
*unredacted* value (`:777`), keying it by `flattenAddress(address, attribute)`
(`:793`, definition at `:844`). `SetSecretsFromState` (`:993`) writes those into
Pulumi stack config as secrets. So after `digest tf`:

- the digest holds `(sensitive)`;
- the stack config holds the real value under a flattened key;
- the mapping between them is *not written down anywhere* — it is recomputed by
  calling `flattenAddress` again, in `redactedAttributeKeys`
  (`pkg/import_filler.go:102`) and in `patchResourceFields`
  (`pkg/state_patcher.go:554`), and a fourth time in `resolveOutputSecrets` /
  `resolveSecretInputs` (`pkg/state_injector.go:383`, `:444`), which look the
  recomputed key up in `r.RedactedAttributes`.

`flattenAddress` is therefore a load-bearing pure function whose output is a
cross-command contract.

**Colliding keys are now a hard error.** They used to be deduped by appending
`_2`, `_3`, with the counter living only in `DiscoverSensitiveSecrets` — but
every later call site recomputes the *undeduped* key, so nothing could ever read
a suffixed one back. The second and later colliding resources therefore resolved
to the **first one's secret**: a real secret written into the wrong resource's
state, silently, and nondeterministically, since the state maps were never
sorted. Suffixing did not handle the collision, it hid it. `digest tf` now fails
and names both addresses, and the discovery walk is sorted so the result is
reproducible.

Collisions are easy to reach because `flattenAddress` drops the resource type
and collapses punctuation: `module.db.aws_db_instance.this` and
`module.db.aws_rds_cluster.this` both flatten to `db_password`, as do
`ssm_parameters["/develop/api/key"]` and `ssm_parameters["/develop/api_key"]`.
The cause-level fix — recording the resolved key on the digest so `resolve tf`
consumes it instead of recomputing — needs the digest written after secret
discovery, which is a pipeline reorder rather than a fix.

`DiscoverSensitiveSecrets` now decodes with `decodeAttrs` (`UseNumber`) before
stringifying with `fmt.Sprintf("%v", value)`. It previously used a plain
`json.Unmarshal`, which turned a sensitive `1234567890123456789` into
`"1.2345678901234568e+18"` in stack config — and injection then resolved that
key and wrote the corrupted, retyped value into state as the resource's real
secret. `json.Number` is itself a string type, so `%v` prints the original
digits.

`BuildSensitivityMap` / `RedactSensitiveAttributes` (`pkg/provider_schema.go:44`,
`:235`) implement a second, schema-driven redaction mechanism that reads
`Sensitive` off the *live* provider schema. Nothing outside tests calls either.
See [#28](https://github.com/pulumi-proserv/pulumi-tool-import/issues/28).

The nested-path and `tofu show -json` gaps this used to be proposed as the fix
for are **now closed in the primary mechanism instead**: `redactAtPath` walks a
sensitive path to any depth, and `rawStateFromTfjson` populates
`AttrSensitivePaths` from the format's own `sensitive_values` document. Before
that, `tofu show -json` state — which `DetectStateFormatBytes` selects
automatically on the presence of a `format_version` key, with no flag to warn
you — got **no redaction at all**, and a nested sensitive attribute was left in
plaintext at any depth below the first.

A nested secret still gets no stack config key, because the key format is an
address plus one attribute name. That is deliberate: injection then meets the
placeholder and hard-fails in `checkNoPlaceholders` rather than leaking.

### S2b — the non-importable enrichment

When `Check` returns `Unsupported`, `populateInjectionState`
(`pkg/module_map.go:460`) computes three extra fields — `PulumiOutputs`,
`RawStateDelta` and `SchemaVersion` — which are what makes S7 injection
possible without a provider. It needs **both** loaders at once:

- the live Terraform provider, obtained by type-asserting the
  `ImportSupportChecker` to `ProviderAccessor` (`:469`, interface at `:100`,
  implementation at `pkg/importsupport/prober.go:160`) — for the cty type;
- the Pulumi bridge mock, from `pulumiProviders` (`:484-490`) — for Pulumi
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
  (`safeComputeInjectionState`, `pkg/module_map.go:527`), and a missing schema
  map returns early (`:492-503`). The digest is written either way, so a
  consumer cannot distinguish "this resource needs no delta" from "computing it
  blew up". The injector reports the aggregate as `InjectResult.NoDelta`
  (`pkg/state_injector.go:50`), which is the only signal an operator gets.

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

The digest is read at `cmd/import_id_match.go:86` with `UseNumber` and the
sidecar is written with `json.MarshalIndent` (`cmd/non_importable.go:55`), so
numbers now cross as `json.Number` and reach `LoadNonImportableFile`
(`pkg/non_importable_file.go:36`) — which reads them back with `UseNumber` —
exactly as they were in Terraform state. Before the fix on this branch the care
was applied only at the reading end and every number had already been through
`float64`.

The sidecar is consumed by `patch-state tf --non-importable`
(`cmd/patch_state_tf.go:245`) — see S7.

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

The state as it exists after this stage is the only *deployed* state the tool
sees in Pulumi form, and everything `patch-state` patches is repair work on it.
It is no longer the only Pulumi-shaped input, though: injection reads a second
one, `newState` from a preview create step (S7), which describes resources that
were never imported at all.

### S5 — deployment → `patch-state tf` → patched deployment

In file mode the exported deployment comes from disk (`cmd/patch_state_tf.go:152`);
in stack mode from `StackSession.Export` (`pkg/state_stack.go:58`), which
re-marshals the whole `{"version":…,"deployment":{…}}` envelope because
`auto.Stack.Export` hands back only the inner object and every consumer here
reads the envelope (`:49-57`).

`PatchState` (`pkg/state_patcher.go:691`) reads it with `UseNumber` (`:706`),
walks `deployment.resources`, and for each custom resource whose short Pulumi
type appears in the fields file, builds `patchFieldDescriptor`s (`:813`) and
calls `patchAndValidateResource` (`:826`).

Per field, `patchResourceFields` (`:495`):

1. **Reads the digest value** by TF attribute name (`:511`) and camelCases
   nested keys (`camelCaseKeys`, `:515` → `:1545`).
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

- `injectAssetDeltas` (`:663` → `:1165`) adds `{"asset": …}` entries to
  `__pulumi_raw_state_delta.obj.ps` for each patched asset field;
- `validateRecover` (`:671` → `:1496`) runs the bridge's own
  `UnmarshalRawStateDelta` + `Recover` over the patched outputs, and **on
  failure reverts inputs and outputs wholesale** (`:672-676`).

The revert is structural: it proves outputs and delta are mutually consistent,
not that either is right. Value correctness is checked only by the verifying
preview in stack mode (S7).

The result is re-serialized with `json.MarshalIndent` (`:856`). In file mode it
is written to `--out` (`cmd/patch_state_tf.go:351`) and the operator runs
`pulumi stack import`; in stack mode it is handed to injection and then to
`StackSession.Import` (`cmd/patch_state_tf.go:292`).

**Dead or near-dead machinery in this file.** `updateDeltaForPatchedOutputs`
(`:1206`) and `patchedOutputFieldInfo` (`:1147`) have no callers at all.
`conformToDelta` (`:1379`) is called only from `pkg/state_patcher_test.go`.
`PatchStateFromSchema` (`:1656`) — the schema-driven alternative to the curated
fields file — is called only from tests; no command wires it up. That is
consistent with the finding in [S-schema](#schema-forms-and-their-consumers)
that its default-fallback path cannot work in production.

### S6 — re-imported state

`pulumi stack import` runs `Snapshot.VerifyIntegrity`
(`pulumi/pkg/v3@v3.222.0/resource/deploy/snapshot.go`). The tool runs the same
check in-process first, `VerifyDeploymentIntegrity` (`pkg/state_verify.go:35`),
with an extra pre-check for an empty provider reference on a resource that has
an ID (`:48-53`). It is called at the end of `InjectNonImportable`
(`pkg/state_injector.go:154`), so an injected deployment is rejected before it
is written or imported rather than by the CLI afterwards.

Note the asymmetry: a **patch-only** run is not integrity-checked. `PatchState`
mutates values inside existing resources and adds no URNs, parents or provider
references, so it cannot produce the structural faults `VerifyIntegrity` looks
for — but nothing enforces that, and the check is cheap.

In stack mode the re-import goes through `StackSession.Import`
(`pkg/state_stack.go:71`), which unmarshals the envelope back into an
`apitype.UntypedDeployment` and calls `auto.Stack.Import`.

### S7 — injection of non-importable resources

`patch-state tf --non-importable` writes the resources `resolve tf` left out of
the import file directly into the deployment. `InjectNonImportable`
(`pkg/state_injector.go:69`) is the whole of it, and it **starts no provider**:
everything provider-derived was computed by `digest tf` in S2b and travels in
the sidecar (`:63-68`). The `providers` argument is only used for
`GetSchemaFieldInfo` name lookups (`:188`).

**The program is the source of truth for everything but the values.** A preview
of the user's program reports these resources as `create` steps — they are
declared, they simply could not be imported — and each step's `newState`
carries the URN, parent, provider reference, inputs and dependency edges the
engine computed. `CreatesByTypeName` (`pkg/preview.go:67`) indexes those by
Pulumi type and name; `buildInjectedResource` (`pkg/state_injector.go:164`)
copies `newState` wholesale (`:172-180`) and overrides only what the sidecar
knows:

| Field | Source |
|---|---|
| `urn`, `parent`, `provider`, `protect`, `dependencies`, `propertyDependencies`, … | `newState`, verbatim (`:172-180`) |
| `custom` | `true` (`:181`) |
| `id` | sidecar (`:182`) |
| `outputs` | `r.PulumiOutputs` from S2b, or `MapTFAttributesToPulumi(r.Attributes, fields)` for a sidecar written before S2b existed (`:195-203`) |
| `__pulumi_raw_state_delta`, `__meta` | `attachRawStateDelta` (`:304`) |
| `inputs` | `newState.inputs` plus `__defaults: []` when the engine did not already supply one (`:212-230`) |

Matching is strict in both directions: a sidecar entry listed twice is an error
(`:113-118`), and a sidecar entry with no matching create step is an error
(`:121-127`). There is no fallback heuristic.

**Two placeholders have to be resolved, from opposite directions.**
`(sensitive)` arrives in the *outputs* from the digest and is resolved by
`resolveOutputSecrets` (`:365`), which maps the TF attribute name to a Pulumi
name and looks the config key up in `r.RedactedAttributes`. `[secret]` arrives
in the *inputs* from the preview — `MassageSecrets` masks every secret property
in `pulumi preview --json` output — and is resolved by `resolveSecretInputs`
(`:416`), which maps the Pulumi name back to a TF name (falling back to
`tfbridge.PulumiToTerraformName` when no schema is loaded, `:442`) and wraps the
real value in Pulumi's secret envelope (`:463-466`). Both hard-error when the
value cannot be resolved (`:401`, `:446`, `:453`).

Because both resolutions depend on a name mapping being right,
`checkNoPlaceholders` (`:261`) then walks inputs and outputs recursively —
including nested objects and arrays, which the targeted resolvers do not — and
fails the injection if either literal survives anywhere (`:242-247`). That
backstop depends on no mapping at all.

**The delta is attached conservatively.** `attachRawStateDelta` (`:304`) writes
`__meta` whenever a non-zero schema version is known (`:310-314`), independently
of the delta, since a provider upgrade needs the version even when no delta
exists. It then drops the delta outright if its raw JSON contains `(sensitive)`
anywhere (`:324-327`) — substituting the real secret into outputs does not
change what a `Replace` node reconstructs, so such a delta would rebuild
Terraform state containing the placeholder — and otherwise validates it with
`validateRecover` and deletes it on failure (`:332-335`). Every drop increments
`InjectResult.NoDelta`, and the resource falls back to the bridge's pre-delta
reconstruction. This is what closes
[#25](https://github.com/pulumi-proserv/pulumi-tool-import/issues/25) for the
common case; the residue is exactly the `NoDelta` count.

`orderInjected` (`:479`) topologically sorts the batch so a resource appears
after its parent and after any injected resource it depends on, because
`VerifyIntegrity` rejects forward references. Only edges *within* the batch
matter — anything already in the deployment is necessarily earlier (`:476-478`).

**Stack mode wraps the cycle** (`pkg/state_stack.go`, driven from
`cmd/patch_state_tf.go:97-348`):

1. `Export` the deployment, write a timestamped backup to disk and print an
   absolute `pulumi stack import` recovery command — **before any mutation**
   (`cmd/patch_state_tf.go:104-149`).
2. `PreviewJSON` (`pkg/state_stack.go:88`) for the injection skeleton; that same
   preview is reused as the **baseline** (`cmd/patch_state_tf.go:254`). A
   patch-only run takes a baseline of its own, still before import (`:280-288`).
3. Patch, then inject, then `VerifyDeploymentIntegrity`, then `Import`.
4. `PreviewJSON` again and compare.

**The verification rule is a comparison, not a cleanliness check.**
`CheckInjectionVerification` (`pkg/state_stack.go:156`) requires two things:
every injected URN reports `same` (`CheckInjectedOps`, `:105`), and the run did
not make anything else worse — no URN that was `same` or absent in the baseline
is non-`same` afterwards (`:181-191`), and the count of non-`same` steps outside
the injected set does not increase (`:192-196`). A stack mid-migration
legitimately still has diffs, which is why the operator is running the tool at
all; demanding an absolutely clean preview would revert nearly every legitimate
patch-only pass (`:146-153`). `CheckPreviewClean` (`:129`) exists but is
diagnostic only — it reports how many operations remain outstanding (`:345`).

Any problem triggers `revertOrExplain` (`cmd/patch_state_tf.go:304`), which
re-imports the pre-mutation export and, if even that fails, prints the
hand-restore command pointing at the on-disk backup.

---

## The three bridge reserved keys

Defined in `pulumi-terraform-bridge/v3@v3.121.0/pkg/reservedkeys/keys.go`:
`Meta = "__meta"` (`:19`), `Defaults = "__defaults"` (`:26`),
`RawStateDelta = "__pulumi_raw_state_delta"` (`:30`).

**This repo never imports `reservedkeys`.** Every occurrence is a string
literal or a locally redeclared constant: `pkg/state_patcher.go:661`, `:663`,
`:670`, `:1380`, `:1424`, `:1497`, and `rawStateDeltaKey` / `metaKey` /
`reservedDefaultsKey` in `pkg/state_injector.go:38`, `:39`, `:291`. The
injector's comment (`:33-36`) says explicitly that it duplicates the constants
to match the rest of the package. All three keys are now written by the tool,
so the duplication has more surface than it did.

| Key | Written by | Read by this tool | Mutated by this tool |
|---|---|---|---|
| `__pulumi_raw_state_delta` | The bridge, during `pulumi import`. Computed by `ComputeInjectionState` (`pkg/raw_state_delta.go:69`) into the digest and sidecar, and written into outputs by `attachRawStateDelta` (`pkg/state_injector.go:329`). | `validateRecover` (`pkg/state_patcher.go:1497`), `attachRawStateDelta` (`pkg/state_injector.go:324`), `conformToDelta` (`pkg/state_patcher.go:1380`, tests only) | `injectAssetDeltas` (`:1165`) adds asset entries; `attachRawStateDelta` deletes the key when `Recover` fails (`pkg/state_injector.go:333`). `updateDeltaForPatchedOutputs` (`:1206`) would rebuild array deltas but is uncalled. Explicitly *not* copied from a preview create step (`pkg/state_injector.go:176-178`). |
| `__meta` | The bridge (schema version + private state). Now also by `attachRawStateDelta` (`pkg/state_injector.go:312`), from the sidecar's `SchemaVersion`. | Nothing reads it back. | `metaPayload` (`:342`) builds the bridge's own `{"schema_version":"N"}` string and omits it entirely for version 0 (`:350-352`), mirroring `tfbridge.MakeTerraformResult`. |
| `__defaults` | The bridge, on inputs; and by the injector, but **only when absent** (`pkg/state_injector.go:228-230`). | The same presence check. | Never overwritten: the engine's `Check` usually supplies a populated list, and replacing it with `[]` would discard what `Check` worked out (`:225-227`). |

The delta's contract, per the bridge's own `turnaroundCheck`
(`rawstate.go:522`), is that `Recover` must reproduce the raw state byte for
byte. That is why `patch-state` must edit it when it edits a value, and why
`validateRecover` reverting on failure is the correct conservative behaviour.
`Recover`'s empty-delta path, `rawStateRecoverNatural` (`rawstate.go:234`),
refuses object values outright (`:263`), which is why an absent delta and an
empty delta are not the same thing.

[#25](https://github.com/pulumi-proserv/pulumi-tool-import/issues/25) recorded
that injected resources were written without a delta at all, because computing
one appeared to need a `shim.InstanceState` the tool cannot build. This branch
closes that by taking the `RawStateComputeDelta` route instead (S2b). What
remains of the issue is the `NoDelta` residue: resources whose delta could not
be computed, embedded `(sensitive)`, or failed `Recover`.

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
default-fallback branch (`pkg/state_patcher.go:602`, fed at `:1765-1766`) can
never fire against a real provider. It fires in tests, which construct
`schema.Schema{Default: …}` directly. This is very likely why the curated
`data/aws-import-diff-fields.json` exists and why `PatchStateFromSchema` was
never wired to a command.

Consumers of the mock:
- `bridge.PulumiTypeToken` (`pkg/bridge/pulumi_type_token.go:28`) — Pulumi type
  token for a TF type, used to build URNs (`pkg/module_map.go:568`).
- `GetSchemaFieldInfo` (`pkg/schema_fields.go:72`) — TF→Pulumi names via
  `tfbridge.TerraformToPulumiNameV2` (`:93`), input/computed classification
  (`:103-104`), asset overlay (`:107-113`).
- `ComputeInjectionState` — passes `schemaMap` and `schemaInfos` straight into
  `MakeTerraformOutputs` and `RawStateComputeDelta`
  (`pkg/module_map.go:482-491` → `pkg/raw_state_delta.go:69`, `:110`).

Note `PulumiTypeToken` calls `camelPascalPulumiName`, which contains a
`contract.Assertf` on the resource-type prefix
(`pkg/bridge/pulumi_type_token.go:42`). That is a **panic**, not an error, and
`buildResourceURN` (`pkg/module_map.go:568`) only handles the error return. A
provider whose resources do not all share its `GetResourcePrefix()` would abort
the digest.

### 4. Curated data files

`data/aws-import-diff-fields.json`, loaded by `LoadFieldsFile`
(`pkg/state_patcher.go:1823`), and `pkg/importsupport/fallback.json`. These
encode facts about provider *Go code* — `Default`, `Read` behaviour, `Importer`
— that no schema exposes. `docs/aws-import-diff-fields.md` explains the
categories; `docs/non-importable-resources.md:135-145` explains why `Read`
semantics cannot be probed.

### Property naming: five mechanisms that can disagree

| Mechanism | Where | Basis |
|---|---|---|
| `tfbridge.TerraformToPulumiNameV2(tfName, schemaMap, fieldInfos)` | `pkg/schema_fields.go:93` | Schema-aware; handles pluralization from `MaxItems`, and `info.Schema.Name` overrides |
| `snakeToCamel` | `pkg/state_patcher.go:1532`, applied recursively by `camelCaseKeys` (`:1545`) | Pure string transform, no schema |
| `tfToPulumiField` / `pulumiToTFField` | `pkg/state_patcher.go:121`, `:147` | A 23-entry hand-written table |
| `tfbridge.MakeTerraformOutputs` | via `pkg/raw_state_delta.go:110` | The bridge's own conversion, schema-driven at every nested level |
| `tfbridge.PulumiToTerraformName` | `pkg/state_injector.go:442` | The bridge's generic reverse transform, used only when no schema describes the field |

`MapTFAttributesToPulumi` (`pkg/non_importable_file.go:54`) uses the first when
a field is in the schema and falls back to the second when it is not (`:60-63`),
which is the only place the two are reconciled explicitly. It is now live, as
the injector's fallback when a sidecar predates S2b
(`pkg/state_injector.go:202`).

The injector needs the mapping in **both** directions at once and gets each
from a different mechanism: `resolveOutputSecrets` goes TF→Pulumi through
`SchemaFieldInfo.PulumiName` with a `snakeToCamel` fallback
(`pkg/state_injector.go:385-388`), while `resolveSecretInputs` goes
Pulumi→TF through `PulumiToTFNames` with a `tfbridge.PulumiToTerraformName`
fallback (`:436-443`). The two fallbacks are not inverses of each other, which
is precisely why `checkNoPlaceholders` (`:261`) exists as a name-independent
backstop.

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
  from the Pulumi name through the hand table (`:815`); the latter takes both
  from the schema (`:1763-1764`). A field present in the fields file but absent
  from the table gets `TFName: ""` and is therefore never matched to a digest
  value at all (`:510`) — it can only ever be patched from its default.

---

## Representations and conversions

| # | Representation | Produced by | Converted to | By |
|---|---|---|---|---|
| 1 | `.tfstate` bytes | Terraform | `*states.State` | `statefile.Read`, `pkg/tofu_eval.go:147` |
| 1a | `tofu show -json` bytes | `tofu` | `tfjson.State` → `*states.State` | `pkg/generate_module_map.go:125`, `:259` |
| 2 | `AttrsJSON` (`[]byte`, exact) | Terraform | `map[string]interface{}` with `json.Number` | `decodeAttrs`, `pkg/module_map.go:432` |
| 3 | `[]cty.PathValueMarks` | Terraform | `(sensitive)` placeholders | `redactSensitivePaths`, `pkg/module_map.go:1047` |
| 3b | same | | stack config secrets | `DiscoverSensitiveSecrets` → `SetSecretsFromState`, `:724`, `:993` |
| 4 | `ModuleResource` | `matchResources`, `:328` | `tf-digest.json` | `WriteModuleMap`, `:1124` |
| 5 | `AttrsJSON` (redacted, re-marshalled) | `pkg/module_map.go:505` | `cty.Value` | `ctyjson.Unmarshal`, `pkg/raw_state_delta.go:58` |
| 6 | `cty.Value` | | `resource.PropertyMap` | `MakeTerraformOutputs`, `pkg/raw_state_delta.go:110` |
| 7 | `cty.Value` + `PropertyMap` | | `RawStateDelta` | `RawStateComputeDelta`, `:69` |
| 8 | `RawStateDelta` | | `map[string]interface{}` | `json.Marshal` + `decodeAttrs`, `:139` |
| 9 | `ModuleResource` | | `ImportEntry.ID` (string only) | `fillState.assign`, `pkg/import_filler.go:280` |
| 10 | `ModuleResource` | | `NonImportableResource` | `pkg/import_filler.go:265` |
| 11 | `ImportFile` | | `[]*optimport.ImportResource` | `pkg/batchimport/file.go:39` |
| 12 | — | `pulumi import` | deployment JSON (`apitype.DeploymentV3`) | the engine + bridge |
| 13 | `apitype.UntypedDeployment` | `auto.Stack.Export` | full envelope bytes | `StackSession.Export`, `pkg/state_stack.go:58` |
| 14 | deployment JSON | `pulumi stack export` | `map[string]interface{}` with `json.Number` | `pkg/state_patcher.go:706` |
| 15 | `map[string]interface{}` | `patchResourceFields`, `:495` | patched deployment JSON | `json.MarshalIndent`, `:856` |
| 16 | `pulumi preview --json` | `StackSession.PreviewJSON`, `pkg/state_stack.go:88` | `PreviewDigest` with `json.Number` | `ParsePreviewJSON`, `pkg/preview.go:54` |
| 17 | `PreviewDigest` | | `map[PreviewKey]newState` | `CreatesByTypeName`, `pkg/preview.go:67` |
| 18 | `NonImportableResource` + `newState` | | injected resource object | `buildInjectedResource`, `pkg/state_injector.go:164` |
| 19 | injected deployment JSON | `InjectNonImportable`, `:150` | `deploy.Snapshot` | `stack.DeserializeDeploymentV3`, `pkg/state_verify.go:55` |
| 20 | deployment bytes | | `apitype.UntypedDeployment` → stack | `StackSession.Import`, `pkg/state_stack.go:71` |

`shim.InstanceState` appears nowhere in this table. The tool never constructs
one and cannot: the only `shim.Resource` it holds is the mock, whose
`InstanceState` is a hard error (`tfshim/schema/resource.go:55`). This is the
reason `RawStateComputeDelta` (which needs only a type and a value) is used
instead of `RawStateInjectDelta` (`rawstate.go:458`, which needs an instance
state).

---

## Weaknesses and open questions

Ordered roughly by how much damage each can do.

### 1. `UseNumber` is applied at the reading end; the producing end is now only partly covered

**Partly fixed on this branch.** The original finding was that `UseNumber`
appeared only where the tool *read* a document it did not write, while every
site that *produced* one decoded plainly — so the precision was already gone
before the careful reader saw it. Three sites on the path into Pulumi state have
since been fixed:

| Site | Fix |
|---|---|
| `pkg/module_map.go:369` | `decodeAttrs` (`:432`) decodes `AttrsJSON` with `UseNumber`, so digest `attributes` carry `json.Number`. |
| `pkg/module_map.go:372` | `formatImportID` (`:446`) is still `%v`, but `json.Number` is a string type, so it now prints the original digits. |
| `cmd/import_id_match.go:86`, `cmd/patch_state_tf.go:165` | Both digest decodes use `UseNumber`, so the sidecar and the patched deployment keep exact integers. |

What the fix does **not** cover, and why each still matters:

| Site | Consequence |
|---|---|
| `pkg/generate_module_map.go:125`, `pkg/tofu/loader.go:82` | The `tofu show -json` entry path still loses precision before the digest is built. `decodeAttrs` cannot help: by the time it runs, `rawStateFromTfjson` has already re-marshalled a `float64` (`pkg/generate_module_map.go:296`). |
| `pkg/module_map.go:757`, `:777` | `DiscoverSensitiveSecrets` still decodes plainly and stringifies with `fmt.Sprintf("%v", …)`, so a numeric secret is corrupted on its way into stack config — and that config value is what injection and patching write back into state. |
| `cmd/resolve_cfn.go:39`, `cmd/patch_state_cfn.go:84` | The CFN digest decodes have the same shape as the two TF ones that were fixed. |
| `pkg/raw_state_delta.go:106` | Outputs pass through `float64`; unavoidable, since `resource.PropertyValue` numbers are `float64`. The delta itself is computed from the cty value and stays exact. |

**The failure mode is silence, not a parse error.** Measured: `json.Unmarshal` +
`json.Marshal` maps `1234567890123456789` to `1234567890123456800` — a wrong
value that is still valid JSON and still an integer. Scientific notation, which
Pulumi's state parser would reject loudly, only appears at ≥1e21. AWS account
IDs are not affected: they appear in state as strings. So the realistic damage
is a silently wrong large integer (snowflake IDs, epoch-nanosecond timestamps,
some resource IDs), not a crash.

The remaining fix is the same shape as the one applied: a single decode-with-
`UseNumber` helper used at every `json.Unmarshal` that touches state or digest
data. See
[#27](https://github.com/pulumi-proserv/pulumi-tool-import/issues/27).

### 2. Two provider loaders, neither able to do the other's job (#26)

`populateInjectionState` (`pkg/module_map.go:460`) is the clearest symptom: it
needs the live provider for the cty type *and* the bridge mock for Pulumi
naming, and bails out entirely if either is missing (`:471`, `:475`, `:492`).
The comment at `:492-503` documents that these are "two different loaders with
different failure modes" and that a mismatch produces a silently
under-populated digest.

Injection has made the consequence concrete rather than theoretical: a digest
built while the two disagreed carries no `pulumiOutputs`, so the injector falls
back to `MapTFAttributesToPulumi` (`pkg/state_injector.go:202`), and no
`rawStateDelta`, so the resource is injected without one and counted in
`NoDelta`. Both degradations are correct, and neither is distinguishable from
"this resource genuinely needed nothing".

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

- `redactSensitivePaths` (`pkg/module_map.go:1047`) handles top-level paths
  only; nested sensitive values reach the digest in plaintext (`:1062`).
- The `tofu show -json` path carries no sensitivity at all
  (`pkg/generate_module_map.go:302` vs `terraform-json/state.go:164`), so on
  that path *nothing* is redacted and no secret is written to config.
- `BuildSensitivityMap` (`pkg/provider_schema.go:44`) — the schema-driven
  implementation that handles nesting — is unused.
- The digest↔config link is the pure function `flattenAddress`
  (`pkg/module_map.go:844`), recomputed at four sites
  (`:793`, `pkg/import_filler.go:102`, `pkg/state_patcher.go:554`, and
  indirectly via `RedactedAttributes` in `pkg/state_injector.go:383`), and only
  the first applies dedup suffixes (`:803`). Colliding keys are therefore
  unresolvable downstream, with only a digest-time warning.
- The **second placeholder is now live**, not just designed: `[secret]`, from
  `MassageSecrets` on the preview path, arrives in injected *inputs* while
  `(sensitive)` arrives in injected *outputs*, and each is resolved by a
  different function using a different name mapping
  (`pkg/state_injector.go:365`, `:416`). `checkNoPlaceholders` (`:261`) is the
  name-independent backstop that makes the pair safe, and its existence is a
  fair measure of how hard the naming is to get right.

Recording `RedactedAttributes` in the digest itself (rather than recomputing it
in `resolve tf` from a string match against `"(sensitive)"`,
`pkg/import_filler.go:98`) would make the link explicit and dedup-safe. See
[#28](https://github.com/pulumi-proserv/pulumi-tool-import/issues/28).

### 4. `patch-state` can only repair what the digest kept, and only by name

`PatchState` matches state resources to digest resources by Pulumi resource
*name* (`pkg/state_patcher.go:782`, built by `BuildDigestNameMap` at `:307`),
with a chain of fallbacks ending in "exactly one unused candidate of this type"
(`:412`, `:461`). A mismatch is reported only as an aggregate `NoMatch` count
(`:845`) — there is no per-resource report of what went unmatched, and no way to
assert that a resource the operator cares about was matched.

The same fallback exists in `FillImportFile` (`pkg/import_filler.go:364`) with
a warning, and in `matchChildren`'s normalized-name pass
(`pkg/state_patcher.go:389-400`). Three near-identical matchers with slightly
different rules is a simplification opportunity: `FillImportFile` and
`BuildDigestNameMap` are the same algorithm over different input shapes.

Injection deliberately went the other way. `InjectNonImportable` matches on
Pulumi type plus name against preview create steps and **fails** on a duplicate
or a miss (`pkg/state_injector.go:113-127`) rather than falling back to a
"single candidate" guess. It is the same matching problem with the opposite
policy, and the stricter policy is the one that produces an actionable error.

### 5. Dead and half-wired code obscures which path is real

- `updateDeltaForPatchedOutputs` (`pkg/state_patcher.go:1206`): no callers.
- `conformToDelta` (`:1379`): tests only.
- `PatchStateFromSchema` (`:1656`): tests only, and its default path is
  unreachable in production (see §2).
- `BuildSensitivityMap`, `RedactSensitiveAttributes` (`pkg/provider_schema.go`):
  tests only.

**Resolved for the #22 group.** `ParsePreviewJSON`, `VerifyDeploymentIntegrity`,
`LoadNonImportableFile`, `MapTFAttributesToPulumi` and `PulumiToTFNames` were
listed here as built-but-unwired; all five are now on the live injection path
(`pkg/state_injector.go:99`, `:154`, `:202`, `:422`, `cmd/patch_state_tf.go:246`).

What remains is the first four, and `PatchStateFromSchema` in particular still
reads like a supported alternative to the curated fields file when it cannot
currently work.

### 6. The digest is the only inter-command contract, and it is untyped at the boundaries

`ModuleMap` is decoded into a struct whose `Attributes` is
`map[string]interface{}`. There is no schema version on the file, no checksum,
and no record of which provider versions produced it — only `mm.Providers`, a
map whose values are always the empty string (`pkg/module_map.go:155`). A digest
built against AWS provider 5.x and consumed by a `patch-state` run against 7.x
is indistinguishable from a matched pair.

That map has since grown a second job: `loadProvidersForDigest`
(`cmd/patch_state_tf.go:417`) uses its *keys* to re-resolve provider schemas at
injection time without a Terraform state file, passing `nil` versions
(`:429`). So the tool now depends on the digest recording which providers were
involved, while still recording nothing about which versions — and the injected
property names come from whatever version `RecommendPulumiProvider` picks today.

`ImportSupportChecked` (`:50`) is the one piece of provenance that *is*
recorded, and `resolve tf` uses it well (`cmd/import_id_match.go:174`). The
same treatment for provider versions and for "injection state was computed /
was attempted and failed" would let consumers tell absence from failure —
today `PulumiOutputs == nil` means both, and the injector's `NoDelta` count
collapses three distinct causes into one number
(`pkg/state_injector.go:46-50`).

### 7. Verification is structural, except in stack mode

**Partly fixed on this branch.** `validateRecover`
(`pkg/state_patcher.go:1496`) checks delta↔outputs consistency;
`VerifyDeploymentIntegrity` (`pkg/state_verify.go:35`) checks snapshot
structure. Neither checks a value against the cloud, and `refresh` cannot
either for the types that matter — it reports these resources unchanged even
when their values are wrong.

Stack mode now closes that: it runs `pulumi preview --json` before and after
the mutation and reverts on regression (`pkg/state_stack.go:156`,
`cmd/patch_state_tf.go:313-339`). Three things about that gate are worth being
precise about, because each is easy to misread:

- **It is a comparison, not a clean bill.** A stack mid-migration legitimately
  has diffs. Requiring an absolutely clean preview would revert nearly every
  legitimate patch-only pass, so the bar is "no regression versus the baseline"
  plus "every injected URN reports `same`" (`pkg/state_stack.go:146-153`).
- **The baseline is the pre-mutation preview**, reused from the injection
  skeleton when there is one (`cmd/patch_state_tf.go:254`). It is therefore
  taken against unpatched state, which is what makes the comparison meaningful.
- **It covers stack mode only.** File mode writes `--out` and prints the
  verification instructions (`cmd/patch_state_tf.go:380-383`); nothing enforces
  that the operator follows them. The air-gapped path is still unverified by
  construction.

The residual gap: `CheckInjectionVerification` compares *counts* of non-`same`
steps outside the injected set (`pkg/state_stack.go:192-196`) as well as
per-URN regressions. A run that fixes one neighbouring resource and breaks
another nets to zero on the count, but the per-URN `newlyDirty` check
(`:181-191`) catches it — so the count is a backstop for churn the per-URN pass
cannot see (resources absent from one preview entirely), not the primary gate.

### 8. Related, already tracked

- [#22](https://github.com/pulumi-proserv/pulumi-tool-import/issues/22) —
  non-importable state injection; S2b, S3b and S7 above. Implemented on this
  branch.
- [#24](https://github.com/pulumi-proserv/pulumi-tool-import/issues/24) —
  splitting large workspaces into shard stacks. Relevant here because it needs
  the same export/verify/import helpers `pkg/state_stack.go` now provides, and
  because sharding multiplies the digest-provenance problem in §6.
- [#25](https://github.com/pulumi-proserv/pulumi-tool-import/issues/25) — the
  raw state delta gap. Closed for the common case by S2b; the residue is the
  `NoDelta` count.
- [#26](https://github.com/pulumi-proserv/pulumi-tool-import/issues/26) — the
  two loaders; §2.
- [#27](https://github.com/pulumi-proserv/pulumi-tool-import/issues/27) — JSON
  number precision; §1. Three sites fixed on this branch, the rest outstanding.
- [#28](https://github.com/pulumi-proserv/pulumi-tool-import/issues/28) —
  sensitive values: the three implementations, the two uncovered paths, and the
  recomputed config-key link; §3.

### Not traced

- What the engine and bridge write into `__meta` and `__defaults` during
  `pulumi import`, in detail. The tool now writes both on the injection path,
  but modelled on the bridge rather than read from its write sites:
  `metaPayload` (`pkg/state_injector.go:342`) mirrors what
  `tfbridge.MakeTerraformResult` produces, and the `__defaults` handling assumes
  the engine's `Check` populates the list. Neither assumption was verified
  against the bridge source; both are asserted only by unit test.
- Whether `__meta` carries anything besides `schema_version` for the resource
  types injection targets. `metaPayload` writes only that key, which is correct
  for a resource with no provider private state — but "these types have none"
  was not established.
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
