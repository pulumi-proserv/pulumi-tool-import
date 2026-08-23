# How schema and state travel through the import pipeline

A reference for the two things the tool moves: **state** (resource values, from
Terraform to a Pulumi deployment) and **schema** (provider metadata, needed to
rename and reshape those values). It records, at each hop, what representation
the data is in, what is added, what is dropped, and what cannot be recovered
afterwards.

This is a diagnostic document. Where a stage is lossy or a dependency is
awkward, it says so; where a claim could not be established from the source, it
says that instead of guessing.

> **On the line numbers.** This document carries roughly 290 of them, and they
> rot fast — a single day's changes to `pkg/state_injector.go` invalidated every
> anchor in the S7 section, several of which then pointed at blank lines and
> closing braces. **Treat function and identifier names as authoritative and
> line numbers as a hint.** S7 has been rewritten without them as the pattern to
> follow; the rest are unverified after any given change. Versions referenced
> are `github.com/pulumi/pulumi-terraform-bridge/v3@v3.121.0` and
> `github.com/pulumi/pulumi/pkg/v3@v3.222.0`.

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
([cmd/import_id_match.go:85](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/cmd/import_id_match.go#L85)) and again by `patch-state tf`
([cmd/patch_state_tf.go:173](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/cmd/patch_state_tf.go#L173)), and everything either command knows about
Terraform values comes from it. Nothing downstream re-reads Terraform state.

`patch-state tf` has two modes ([cmd/patch_state_tf.go:88](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/cmd/patch_state_tf.go#L88)). **File mode**
(`--state` + `--out`) reads and writes files and leaves `pulumi stack import` to
the operator. **Stack mode** (`--project-dir` + `--stack`, no `--state`;
`--out` optionally writes the verified state to a file after verification
passes) drives the whole export → patch → inject → import → verify cycle
through the Automation API (`pkg/state_stack.go`). Injection of non-importable
resources (`--non-importable`) works in both, but file mode additionally needs
`--preview-json` because the program metadata cannot come from anywhere else
([cmd/patch_state_tf.go:100](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/cmd/patch_state_tf.go#L100)).

Two provider loaders run, for different reasons; `resolveInjectionProviders`
(`pkg/provider_pair.go`) is where the pair is correlated, naming the missing
half when one is absent:

| Loader | Started by | Protocol | Purpose |
|---|---|---|---|
| `tfprovider.LoadProvider` ([pkg/tfprovider/loader.go:65](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/tfprovider/loader.go#L65)) | `importsupport.Prober` ([pkg/importsupport/prober.go:184](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/importsupport/prober.go#L184)), `BuildSensitivityMap` ([pkg/provider_schema.go:77](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/provider_schema.go#L77)) | go-plugin, real Terraform provider binary | `ImportResourceState` probe; `GetProviderSchema` for cty types |
| `PulumiProvidersForTerraformProviders` ([pkg/pulumi_providers.go:75](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/pulumi_providers.go#L75)) | `GenerateModuleMap` ([pkg/generate_module_map.go:119](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/generate_module_map.go#L119), [:134](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/generate_module_map.go#L134)); `loadProvidersForDigest` ([cmd/patch_state_tf.go:479](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/cmd/patch_state_tf.go#L479)) | Pulumi plugin gRPC, `GetMapping("terraform")` | Pulumi type tokens and property names |

---

## State, stage by stage

### S1 — Terraform state file → in-memory state

Two entry formats, detected by the presence of `format_version`
([pkg/tofu_eval.go:53](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/tofu_eval.go#L53)).

**Raw `.tfstate`** goes through OpenTofu's own reader,
`statefile.Read` ([pkg/tofu_eval.go:147](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/tofu_eval.go#L147)), producing `*states.State`. Each
instance keeps:

- `inst.Current.AttrsJSON` — the **original bytes**, untouched. Number
  fidelity is intact at this point.
- `inst.Current.AttrSensitivePaths` — `[]cty.PathValueMarks`, Terraform's own
  record of which attribute paths are sensitive.
- the instance key (`res.Instances` is keyed by `addrs.InstanceKey`).

**`tofu show -json`** goes through `json.Unmarshal` into `tfjson.State`
([pkg/generate_module_map.go:128](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/generate_module_map.go#L128)), then `rawStateFromTfjson`
([pkg/generate_module_map.go:262](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/generate_module_map.go#L262)) rebuilds a synthetic `*states.State` by
re-marshalling `r.AttributeValues` ([:299](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/generate_module_map.go#L299)).

That second path loses three things at once:

1. **Numbers.** `json.Unmarshal` without `UseNumber` turns every JSON number
   into `float64`, silently rounding integers above 2^53. Both entry paths
   decode with precision kept: the raw `.tfstate` path via `decodeAttrs`
   ([pkg/module_map.go:459](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L459)), this path via
   `State.UseJSONNumber(true)` — the only hook that reaches `tfjson`'s own
   decoder, whose custom `UnmarshalJSON` ignores `UseNumber` at the call
   site. The consuming-side ceiling — `resource.PropertyValue` holds numbers
   as `float64` — is
   [#29](https://github.com/pulumi-proserv/pulumi-tool-import/issues/29).
2. **Sensitivity.** `sensitivePathsFromTfjson` derives sensitive paths from
   `tfjson.StateResource.SensitiveValues` (`terraform-json@v0.27.1/state.go:164`)
   and `rawStateFromTfjson` sets them on the instance
   ([pkg/generate_module_map.go:316](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/generate_module_map.go#L316)), nested leaves included.

   This matters more than it looks: the format is selected automatically
   whenever the state carries a `format_version` key, with no flag to indicate
   it, so an empty `AttrSensitivePaths` here would silently disable redaction,
   the config-key discovery and the sidecar's redacted-attribute map all at
   once. See [#28](https://github.com/pulumi-proserv/pulumi-tool-import/issues/28)
   for the duplicate sensitivity implementations elsewhere.
3. **Instance keys.** The resource instance is always registered under
   `addrs.NoKey` ([pkg/generate_module_map.go:304](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/generate_module_map.go#L304)), and
   `tfjson.StateResource.Index` (`state.go:142`) is not read. Two instances of a
   counted or `for_each` resource in the same module collide on the same key;
   the last one visited wins.

`LoadTerraformState` ([pkg/tofu/loader.go:73](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/tofu/loader.go#L73)) is a third entry point that
shells out to `tofu`; it is used by the `--state-file`-less flows and does a
`registry.terraform.io/` → `registry.opentofu.org/` textual rewrite of the state
JSON ([pkg/tofu/loader.go:285](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/tofu/loader.go#L285)) when OpenTofu cannot resolve Terraform-registry
provider references. That rewrite is applied to the whole document as a string,
so it would also rewrite the substring inside an attribute value. In practice
that string appears only in provider references; it has not been observed to
corrupt an attribute, and no guard exists.

### S2 — in-memory state → `ModuleMap` (the digest)

`matchResources` ([pkg/module_map.go:355](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L355)) walks the state and emits one
`ModuleResource` ([pkg/module_map.go:54](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L54)) per current instance.

| Field | Source | Notes |
|---|---|---|
| `TerraformAddress` | `res.Addr` + instance key + module addr ([:384-389](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L384-L389)) | The join key for everything downstream. |
| `ImportID` | `attrs["id"]` via `formatImportID` ([:399](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L399), definition [:473](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L473)) | Stringified with `%v`, which is safe: `json.Number` is a string type, so `%v` prints the original digits. |
| `Attributes` | `decodeAttrs(AttrsJSON)` ([:396](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L396), definition [:459](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L459)), then redacted | Decodes with `UseNumber`, so integers above 2^53 survive as `json.Number`. |
| `TranslatedURN` | `buildResourceURN` ([:592](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L592)) | Needs the Pulumi provider mapping; falls back to the raw TF address when absent ([:601](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L601), [:606](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L606), [:611](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L611)). |
| `Mode` | `managed` / `data` ([:405-407](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L405-L407)) | Data sources get no URN and are never imported. |
| `NonImportable` | `importChecker.Check` ([:435](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L435)) | See S2b. |
| `PulumiOutputs`, `RawStateDelta`, `SchemaVersion` | `populateInjectionState` ([:494](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L494)) | Only for non-importable resources. See S2b. |

**Redaction.** `redactSensitivePaths` ([pkg/module_map.go:1135](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L1135)) replaces each
sensitive attribute's value with the literal string `(sensitive)`
([:1198](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L1198)). It walks a sensitive path to **any depth** via
`redactAtPath` ([:1162](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L1162)), following `GetAttrStep` into objects and `IndexStep` into
lists and maps.

Depth matters because OpenTofu records deeper paths: an `aws_mq_broker`
`user[].password` yields a length-3 path, and `ResourceInstanceObject.Encode`
stores it with no depth filter. Where a set index cannot be resolved to an
ordinal — a set index IS the element value, not a position — **every** element
at that level is redacted instead: over-redacting costs a placeholder the
operator must supply, under-redacting writes a secret into state. Redaction
happens *before*
`populateInjectionState`, deliberately, so the raw state delta can never embed a
secret ([:423-430](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L423-L430)).

**Where the real value goes.** Separately, `DiscoverSensitiveSecrets`
([pkg/module_map.go:765](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L765)) re-parses the same `AttrsJSON` ([:805](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L805)) and reads the
*unredacted* value ([:819](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L819)), keying it by `flattenAddress(address, attribute)`
([:855](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L855), definition at [:932](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L932)). `SetSecretsFromState` ([:1081](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L1081)) writes those into
Pulumi stack config as secrets. So after `digest tf`:

- the digest holds `(sensitive)`;
- the stack config holds the real value under a flattened key;
- the mapping between them is *not written down anywhere* — it is recomputed by
  calling `flattenAddress` again, in `redactedAttributeKeys`
  ([pkg/import_filler.go:107](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/import_filler.go#L107)) and in `patchResourceFields`
  ([pkg/state_patcher.go:554](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L554)), and a fourth time in `resolveOutputSecrets` /
  `resolveSecretInputs` ([pkg/state_injector.go:647](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_injector.go#L647), [:716](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_injector.go#L716)), which look the
  recomputed key up in `r.RedactedAttributes`.

`flattenAddress` is therefore a load-bearing pure function whose output is a
cross-command contract.

**Colliding keys are a hard error.** `digest tf` fails and names both addresses,
and the discovery walk is sorted so the result is reproducible.

It has to be an error rather than a rename, because the key is recomputed
independently at four sites and only the discovery walk could ever know a
suffix had been applied. Appending `_2` would produce a config entry nothing
could read back, and the second colliding resource would resolve to the
**first one's secret** — a real secret written into the wrong resource's state.

Collisions are easy to reach because `flattenAddress` drops the resource type
and collapses punctuation: `module.db.aws_db_instance.this` and
`module.db.aws_rds_cluster.this` both flatten to `db_password`, as do
`ssm_parameters["/develop/api/key"]` and `ssm_parameters["/develop/api_key"]`.
Removing the error would need the resolved key recorded on the digest so
`resolve tf` consumes it instead of recomputing — which means writing the digest
after secret discovery, a pipeline reorder rather than a local change.

`DiscoverSensitiveSecrets` now decodes with `decodeAttrs` (`UseNumber`) before
stringifying with `fmt.Sprintf("%v", value)`. It previously used a plain
`json.Unmarshal`, which turned a sensitive `1234567890123456789` into
`"1.2345678901234568e+18"` in stack config — and injection then resolved that
key and wrote the corrupted, retyped value into state as the resource's real
secret. `json.Number` is itself a string type, so `%v` prints the original
digits.

`BuildSensitivityMap` / `RedactSensitiveAttributes` ([pkg/provider_schema.go:44](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/provider_schema.go#L44),
[:235](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/provider_schema.go#L235)) implement a second, schema-driven redaction mechanism that reads
`Sensitive` off the *live* provider schema. Nothing outside tests calls either.
See [#28](https://github.com/pulumi-proserv/pulumi-tool-import/issues/28).

Depth and format coverage are handled in the primary mechanism instead:
`redactAtPath` walks a sensitive path to any depth, and `rawStateFromTfjson`
populates `AttrSensitivePaths` from the `tofu show -json` format's own
`sensitive_values` document.

A nested secret redacts to a **path-tagged placeholder** —
`(sensitive:user[0].password)` — whose tag is the correlation key: the
sidecar's `RedactedAttributes` records the same rendered path against a stack
config key (`flattenAddressPath`), discovery writes the value under that key,
and injection's `resolveTaggedPlaceholders` substitutes it back by string
equality on the tag, with no Pulumi→Terraform name inversion anywhere.
Top-level attributes keep the bare `(sensitive)` form and their original keys.
The one nested case that still hard-fails the digest is a schema-marked but
state-unmarked attribute: with no state mark there is no concrete path to tag
(#28).

### S2b — the non-importable enrichment

When `Check` returns `Unsupported`, `populateInjectionState`
([pkg/module_map.go:494](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L494)) computes three extra fields — `PulumiOutputs`,
`RawStateDelta` and `SchemaVersion` — which are what makes S7 injection
possible without a provider. It needs **both** loaders at once:

- the live Terraform provider, obtained by type-asserting the
  `ImportSupportChecker` to `ProviderAccessor` ([:503](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L503), interface at [:127](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L127),
  implementation at [pkg/importsupport/prober.go:160](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/importsupport/prober.go#L160)) — for the cty type;
- the Pulumi bridge mock, from `pulumiProviders` ([:518-525](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L518-L525)) — for Pulumi
  naming.

`ComputeInjectionState` ([pkg/raw_state_delta.go:56](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/raw_state_delta.go#L56)) then does:

```
sch := prov.GetProviderSchema(ctx).ResourceTypes[tfType]   // :64-65
ty  := sch.Block.ImpliedType()                             // :70   cty.Type
val := ctyjson.Unmarshal(attrsJSON, ty)                    // :71   cty.Value
props := pulumiOutputsFromCty(ctx, val, schemaMap, schemaInfos)  // :115
                                  // → MakeTerraformOutputs at :228, PropertyMap
delta := RawStateComputeDelta(ctx, schemaMap, schemaInfos,
             props, FromCtyType(stripTimeouts(ty)), FromCtyValue(val))  // :153
version := sch.Version                                     // returned from :143,
                                  // :160, :184, :189, :199, :202 — one per outcome
```

Three notes on this hop:

- The value round-trips through `ctyjson.Marshal` → `json.Unmarshal`
  ([pkg/raw_state_delta.go:218-224](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/raw_state_delta.go#L218-L224)) with no `UseNumber`, and
  `MakeTerraformOutputs` produces `resource.PropertyValue` numbers, which are
  `float64` by definition. **Integer fidelity beyond 2^53 cannot survive into
  `PulumiOutputs` at all**, whatever the decoder does. `RawStateDelta` is
  computed from the cty value, so the delta itself is exact; the outputs it
  applies to are not.
- `stripTimeouts` ([:244](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/raw_state_delta.go#L244)) replicates a bridge behaviour that has no zclconf
  equivalent. It is a copy, and will drift.
- A delta that cannot be computed yields no error, but `ComputeInjectionState`
  returns a `deltaUnavailableReason` alongside it ([pkg/raw_state_delta.go:64](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/raw_state_delta.go#L64)), which
  `populateInjectionState` prints and records on the resource as
  `RawStateDeltaReason`. A panic is caught and turned into "no fields"
  (`safeComputeInjectionState`, [pkg/module_map.go:567](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L567)), and a missing schema
  map returns early. The digest is written either way, and a consumer can
  distinguish "needs no delta" from "computing it blew up" because
  `patch-state` names the cause per resource.
- The injector reports **`Deltas attached (injected): X of Y`** plus a named
  reason for each resource that has none, in three separate categories: the
  sidecar carried none, the delta embedded an unresolvable `(sensitive)`, or it
  failed `Recover` against the outputs. The count is reported positively rather
  than inferred from those three being zero, because a missing delta does not
  fail anything: the resource degrades to the bridge's legacy state conversion
  and still previews as `same`, so a regression to zero deltas is otherwise
  invisible.

### S3 — digest → import file

`FillImportFile` ([pkg/import_filler.go:125](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/import_filler.go#L125)) matches digest resources to
placeholder entries in a `pulumi preview --import-file` skeleton and writes
`entry.ID = tfRes.ImportID` ([:293](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/import_filler.go#L293)). **Only the ID crosses.** Attributes,
outputs, deltas — none of it is in the import file, because `ImportEntry`
([:23](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/import_filler.go#L23)) has nowhere to put it and `pulumi import` would discard it anyway (see
the spec's first finding: `ImportStep.Apply` calls `prov.Read` and writes the
*provider's* values).

Matching is by Pulumi type plus name suffix, with a "exactly one candidate of
this type" fallback (`matchChildren`, [:339](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/import_filler.go#L339), fallback at [:377](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/import_filler.go#L377)). The fallback
is the only place where a wrong resource can be silently assigned an ID.

`TranslateImportIDs` ([:465](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/import_filler.go#L465)) then rewrites IDs for ~16 hardcoded AWS types
whose Pulumi import format differs from Terraform's, reading fields back out of
`tf.Attributes` ([:494](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/import_filler.go#L494)). This is the one place where digest *attributes*
influence the import file. It reads `from_port`/`to_port` through
`fmt.Sprintf("%v", …)` ([:534-535](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/import_filler.go#L534-L535)), which is float-formatted; ports are small
enough that this is currently harmless.

Non-importable resources are diverted here rather than filled: `assign`
([:266](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/import_filler.go#L266)) appends a `NonImportableResource` ([:52](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/import_filler.go#L52)) and marks the entry dropped
([:290](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/import_filler.go#L290)); dropped entries are removed from the file ([:233-242](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/import_filler.go#L233-L242)).

### S3b — digest → non-importable sidecar

`writeNonImportable` ([cmd/non_importable.go:38](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/cmd/non_importable.go#L38)) writes
`<out>.non-importable.json`. This carries everything S3 could not:
`Attributes`, `RedactedAttributes`, `PulumiOutputs`, `RawStateDelta`,
`SchemaVersion` ([pkg/import_filler.go:283-288](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/import_filler.go#L283-L288)).

The digest is read at [cmd/import_id_match.go:86](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/cmd/import_id_match.go#L86) with `UseNumber` and the
sidecar is written with `json.MarshalIndent` ([cmd/non_importable.go:55](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/cmd/non_importable.go#L55)), so
numbers now cross as `json.Number` and reach `LoadNonImportableFile`
([pkg/non_importable_file.go:36](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/non_importable_file.go#L36)) — which reads them back with `UseNumber` —
exactly as they were in Terraform state.

The sidecar is consumed by `patch-state tf --non-importable`
([cmd/patch_state_tf.go:260](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/cmd/patch_state_tf.go#L260)) — see S7.

### S4 — import file → `pulumi import` → deployment

`pkg/batchimport` decodes the file straight into `[]*optimport.ImportResource`
([pkg/batchimport/file.go:30](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/batchimport/file.go#L30)) and hands it to `auto.Stack.ImportResources`
([pkg/batchimport/stack.go:55](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/batchimport/stack.go#L55)), in batches, with `Protect(false)` and
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

In file mode the exported deployment comes from disk ([cmd/patch_state_tf.go:166](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/cmd/patch_state_tf.go#L166));
in stack mode from `StackSession.Export` ([pkg/state_stack.go:58](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_stack.go#L58)), which
re-marshals the whole `{"version":…,"deployment":{…}}` envelope because
`auto.Stack.Export` hands back only the inner object and every consumer here
reads the envelope ([:49-57](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_stack.go#L49-L57)).

`PatchState` ([pkg/state_patcher.go:728](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L728)) reads it with `UseNumber` ([:743](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L743)),
walks `deployment.resources`, and for each custom resource whose short Pulumi
type appears in the fields file, builds `patchFieldDescriptor`s ([:850](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L850)) and
calls `patchAndValidateResource` ([:863](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L863)).

Per field, `patchResourceFields` ([:495](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L495)):

1. **Reads the digest value** by TF attribute name ([:511](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L511)) and camelCases
   nested keys (`camelCaseKeys`, [:514](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L514) → [:1674](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L1674)).
2. **Builds asset sentinels** for asset-typed fields ([:527-550](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L527-L550)), which may
   reach out to AWS and download Lambda code ([:540](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L540)).
3. **Resolves `(sensitive)`** by recomputing `flattenAddress` ([:554](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L554)) and
   looking it up in `configSecrets`, then wrapping the value in Pulumi's secret
   envelope with the signature written as a string literal ([:561](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L561)).
4. **Writes inputs** only when the existing input is empty or of the wrong
   shape ([:576](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L576)), preferring the digest value, falling back to the schema/file
   default ([:603](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L603)).
5. **Writes outputs** only for simple values and asset sentinels ([:613](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L613)) —
   arrays and objects are deliberately not patched into outputs, because the
   bridge may have reshaped them.

Then, in `patchAndValidateResource` ([:661](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L661)):

- `injectAssetDeltas` ([:700](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L700) → [:1207](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L1207)) adds `{"asset": …}` entries to
  `__pulumi_raw_state_delta.obj.ps` for each patched asset field;
- `validateRecover` ([:708](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L708) → [:1625](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L1625)) runs the bridge's own
  `UnmarshalRawStateDelta` + `Recover` over the patched outputs, and **on
  failure reverts inputs and outputs wholesale** ([:709-713](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L709-L713)).

The revert is structural: it proves outputs and delta are mutually consistent,
not that either is right. Value correctness is checked only by the verifying
preview in stack mode (S7).

The result is re-serialized with `json.MarshalIndent` ([:893](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L893)). In file mode it
is written to `--out` ([cmd/patch_state_tf.go:379](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/cmd/patch_state_tf.go#L379)) and the operator runs
`pulumi stack import`; in stack mode it is handed to injection and then to
`StackSession.Import` ([cmd/patch_state_tf.go:320](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/cmd/patch_state_tf.go#L320)).

**Dead or near-dead machinery in this file.** `updateDeltaForPatchedOutputs`
([:1248](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L1248)) and `patchedOutputFieldInfo` ([:1189](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L1189)) have no callers at all.
`conformToDelta` ([:1421](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L1421)) is called only from `pkg/state_patcher_test.go`.
`PatchStateFromSchema` ([:1785](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L1785)) — the schema-driven alternative to the curated
fields file — is called only from tests; no command wires it up. That is
consistent with the finding in [S-schema](#schema-forms-and-their-consumers)
that its default-fallback path cannot work in production.

### S6 — re-imported state

`pulumi stack import` runs `Snapshot.VerifyIntegrity`
(`pulumi/pkg/v3@v3.222.0/resource/deploy/snapshot.go`). The tool runs the same
check in-process first, `VerifyDeploymentIntegrity` ([pkg/state_verify.go:37](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_verify.go#L37)),
with an extra pre-check for an empty provider reference on a resource that has
an ID ([:48-56](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_verify.go#L48-L56)). It is called at the end of `InjectNonImportable`
([pkg/state_injector.go:232](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_injector.go#L232)), so an injected deployment is rejected before it
is written or imported rather than by the CLI afterwards.

Note the asymmetry: a **patch-only** run is not integrity-checked. `PatchState`
mutates values inside existing resources and adds no URNs, parents or provider
references, so it cannot produce the structural faults `VerifyIntegrity` looks
for — but nothing enforces that, and the check is cheap.

In stack mode the re-import goes through `StackSession.Import`
([pkg/state_stack.go:71](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_stack.go#L71)), which unmarshals the envelope back into an
`apitype.UntypedDeployment` and calls `auto.Stack.Import`.

### S7 — injection of non-importable resources

`patch-state tf --non-importable` writes the resources `resolve tf` left out of
the import file directly into the deployment. `InjectNonImportable`
(`pkg/state_injector.go`) is the whole of it, and it **starts no provider**:
everything provider-derived was computed by `digest tf` in S2b and travels in
the sidecar. The `providers` argument is used only for `GetSchemaFieldInfo` name
lookups.

> Function names rather than line numbers below. The previous version of this
> section anchored everything to line numbers and every one of them was stale
> within a day of the code changing.

**The program is the source of truth for everything but the values.** A preview
of the user's program reports these resources as `create` steps — they are
declared, they simply could not be imported — and each step's `newState` carries
the URN, parent, provider reference, inputs and dependency edges the engine
computed. `CreatesByTypeName` (`pkg/preview.go`) indexes those by Pulumi type and
name; `buildInjectedResource` copies `newState` wholesale and overrides only what
the sidecar knows:

| Field | Source |
|---|---|
| `urn`, `parent`, `provider`, `protect`, `dependencies`, `propertyDependencies`, … | `newState`, verbatim |
| `custom` | `true` |
| `id` | sidecar — **rejected if empty**, see below |
| `outputs` | `r.PulumiOutputs` from S2b, or `MapTFAttributesToPulumi(r.Attributes, fields)` for a sidecar written before S2b existed |
| `__pulumi_raw_state_delta`, `__meta` | `attachRawStateDelta` |
| `inputs` | `newState.inputs` plus `__defaults: []` when the engine did not already supply one |

Matching is strict: a sidecar entry listed twice is an error, and a sidecar entry
with no matching create step is an error. There is no fallback heuristic.

An **ambiguous** create key is different from a missing one. `PreviewKey` is
(type, name), but a parented URN's type segment is `parentType$childType` while
the sidecar records only the child's own type — so two same-named resources under
different components collapse to one key. That is legal (a Terraform module
instantiated twice, mapped to a Pulumi component, produces it), so
`CreatesByTypeName` records the ambiguity and `PreviewCreates.Lookup` reports it
**only if a sidecar entry actually needs that key**. Failing at index time would
block injection of every unrelated resource.

#### Values that must never reach state

Three placeholders can arrive in a resource being injected, from three
directions, and each is handled where it arrives:

- **`(sensitive)`** arrives in the *outputs* from the digest. `resolveOutputSecrets`
  maps the Terraform attribute name to a Pulumi name and looks the config key up
  in `r.RedactedAttributes`.
- **`[secret]`** arrives in the *inputs* from the preview — `MassageSecrets` masks
  every secret property in `pulumi preview --json` output — and is resolved by
  `resolveSecretInputs` from stack config.
- **The engine's unknown sentinel** (`04da6b54-…`) arrives in the *inputs* when an
  injected resource references **another injected resource**: at the preview that
  drives injection the dependency is not in state yet, so its outputs are unknown.
  `resolveUnknownInputs` fills the value from the resource's own
  Terraform-derived outputs — Terraform already created both resources, so the
  real value is in the sidecar. This is the mirror of `fillOutputsFromInputs`.

`checkNoPlaceholders` then walks both bags recursively and hard-errors on any
survivor, reporting the property path. Nothing may be written to state that a
later operation could not distinguish from a real value.

`resolveSecretInputs` distinguishes a **trusted** Terraform name from a
**guessed** one. With no loaded provider schema, names go through
`PulumiToTerraformName`, which cannot invert the bridge's pluralisation (513
attributes in pulumi-aws v7.24.0 do not round-trip). An attribute *found* under
the guessed name corroborates it, so a present-but-null value is a genuine
"Terraform has no value here" and the input is dropped; an *absent* one is
conclusive only when the name came from the schema, and is otherwise an error.
Dropping on a guess silently deletes a program-declared input.

#### The raw state delta

`attachRawStateDelta` writes `__meta` (the schema version, independently of
whether a delta exists) and then the delta itself, subject to three gates:

1. a delta embedding `(sensitive)` is **dropped** — substituting the real secret
   into outputs would not change what a Replace node reconstructs;
2. `validateRecover` must succeed, or the delta is dropped and the reason
   recorded;
3. surviving Replace nodes are wrapped in the Pulumi secret envelope by
   `envelopeReplaceNodes`, matching what the engine does for the deltas the
   bridge writes itself, so the same payload is not encrypted one way and
   plaintext the other. Applied *after* validation, because `validateRecover`
   builds its `PropertyValue` with `deltaPropertyValue`, which deliberately does
   not interpret sentinel maps.

Each outcome is counted separately and reported as
`Deltas attached (injected): X of Y` plus a named reason per dropped resource.
This matters because a missing delta does **not** fail: the resource degrades to
the bridge's legacy state conversion and still previews as `same`. Without the
positive count, a regression to zero deltas is invisible.

#### Ordering and verification

`orderInjected` sorts injected resources so a dependency precedes its dependent —
`VerifyIntegrity` rejects a resource whose parent or dependency appears later in
the array. A cycle terminates without dropping anything and leaves a forward
reference for verification to catch.

`InjectNonImportable` verifies the deployment it produces before returning, and
does so **even when the sidecar is empty**. That path returns a non-nil (empty)
result, so the command's own fallback check — guarded on a nil result — would
otherwise be skipped too, leaving the only path through `patch-state` that
verified nothing at all.

## The three bridge reserved keys

Defined in `pulumi-terraform-bridge/v3@v3.121.0/pkg/reservedkeys/keys.go`:
`Meta = "__meta"` (`:19`), `Defaults = "__defaults"` (`:26`),
`RawStateDelta = "__pulumi_raw_state_delta"` (`:30`).

**This repo never imports `reservedkeys`.** Every occurrence is a string
literal or a locally redeclared constant: [pkg/state_patcher.go:698](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L698), [:700](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L700),
[:707](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L707), [:1422](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L1422), [:1466](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L1466), [:1626](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L1626), and `rawStateDeltaKey` / `metaKey` /
`reservedDefaultsKey` in [pkg/state_injector.go:54](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_injector.go#L54), [:55](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_injector.go#L55), [:451](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_injector.go#L451). The
injector's comment ([:49-52](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_injector.go#L49-L52)) says explicitly that it duplicates the constants
to match the rest of the package. All three keys are now written by the tool,
so the duplication has more surface than it did.

| Key | Written by | Read by this tool | Mutated by this tool |
|---|---|---|---|
| `__pulumi_raw_state_delta` | The bridge, during `pulumi import`. Computed by `ComputeInjectionState` ([pkg/raw_state_delta.go:153](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/raw_state_delta.go#L153)) into the digest and sidecar, and written into outputs by `attachRawStateDelta` ([pkg/state_injector.go:551](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_injector.go#L551)). | `validateRecover` ([pkg/state_patcher.go:1626](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L1626)), `attachRawStateDelta` ([pkg/state_injector.go:544](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_injector.go#L544)), `conformToDelta` ([pkg/state_patcher.go:1422](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L1422), tests only) | `injectAssetDeltas` ([:1207](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L1207)) adds asset entries; `attachRawStateDelta` deletes the key when `Recover` fails ([pkg/state_injector.go:555](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_injector.go#L555)). `updateDeltaForPatchedOutputs` ([:1248](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L1248)) would rebuild array deltas but is uncalled. Explicitly *not* copied from a preview create step ([pkg/state_injector.go:274-276](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_injector.go#L274-L276)). |
| `__meta` | The bridge (schema version + private state). Now also by `attachRawStateDelta` ([pkg/state_injector.go:525](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_injector.go#L525)), from the sidecar's `SchemaVersion`. | Nothing reads it back. | `metaPayload` ([:624](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_injector.go#L624)) builds the bridge's own `{"schema_version":"N"}` string and omits it entirely for version 0 ([:632-634](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_injector.go#L632-L634)), mirroring `tfbridge.MakeTerraformResult`. |
| `__defaults` | The bridge, on inputs; and by the injector, but **only when absent** ([pkg/state_injector.go:347-349](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_injector.go#L347-L349)). | The same presence check. | Never overwritten: the engine's `Check` usually supplies a populated list, and replacing it with `[]` would discard what `Check` worked out ([:344-346](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_injector.go#L344-L346)). |

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
remains of the issue is the `DeltaAbsentFromSidecar` / `DeltaDroppedSensitive` /
`DeltaDroppedUnrecoverable` residue: resources whose delta could not
be computed, embedded `(sensitive)`, or failed `Recover`.

---

## Schema forms and their consumers

Schema enters in **four** distinguishable forms.

### 1. The Terraform protocol schema (live provider)

`providers.GetProviderSchema(ctx)` over go-plugin, from
`tfprovider.LoadProvider` ([pkg/tfprovider/loader.go:65](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/tfprovider/loader.go#L65)). Note the package: the
bridge's *vendored* OpenTofu
(`pulumi-terraform-bridge/v3/pkg/vendored/opentofu/providers`), whose method
takes a `context.Context` — not `github.com/pulumi/opentofu/providers`, which
this repo also depends on for state parsing.

Carries: `Block` (→ `ImpliedType()`, a `cty.Type`), `Version`, per-attribute
`Sensitive`, `Required`/`Optional`/`Computed`.

Consumers:
- `ComputeInjectionState` — `sch.Block.ImpliedType()` and `sch.Version`
  ([pkg/raw_state_delta.go:70](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/raw_state_delta.go#L70), [:202](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/raw_state_delta.go#L202)). Nothing else can supply these.
- `BuildSensitivityMap` — `attr.Sensitive` ([pkg/provider_schema.go:95](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/provider_schema.go#L95),
  [:166](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/provider_schema.go#L166)). Unused outside tests.

Cost: downloads and runs the real provider binary. Requires a locked version
from `.terraform.lock.hcl` ([pkg/importsupport/prober.go:176](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/importsupport/prober.go#L176)).

### 2. Provider *behaviour*, probed rather than read

Importability is not in any schema. `Prober.Check`
([pkg/importsupport/prober.go:98](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/importsupport/prober.go#L98)) calls `ImportResourceState` with a dummy ID
([:120](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/importsupport/prober.go#L120)) and classifies the error ([:126](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/importsupport/prober.go#L126)). Memoized per provider+type
([:104](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/importsupport/prober.go#L104)). This is a schema consumer only in the sense that it needs the same
running provider.

Fallback when no provider can be loaded: the curated
`pkg/importsupport/fallback.json` ([:199](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/importsupport/prober.go#L199)), which answers `Unsupported` for
types it lists and `Unknown` for everything else.

### 3. The bridge mapping mock (`GetMapping("terraform")`)

`PulumiProvidersForTerraformProviders` ([pkg/pulumi_providers.go:75](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/pulumi_providers.go#L75)) installs
and runs the **Pulumi** provider binary, calls `GetMapping`
([pkg/bridgedproviders/mapping.go:93](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/bridgedproviders/mapping.go#L93)), unmarshals a
`info.MarshallableProvider` ([:125](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/bridgedproviders/mapping.go#L125)) and calls `.Unmarshal()` ([:129](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/bridgedproviders/mapping.go#L129)). Results
are cached to `~/.pulumi/mapping-cache` ([pkg/pulumi_providers.go:174](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/pulumi_providers.go#L174)).

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
sets `HasDefault` from `schema.Default()` ([pkg/schema_fields.go:97](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/schema_fields.go#L97)), so
**`HasDefault` is always false in production**, and `PatchStateFromSchema`'s
default-fallback branch ([pkg/state_patcher.go:602](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L602), fed at [:1894-1895](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L1894-L1895)) can
never fire against a real provider. It fires in tests, which construct
`schema.Schema{Default: …}` directly. This is very likely why the curated
`data/aws-import-diff-fields.json` exists and why `PatchStateFromSchema` was
never wired to a command.

Consumers of the mock:
- `bridge.PulumiTypeToken` ([pkg/bridge/pulumi_type_token.go:28](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/bridge/pulumi_type_token.go#L28)) — Pulumi type
  token for a TF type, used to build URNs ([pkg/module_map.go:609](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L609)).
- `GetSchemaFieldInfo` ([pkg/schema_fields.go:72](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/schema_fields.go#L72)) — TF→Pulumi names via
  `tfbridge.TerraformToPulumiNameV2` ([:93](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/schema_fields.go#L93)), input/computed classification
  ([:103-104](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/schema_fields.go#L103-L104)), asset overlay ([:107-113](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/schema_fields.go#L107-L113)).
- `ComputeInjectionState` — passes `schemaMap` and `schemaInfos` straight into
  `MakeTerraformOutputs` and `RawStateComputeDelta`
  ([pkg/module_map.go:516-525](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L516-L525) → [pkg/raw_state_delta.go:153](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/raw_state_delta.go#L153), [:228](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/raw_state_delta.go#L228)).

Note `PulumiTypeToken` calls `camelPascalPulumiName`, which contains a
`contract.Assertf` on the resource-type prefix
([pkg/bridge/pulumi_type_token.go:42](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/bridge/pulumi_type_token.go#L42)). That is a **panic**, not an error, and
`buildResourceURN` ([pkg/module_map.go:609](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L609)) only handles the error return. A
provider whose resources do not all share its `GetResourcePrefix()` would abort
the digest.

### 4. Curated data files

`data/aws-import-diff-fields.json`, loaded by `LoadFieldsFile`
([pkg/state_patcher.go:1952](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L1952)), and `pkg/importsupport/fallback.json`. These
encode facts about provider *Go code* — `Default`, `Read` behaviour, `Importer`
— that no schema exposes. `docs/aws-import-diff-fields.md` explains the
categories; [docs/non-importable-resources.md:229-241](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/docs/non-importable-resources.md#L229-L241) explains why `Read`
semantics cannot be probed.

### Property naming: five mechanisms that can disagree

| Mechanism | Where | Basis |
|---|---|---|
| `tfbridge.TerraformToPulumiNameV2(tfName, schemaMap, fieldInfos)` | [pkg/schema_fields.go:93](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/schema_fields.go#L93) | Schema-aware; handles pluralization from `MaxItems`, and `info.Schema.Name` overrides |
| `snakeToCamel` | [pkg/state_patcher.go:1661](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L1661), applied recursively by `camelCaseKeys` ([:1674](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L1674)) | Pure string transform, no schema |
| `tfToPulumiField` / `pulumiToTFField` | [pkg/state_patcher.go:121](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L121), [:147](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L147) | A 22-entry hand-written table |
| `tfbridge.MakeTerraformOutputs` | via [pkg/raw_state_delta.go:228](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/raw_state_delta.go#L228) | The bridge's own conversion, schema-driven at every nested level |
| `tfbridge.PulumiToTerraformName` | [pkg/state_injector.go:742](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_injector.go#L742) | The bridge's generic reverse transform, used only when no schema describes the field |

`MapTFAttributesToPulumi` ([pkg/non_importable_file.go:54](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/non_importable_file.go#L54)) uses the first when
a field is in the schema and falls back to the second when it is not ([:60-63](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/non_importable_file.go#L60-L63)),
which is the only place the two are reconciled explicitly. It is now live, as
the injector's fallback when a sidecar predates S2b
([pkg/state_injector.go:317](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_injector.go#L317)).

The injector needs the mapping in **both** directions at once and gets each
from a different mechanism: `resolveOutputSecrets` goes TF→Pulumi through
`SchemaFieldInfo.PulumiName` with a `snakeToCamel` fallback
([pkg/state_injector.go:667-670](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_injector.go#L667-L670)), while `resolveSecretInputs` goes
Pulumi→TF through `PulumiToTFNames` with a `tfbridge.PulumiToTerraformName`
fallback ([:736-743](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_injector.go#L736-L743)). The two fallbacks are not inverses of each other, which
is precisely why `checkNoPlaceholders` ([:412](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_injector.go#L412)) exists as a name-independent
backstop.

They disagree in predictable cases:

- **Pluralization.** `TerraformToPulumiNameV2` turns a `MaxItems != 1` list
  attribute `ingress_rule` into `ingressRules`; `snakeToCamel` yields
  `ingressRule`. `patchResourceFields` uses `camelCaseKeys` for *nested* digest
  values ([pkg/state_patcher.go:514](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L514)), so nested list-of-object fields patched
  from the digest get unpluralized names inside a correctly-named top-level
  property.
- **Name overrides.** `info.Schema.Name` (an explicit rename in the provider's
  bridge metadata) is honoured only by the first mechanism. The table at [:121](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L121)
  encodes one such override by hand — `"filename": "code"` ([:126](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L126)) and
  `"parameter": "parameters"` ([:134](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L134)).
- **`PatchState` vs `PatchStateFromSchema`.** The former derives the TF name
  from the Pulumi name through the hand table ([:852](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L852)); the latter takes both
  from the schema ([:1892-1893](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L1892-L1893)). A field present in the fields file but absent
  from the table gets `TFName: ""` and is therefore never matched to a digest
  value at all ([:510](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L510)) — it can only ever be patched from its default.

---

## Representations and conversions

| # | Representation | Produced by | Converted to | By |
|---|---|---|---|---|
| 1 | `.tfstate` bytes | Terraform | `*states.State` | `statefile.Read`, [pkg/tofu_eval.go:147](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/tofu_eval.go#L147) |
| 1a | `tofu show -json` bytes | `tofu` | `tfjson.State` → `*states.State` | [pkg/generate_module_map.go:128](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/generate_module_map.go#L128), [:262](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/generate_module_map.go#L262) |
| 2 | `AttrsJSON` (`[]byte`, exact) | Terraform | `map[string]interface{}` with `json.Number` | `decodeAttrs`, [pkg/module_map.go:459](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L459) |
| 3 | `[]cty.PathValueMarks` | Terraform | `(sensitive)` placeholders | `redactSensitivePaths`, [pkg/module_map.go:1135](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L1135) |
| 3b | same | | stack config secrets | `DiscoverSensitiveSecrets` → `SetSecretsFromState`, [:765](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L765), [:1081](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L1081) |
| 4 | `ModuleResource` | `matchResources`, [:355](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L355) | `tf-digest.json` | `WriteModuleMap`, [:1332](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L1332) |
| 5 | `AttrsJSON` (redacted, re-marshalled) | [pkg/module_map.go:539](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L539) | `cty.Value` | `ctyjson.Unmarshal`, [pkg/raw_state_delta.go:71](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/raw_state_delta.go#L71) |
| 6 | `cty.Value` | | `resource.PropertyMap` | `MakeTerraformOutputs`, [pkg/raw_state_delta.go:228](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/raw_state_delta.go#L228) |
| 7 | `cty.Value` + `PropertyMap` | | `RawStateDelta` | `RawStateComputeDelta`, [:153](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/raw_state_delta.go#L153) |
| 8 | `RawStateDelta` | | `map[string]interface{}` | `json.Marshal` + `decodeAttrs`, [:181-186](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/raw_state_delta.go#L181-L186) |
| 9 | `ModuleResource` | | `ImportEntry.ID` (string only) | `fillState.assign`, [pkg/import_filler.go:293](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/import_filler.go#L293) |
| 10 | `ModuleResource` | | `NonImportableResource` | [pkg/import_filler.go:277](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/import_filler.go#L277) |
| 11 | `ImportFile` | | `[]*optimport.ImportResource` | [pkg/batchimport/file.go:39](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/batchimport/file.go#L39) |
| 12 | — | `pulumi import` | deployment JSON (`apitype.DeploymentV3`) | the engine + bridge |
| 13 | `apitype.UntypedDeployment` | `auto.Stack.Export` | full envelope bytes | `StackSession.Export`, [pkg/state_stack.go:58](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_stack.go#L58) |
| 14 | deployment JSON | `pulumi stack export` | `map[string]interface{}` with `json.Number` | [pkg/state_patcher.go:743](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L743) |
| 15 | `map[string]interface{}` | `patchResourceFields`, [:495](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L495) | patched deployment JSON | `json.MarshalIndent`, [:893](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L893) |
| 16 | `pulumi preview --json` | `StackSession.PreviewJSON`, [pkg/state_stack.go:88](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_stack.go#L88) | `PreviewDigest` with `json.Number` | `ParsePreviewJSON`, [pkg/preview.go:62](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/preview.go#L62) |
| 17 | `PreviewDigest` | | `*PreviewCreates` (lookup via `PreviewCreates.Lookup`) | `CreatesByTypeName`, [pkg/preview.go:111](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/preview.go#L111) |
| 18 | `NonImportableResource` + `newState` | | injected resource object | `buildInjectedResource`, [pkg/state_injector.go:262](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_injector.go#L262) |
| 19 | injected deployment JSON | `InjectNonImportable`, [:228](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_injector.go#L228) | `deploy.Snapshot` | `stack.DeserializeDeploymentV3`, [pkg/state_verify.go:58](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_verify.go#L58) |
| 20 | deployment bytes | | `apitype.UntypedDeployment` → stack | `StackSession.Import`, [pkg/state_stack.go:71](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_stack.go#L71) |

`shim.InstanceState` appears nowhere in this table. The tool never constructs
one and cannot: the only `shim.Resource` it holds is the mock, whose
`InstanceState` is a hard error (`tfshim/schema/resource.go:55`). This is the
reason `RawStateComputeDelta` (which needs only a type and a value) is used
instead of `RawStateInjectDelta` (`rawstate.go:458`, which needs an instance
state).

---

## Weaknesses and open questions

Ordered roughly by how much damage each can do.

### 1. Sensitivity is walked twice, independently

- `redactSensitivePaths` ([pkg/module_map.go:1135](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L1135)) delegates to `redactAtPath`
  ([:1162](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L1162)), which walks a sensitive path to any depth.
- The `tofu show -json` path gets its paths from the format's own
  `sensitive_values` document ([pkg/generate_module_map.go:316](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/generate_module_map.go#L316)).
- Both of the above walk `AttrSensitivePaths` independently — one to redact,
  one to discover config keys — and must agree about which attributes are
  sensitive and what key each maps to. That duplication is
  [#28](https://github.com/pulumi-proserv/pulumi-tool-import/issues/28). A third, schema-driven implementation
  (`BuildSensitivityMap`) existed with no callers at all and has been deleted:
  it read as a working alternative and was not one.
- The digest↔config link is the pure function `flattenAddress`
  ([pkg/module_map.go:932](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L932)), recomputed at four sites
  ([:855](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L855), [pkg/import_filler.go:114](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/import_filler.go#L114), [pkg/state_patcher.go:554](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L554), and
  indirectly via `RedactedAttributes` in [pkg/state_injector.go:665](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_injector.go#L665)), and only
  the first could apply a dedup suffix ([:865](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L865)) — which is exactly why a
  collision is a hard error instead: a suffixed key would be unresolvable at the
  other three sites. `DiscoverSensitiveSecrets` returns an error
  ([:897-906](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/module_map.go#L897-L906)) and `GenerateModuleMap` propagates it
  ([pkg/generate_module_map.go:205-207](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/generate_module_map.go#L205-L207)), so `digest tf` fails. See S2.
- The **second placeholder is now live**, not just designed: `[secret]`, from
  `MassageSecrets` on the preview path, arrives in injected *inputs* while
  `(sensitive)` — or its path-tagged nested form `(sensitive:<path>)` —
  arrives in injected *outputs*, and each is resolved by a
  different function using a different name mapping
  ([pkg/state_injector.go:647](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_injector.go#L647), [:716](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_injector.go#L716)). `checkNoPlaceholders` ([:412](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_injector.go#L412)) is the
  name-independent backstop that makes the pair safe, and its existence is a
  fair measure of how hard the naming is to get right.

Recording `RedactedAttributes` in the digest itself (rather than recomputing it
in `resolve tf` from a string match against `"(sensitive)"`,
[pkg/import_filler.go:110](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/import_filler.go#L110)) would make the link explicit and dedup-safe. See
[#28](https://github.com/pulumi-proserv/pulumi-tool-import/issues/28).

### 2. `patch-state` can only repair what the digest kept, and only by name ([#37](https://github.com/pulumi-proserv/pulumi-tool-import/issues/37))

`PatchState` matches state resources to digest resources by Pulumi resource
*name* ([pkg/state_patcher.go:819](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L819), built by `BuildDigestNameMap` at [:307](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L307)),
with a chain of fallbacks ending in "exactly one unused candidate of this type"
([:412](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L412), [:461](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L461)). A mismatch is reported only as an aggregate `NoMatch` count
([:882](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L882)) — there is no per-resource report of what went unmatched, and no way to
assert that a resource the operator cares about was matched.

The same fallback exists in `FillImportFile` ([pkg/import_filler.go:377](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/import_filler.go#L377)) with
a warning, and in `matchChildren`'s normalized-name pass
([pkg/state_patcher.go:389-400](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L389-L400)). Three near-identical matchers with slightly
different rules is a simplification opportunity: `FillImportFile` and
`BuildDigestNameMap` are the same algorithm over different input shapes.

Injection deliberately went the other way. `InjectNonImportable` matches on
Pulumi type plus name against preview create steps and **fails** on a duplicate
or a miss ([pkg/state_injector.go:178-195](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_injector.go#L178-L195)) rather than falling back to a
"single candidate" guess. It is the same matching problem with the opposite
policy, and the stricter policy is the one that produces an actionable error.

### 3. Dead and half-wired code obscures which path is real ([#52](https://github.com/pulumi-proserv/pulumi-tool-import/issues/52))

- `conformToDelta` ([:1421](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L1421)): tests only.
- `PatchStateFromSchema` ([:1785](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L1785)): tests only, and its default path is
  unreachable in production (the bridge mapping mock carries no `Default` —
  see "The bridge mapping mock" under Schema forms).

`PatchStateFromSchema` in particular still reads like a supported alternative
to the curated fields file when it cannot currently work.

### 4. Verification is structural — nothing checks a value against the cloud

`validateRecover`
([pkg/state_patcher.go:1625](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher.go#L1625)) checks delta↔outputs consistency;
`VerifyDeploymentIntegrity` ([pkg/state_verify.go:37](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_verify.go#L37)) checks snapshot
structure. Neither checks a value against the cloud, and `refresh` cannot
either for the types that matter — it reports these resources unchanged even
when their values are wrong.

Stack mode's gate runs `pulumi preview --json` before and after
the mutation and reverts on regression ([pkg/state_stack.go:195](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_stack.go#L195),
[cmd/patch_state_tf.go:341-367](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/cmd/patch_state_tf.go#L341-L367)). Three things about that gate are worth being
precise about, because each is easy to misread:

- **It is a comparison, not a clean bill.** A stack mid-migration legitimately
  has diffs. Requiring an absolutely clean preview would revert nearly every
  legitimate patch-only pass, so the bar is "no regression versus the baseline"
  plus "every injected URN reports `same`" ([pkg/state_stack.go:185-192](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_stack.go#L185-L192)).
- **The baseline is the pre-mutation preview**, reused from the injection
  skeleton when there is one ([cmd/patch_state_tf.go:268](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/cmd/patch_state_tf.go#L268)). It is therefore
  taken against unpatched state, which is what makes the comparison meaningful.
- **It covers stack mode only.** File mode writes `--out` and prints the
  verification instructions ([cmd/patch_state_tf.go:438-441](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/cmd/patch_state_tf.go#L438-L441)); nothing enforces
  that the operator follows them. The air-gapped path is still unverified by
  construction.

The residual gap: `CheckInjectionVerification` compares *counts* of non-`same`
steps outside the injected set ([pkg/state_stack.go:260-264](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_stack.go#L260-L264)) as well as
per-URN regressions. A run that fixes one neighbouring resource and breaks
another nets to zero on the count, but the per-URN `newlyDirty` check
([:222-231](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_stack.go#L222-L231)) catches it — so the count is a backstop for churn the per-URN pass
cannot see (resources absent from one preview entirely), not the primary gate.

### 5. Related, already tracked

- [#22](https://github.com/pulumi-proserv/pulumi-tool-import/issues/22) —
  non-importable state injection; S2b, S3b and S7 above. Implemented on this
  branch.
- [#24](https://github.com/pulumi-proserv/pulumi-tool-import/issues/24) —
  splitting large workspaces into shard stacks. Relevant here because it needs
  the same export/verify/import helpers `pkg/state_stack.go` now provides, and
  because sharding multiplies the digest-provenance concern (#38).
- [#25](https://github.com/pulumi-proserv/pulumi-tool-import/issues/25) — the
  raw state delta gap. Closed for the common case by S2b; the residue is the
  `DeltaAbsentFromSidecar` / `DeltaDroppedSensitive` /
  `DeltaDroppedUnrecoverable` counts.
- [#28](https://github.com/pulumi-proserv/pulumi-tool-import/issues/28) —
  sensitive values: the three implementations, the two uncovered paths, and the
  recomputed config-key link; §1.

### Not traced

- What the engine and bridge write into `__meta` and `__defaults` during
  `pulumi import`, in detail. The tool now writes both on the injection path,
  but modelled on the bridge rather than read from its write sites:
  `metaPayload` ([pkg/state_injector.go:624](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_injector.go#L624)) mirrors what
  `tfbridge.MakeTerraformResult` produces, and the `__defaults` handling assumes
  the engine's `Check` populates the list. Neither assumption was verified
  against the bridge source; both are asserted only by unit test.
- Whether `__meta` carries anything besides `schema_version` for the resource
  types injection targets. `metaPayload` writes only that key, which is correct
  for a resource with no provider private state — but "these types have none"
  was not established.
- The CFN half of the pipeline (`pkg/state_patcher_cfn.go`, `pkg/cfn`). It has
  its own state representation and its own `UseNumber` discipline
  ([pkg/state_patcher_cfn.go:42](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher_cfn.go#L42), [:102](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/state_patcher_cfn.go#L102)), and shares only
  `patchResourceFields` with the TF path.
- Whether `loadStateWithRewrite`'s textual
  `registry.terraform.io/` → `registry.opentofu.org/` substitution
  ([pkg/tofu/loader.go:285](https://github.com/pulumi-proserv/pulumi-tool-import/blob/0c081c8e253a0932da742e1ec7d94c82606cf0ca/pkg/tofu/loader.go#L285)) can corrupt an attribute value in practice.
- Whether a `shim.SchemaMap` reconstructed from the live Terraform protocol
  schema would drive `MakeTerraformOutputs` and `RawStateComputeDelta`
  correctly — the key question for collapsing the two loaders.
