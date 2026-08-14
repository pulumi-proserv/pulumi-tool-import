# Writing non-importable resources into state

Design for [#22](https://github.com/pulumi-proserv/pulumi-tool-import/issues/22). Date: 2026-08-13.

## Problem

`resolve tf` detects resource types whose Terraform provider declares no importer, leaves
them out of the import file, and records them in a sidecar beside `--out`:

```json
{
    "resources": [
        {
            "type": "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation",
            "name": "prop0",
            "terraformAddress": "aws_vpn_gateway_route_propagation.prop[0]",
            "id": "vgw-0cdee3deb918b1983_rtb-0e370d1fdde0890b3",
            "attributes": { "route_table_id": "rtb-…", "vpn_gateway_id": "vgw-…" },
            "redactedAttributes": { "shared_key": "route_shared_key" }
        }
    ]
}
```

Detection shipped in v0.2.0. Nothing consumes the sidecar. The operator hand-crafts state
objects from it, which is how the v0.2.0 end-to-end run was completed — a throwaway script,
run once, against real AWS.

If the resources are simply left out, the next `pulumi up` tries to **create** infrastructure
that already exists. For association and toggle resources, whose `Create` does not tolerate a
pre-existing object, that fails or duplicates.

## Goal

`patch-state tf` closes the loop: it writes the missing resources into the deployment, proves
the result with a preview, and leaves the stack either correct or untouched.

## Scope

In scope: injection of sidecar resources into an exported deployment; computing Pulumi outputs,
`__pulumi_raw_state_delta` and the true schema version during `digest tf`, where a provider is
already running, and carrying them through the sidecar; stack mode that wraps
export/import/preview so no hand-run `pulumi` steps remain; structural verification via the
engine's own integrity check; preview-based value verification with revert on disagreement.

Out of scope: dependency edges in the digest — those come from the preview; CFN support — this
is the `tf` subcommand only. `patch-state` must not gain a provider dependency.

## Findings that shaped the design

These were established by reading the vendored engine and bridge source and by a live run
against AWS on 2026-08-12. They are recorded because each one closes off an approach that
looks reasonable from the outside.

### `pulumi import`'s validation cannot cover injection

`ImportStep.Apply` (`pulumi/pkg/v3@v3.222.0/resource/deploy/step.go:1756`) calls
`prov.Read(ImportID)` and writes *the provider's* inputs and outputs into state. Values
supplied by the caller are discarded, never validated. `prov.Check` runs afterwards, but its
failures are printed as warnings and the import still succeeds. So there is no existing
value-level validation to inherit.

### `pulumi stack import` validates structure, and covers it well

`Snapshot.VerifyIntegrity` (`.../resource/deploy/snapshot.go:586`) rejects:

- a resource missing `urn` or `type`;
- `custom: false` combined with a non-empty `id`;
- a `provider` reference that fails to parse, **or that names a provider absent from the
  snapshot**;
- a parent, dependency, property dependency, or `deletedWith` target that is missing, or that
  appears *after* the resource in the array.

That is precisely the failure set observed in #11 — including issue 3, the malformed provider
reference. Because `github.com/pulumi/pulumi/pkg/v3` is already a direct dependency of this
tool, the same check can run in-process, offline, before the file is written.

### Refresh does not verify injected values; preview does

Verified live against AWS: with a deliberately wrong `routeTableId` on a
`VpnGatewayRoutePropagation`, `refresh --preview-only` reported `14 unchanged` while `preview`
reported `+-1 to replace`. These types' `Read` either sets no attributes or re-derives them
from the resource ID, so refresh only proves the ID resolves. **Zero operations on preview is
the acceptance criterion. Refresh is never acceptable as one.**

### The raw state delta: what it is for, and where it gets computed

The delta encodes the difference between a Pulumi `PropertyMap` and the provider's raw
Terraform state, so the latter can be reconstructed without storing it twice. Its purpose,
from its own doc comment (`pkg/tfbridge/rawstate.go:39-56`), is **provider version upgrades**:
"the transformation to PropertyMap is schema aware and may rename fields or even change their
type, and the V-prev schema is not available at read time."

Three facts govern the design.

**Delta and property map are a matched pair.** When the bridge writes a delta it runs
`turnaroundCheck` (`rawstate.go:522`), which calls `Recover` immediately and requires the
result to match the original raw state byte for byte, refusing to write otherwise. This is why
`patch-state` must edit the delta when it edits a value (`updateDeltaForPatchedOutputs`,
`pkg/state_patcher.go:1205`): leaving it stale makes `Recover` either fail with "does not apply
cleanly to the resource state" or, in the `Replace` case which stores raw JSON verbatim,
silently rebuild state containing the old value.

**An absent delta is not an empty delta.** `rawStateRecoverNatural` — the empty-delta case —
errors on any object value ("cannot process Object values due to map vs object confusion",
`rawstate.go:261`), so any resource with a nested block or map necessarily carries a non-empty
delta. But when the key is *absent*, `makeTerraformStateViaUpgradeEnabled` (`schema.go:1365`)
returns false and that code is never reached: the bridge uses `makeTerraformStateWithOpts`,
which is schema-aware and handles objects by construction. That is the pre-delta mechanism,
still the path for all state written by older bridges. Injecting without a delta is therefore
viable, and it is the graceful fallback here — but it reconstructs through the *current*
schema, so it cannot serve a future provider upgrade, and it cannot round-trip anything the
schema does not describe.

**The obvious way to compute one does not work here.** `RawStateInjectDelta` mirrors the
provider's own create path (`provider.go:1360-1375`), but that needs an `InstanceState`, which
requires the Terraform provider's Go code linked into the binary. This tool has no such thing:
it launches the *Pulumi* provider binary over gRPC only to call `GetMapping`
(`pkg/bridgedproviders/mapping.go:84`) and reconstructs a `tfshim/schema` mock from the
marshalled `ProviderInfo`. Probed directly against the AWS provider as this tool loads it:

```
aws_vpn_gateway_route_propagation  InstanceState → "mock schema does not support instance
                                                    states";  SchemaType → nil
```

`ResourceShim.InstanceState` is a hard error (`pkg/tfshim/schema/resource.go:56`) and
`SchemaType` returns an unpopulated field. The same mock reports `SchemaVersion=0` for every
type, including `aws_instance` whose upstream version is non-zero — so it cannot be trusted for
`__meta` either.

**`RawStateComputeDelta` needs no instance state**, and that is the route this design takes. It
takes `valueshim.Type` and `valueshim.Value` (`rawstate.go:485`), and `valueshim` exports
`FromCtyType`/`FromCtyValue` over the zclconf cty that OpenTofu uses. From a live provider,
`providers.Schema.Block.ImpliedType()` gives the type, `ctyjson.Unmarshal` turns the digest's
`AttrsJSON` into the value, and `providers.Schema.Version` gives the true `SchemaVersion`.

**The provider is live during `digest tf`, so that is where this happens.**
`pkg/generate_module_map.go:178` starts real provider processes for the import-support probe —
the very check that flags a resource as non-importable. So every resource that reaches the
sidecar came from a run holding a live provider for its provider address: the delta is available
exactly when it is needed, and never needed when it is not. Computing it there keeps
`patch-state` free of provider downloads, launches, and network access.

### Existing Recover validation does not apply

`validateRecover` (`pkg/state_patcher.go:1448`) returns early when
`__pulumi_raw_state_delta` is absent, and is only reached from `patchResourceFields`, which
operates on resources already present. It is not a safety net for injection. Even when it does
fire it is a structural round-trip check, not a check against the cloud.

## Design

### Command

No new verb. Patching and injecting are two halves of one operation — filling in state that
import could not — performed ahead of the same preview. `patch-state tf` gains:

```
--non-importable <sidecar>   inject the resources recorded in this file (optional)
--project-dir / --stack      promote to stack mode (already present for config secrets)
```

Within a run: **patch first, then inject.** The two touch disjoint resources, but the
*verifying* preview must see a fully patched deployment, or diffs that patching would have
removed get misattributed to the injected resources.

Note the two previews are different runs with different jobs. The **skeleton** preview happens
first, against the stack's current, unpatched state; the non-importable resources appear as
creates there regardless of patching, because they are absent from state entirely. The
**verifying** preview happens last, after both halves have been applied and imported.

### Two modes

**File mode** — today's behaviour, unchanged in shape. Reads `--state`, writes `--out`, runs
`VerifyIntegrity` before writing. The operator runs `pulumi stack import` and `pulumi preview`.
This is the air-gapped path and the reviewable path.

Because the resource skeleton comes from a preview (see below), file mode cannot inject on its
own. It accepts `--preview-json <file>`, the output of `pulumi preview --json`, as the skeleton
source. Combining `--non-importable` with file mode and no `--preview-json` is an error, with a
message naming the command that produces it. Fully air-gapped injection is not possible: the
program metadata does not exist anywhere else, and guessing it is what this design rejects.

**Stack mode** — `--project-dir` and `--stack` given, no `--state`/`--out`. The command wraps
the CLI so the pipeline is entirely tool commands:

```
auto.Stack.Export()
  → write backup to disk, print its path        ← before anything else touches the stack
  → preview --json                              ← skeleton source, see below
  → patch + inject
  → VerifyIntegrity()                           ← in-process, offline
  → auto.Stack.Import(injected)
  → preview --json                              ← verification
  → any injected URN not "same" → Import(backup), report resource and operation
```

The backup is written and its path printed **before** the first mutation, so a killed process
leaves a documented one-line recovery rather than a reconstruction job.

Stack mode requires a runnable program, its dependencies, and cloud credentials. That is
normal inside the migration loop and impossible air-gapped, which is why file mode remains.

### The program is the source of truth for resource metadata

The Pulumi program has already been written from the Terraform source, and it *declares* these
resources — they simply cannot be imported. So a preview run before injection reports them as
**creates**, and the engine's view of each create is authoritative for everything the sidecar
cannot carry.

**The source is `pulumi preview --json`, not the engine event stream.** Its output is a
`previewDigest` (`pulumi/pkg/v3@v3.222.0/display/json.go`) whose `steps[].newState` is a full
`apitype.ResourceV3`, built by `stateForJSONOutput` (`backend/display/json.go:74`) from the
engine's real `*resource.State`. It carries `parent`, `provider`, `protect`, `inputs`,
`dependencies` and `propertyDependencies` as exact URNs, `retainOnDelete`, `customTimeouts`,
`aliases`, and `additionalSecretOutputs`.

The alternative — `optpreview.EventStreams`, which yields `StepEventStateMetadata`
(`sdk/v3/go/common/apitype/events.go:219`) — is strictly weaker: no dependency edges, no
property dependencies. It is not used.

Injection therefore does not assemble a state object field by field. It takes `newState`
wholesale and fills in what only the sidecar knows.

**Obtaining it.** `auto.Stack.Preview()` shells out to `pulumi preview --event-log <file>` and
never passes `--json`, and `optpreview` exposes no JSON option. The Automation API's own command
runner provides it without dropping to `os/exec`:

```go
stdout, _, _, err := ws.PulumiCommand().Run(ctx, projectDir, nil, nil, nil, nil,
    "preview", "--json", "--stack", stackName)
```

That reuses the CLI binary, working directory, and environment the Automation API already
resolved. Export and import continue to use `auto.Stack`. Both previews — skeleton and
verifying — go through this one path, which also yields `previewDigest.ChangeSummary` directly.

Procedure:

1. Run `preview --json`; collect every step whose `op` is `create`.
2. Match each sidecar entry to exactly one create, on Pulumi type plus resource name.
3. **On no match, or more than one match, fail** — printing the unmatched sidecar entries and
   the candidate creates. There is no fallback heuristic.

This replaces every field that would otherwise be guessed. In particular the provider
reference — the uuid in `urn:…::pulumi:providers:aws::default_7_24_0::<uuid>`, which exists
only in the target stack — is read from the engine rather than inferred by scanning the
deployment for a provider of the right package. Stacks with several provider instances
(multiple regions or accounts) resolve correctly with no ambiguity to break.

**Secrets in `newState.Inputs` are masked and must never be injected verbatim.**
`stateForJSONOutput` calls `MassageSecrets`, which replaces every secret property with the
literal string `[secret]`. That is the same hazard as the digest's `(sensitive)` placeholder,
arriving by a second route. Any input equal to `[secret]` is resolved from stack config via
`redactedAttributes`; if it cannot be resolved, the command fails.

### The injected state object

Per matched sidecar entry, one `custom: true` object appended to `deployment.resources`:

| Field | Source |
|---|---|
| `urn`, `parent`, `provider`, `protect`, `dependencies`, `propertyDependencies`, `retainOnDelete`, `customTimeouts`, `aliases`, `additionalSecretOutputs` | `newState`, verbatim |
| `custom` | `true` |
| `id` | sidecar, verbatim — already the provider's ID format |
| `inputs` | `newState.Inputs`, with `[secret]` values resolved from config, plus `__defaults: []` |
| `outputs` | computed by `digest tf`, carried in the sidecar, secrets resolved at injection |
| `__pulumi_raw_state_delta` | computed by `digest tf`, inside `outputs`; dropped if it fails to recover |
| `__meta` | `{"schema_version": "<providers.Schema.Version>"}`, recorded by `digest tf` |

**Outputs, delta and schema version are produced by `digest tf` and carried in the sidecar.**
In the same loop that flags a resource non-importable, with the provider already open, the digest
records: the Pulumi outputs, the `RawStateComputeDelta` result, and `providers.Schema.Version`.
`resolve tf` copies them into the sidecar alongside the fields it already writes. `patch-state`
consumes them and never loads a provider.

**Secrets stay out of the sidecar, using the mechanism already there.** The digest redacts
sensitive attributes to `(sensitive)` and records, per attribute, the stack config key holding
the real value as a secret (`redactedAttributes`, `pkg/import_filler.go:68-75`). The delta is
computed from the *redacted* attributes, so the sidecar — a file people commit — never holds a
secret. At injection, `patch-state` substitutes the real value from config into the outputs
**and** into any placeholder embedded in the delta's `Replace` blobs, which store raw JSON
verbatim.

Substitution is structurally safe only when the value's type is unchanged, which for
string-valued secrets (pre-shared keys, passwords, tokens) it is. Rather than rely on that rule,
the result is checked: `patch-state` runs `validateRecover` (`pkg/state_patcher.go:1448`) — the
function it already has — on the substituted resource, and **drops the delta for that resource
if `Recover` fails**, falling back to the pre-delta path. The guard is empirical, needs no
provider, and degrades to previously-correct behaviour rather than writing something wrong.

**Property name mapping is reuse, not new code.** `GetSchemaFieldInfo` (`pkg/schema_fields.go:72`)
gives `TFName → PulumiName`, and `LookupProviderForPulumiType` (`:125`) finds the provider for a
Pulumi type token. Both are already used by `PatchStateFromSchema`.

**`__defaults: []`** is a bridge reserved key (`reservedkeys.Defaults`) recording which
properties were populated from schema defaults. Every injected value came from real Terraform
state, so the empty list is the truthful answer, and it stops a later update treating injected
values as stale defaults.

**Sensitive attributes are resolved, never injected verbatim.** The digest replaces sensitive
values with `(sensitive)` and records, per attribute, the stack config key where `digest tf`
stored the real value as a secret. Each `redactedAttributes` entry is resolved from stack
config and written inside Pulumi's secret envelope, reusing the `configSecrets` resolution at
`pkg/state_patcher.go:553`. If a redacted attribute cannot be resolved — no `--project-dir`
and `--stack`, or the key is absent from config — the command **fails**. Injecting the
placeholder would write a known-wrong value into state, which is worse than refusing.

**Dependencies need no translation.** `newState.dependencies` and
`newState.propertyDependencies` are already Pulumi URNs, computed by the engine from the
program's own resource graph. An earlier draft of this design derived them instead from
Terraform's recorded `ResourceInstanceObjectSrc.Dependencies`, which would have required a new
digest field, address-to-URN translation, and an over-approximation for counted resources
(Terraform records config addresses, not instance addresses). `newState` makes all of that
unnecessary, so `digest tf` is unchanged by this work.

**Ordering.** Injected entries are inserted so that each appears after its parent and after any
injected resource it depends on, because `VerifyIntegrity` rejects forward references. With
dependency edges now present, this ordering is a topological sort over the injected set rather
than an append. The output is validated by `VerifyIntegrity` before being written or imported,
in both modes.

### Verification

Value correctness is unprotected by anything the engine or bridge does — findings 1, 3 and 5
each close off a candidate. Preview is the only real check, so stack mode runs it rather than
leaving it as a documented step.

- Injected URNs must all report `same`. Any other operation triggers the revert.
- `previewDigest.ChangeSummary` is compared before and after, so an injection that perturbs a
  neighbouring resource — a route table's `propagatingVgws`, for instance — is also caught.
  Both previews already produce this field, so it costs nothing extra.

### Code layout

`pkg/state_patcher.go` is already ~1800 lines, so injection does not go into it.

- `pkg/state_injector.go` — deployment bytes in, deployment bytes out. Matching, state-object
  construction, ordering, `VerifyIntegrity`. No I/O, no Automation API.
- `pkg/state_stack.go` — export, backup, import, preview, revert. Shared by both halves of
  `patch-state`, and deliberately reusable: [#24](https://github.com/pulumi-proserv/pulumi-tool-import/issues/24)
  (splitting large workspaces into parallel shard stacks) needs the same read/verify/write
  helpers.
- `cmd/patch_state_tf.go` — flag wiring and mode selection only.

## Known gaps

**Injection requires a preview, so it requires a runnable program.** Every field beyond `id`
and `outputs` comes from `newState`, so there is no degraded mode that injects without one.
This is a deliberate trade: the alternative is inferring provider references, parents, and
dependency edges from the deployment, which is the brittleness this design exists to avoid.

**Stack mode requires a runnable program and credentials.** Unavoidable — preview executes the
program. File mode covers the air-gapped case.

**The revert window is small but real.** Between `Import(injected)` and `Import(backup)` the
stack holds unverified state. Mitigated by writing the backup to disk first and printing its
path, not eliminated.

## Testing

Unit tests over fixture deployments in `pkg/testdata`:

- URN, parent, and provider taken from a preview create step, including a component parent;
- sidecar entry matching: exact match, no match, ambiguous match (the last two must fail);
- TF → Pulumi property name mapping, and the input/output split;
- secret resolution from config, and the failure when a redacted attribute cannot be resolved;
- `__defaults: []` present in inputs;
- outputs and delta built through the bridge for a resource with a nested block, asserting the
  delta is present and that `RawStateDelta.Recover` reproduces the original attributes;
- the no-schema fallback: attributes renamed directly, no delta, no error;
- `previewDigest` parsing: create steps collected, non-create steps ignored, `dependencies` and
  `propertyDependencies` carried through verbatim;
- a `[secret]` input resolved from config, and the failure when it cannot be;
- ordering, including a topological case where one injected resource depends on another, with a
  `VerifyIntegrity` pass over the output.

End-to-end, per the AWS CE fixture: the v0.2.0 topology (VPC, three route tables, VPN gateway
with three route propagations, customer gateway, VPN connection with a connection route),
verified by `pulumi preview` reporting zero operations. `pulumi refresh` is not an acceptance
signal.

## References

- Issue [#22](https://github.com/pulumi-proserv/pulumi-tool-import/issues/22), and #11 which it
  was split from
- `docs/non-importable-resources.md` — background on why these types cannot be imported
- Related: [#24](https://github.com/pulumi-proserv/pulumi-tool-import/issues/24)
