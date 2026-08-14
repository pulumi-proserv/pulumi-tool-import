# Non-importable resource types

Some Terraform resource types declare no importer. `pulumi import` cannot bring
them into state at all — and the failure it produces points in the wrong
direction:

```
aws:ec2:VpnGatewayRoutePropagation (…route_prop2):
  error: Preview failed: resource 'vgw-0a1b2c3d4e5f60718_rtb-0a1b2c3d4e5f60003' does not exist
```

The ID is correct and the infrastructure exists. What is missing is the
importer: `aws_vpn_gateway_route_propagation` and `aws_vpn_connection_route`
both define Create/Read/Delete and no `Importer`, so the provider rejects every
ID it is given.

**The obvious remedy is the dangerous one.** Deleting those entries from the
import file does not make the resources go away — it converts them into
`create` operations against infrastructure that already exists. That is only
safe when the resource's Create tolerates a pre-existing object, which for
association and toggle resources it usually does not:
`routeTableEnableVGWRoutePropagation` retries only on `errCodeGatewayNotAttached`
with no "already enabled" path, and `CreateVpnConnectionRoute` surfaces the
error directly. The first `pulumi up` then dies partway through the stack.

## How the tool detects them

Importability is not in any schema. `Importer` is a Go struct field on the
provider's `schema.Resource`; the Terraform gRPC schema RPC returns only the
attribute block and schema version, and the Pulumi bridge mapping the tool
consumes (`GetMapping("terraform")` → `MarshallableProvider`) reconstructs
resources as `&schema.Resource{Schema: …}` with `Importer` unset. Reading
`Importer()` off that shim reports nil for *every* type, importable or not.

The provider will answer the question directly, though. Both Terraform SDKs
check for a missing importer at the top of `ImportResourceState`, before any
provider API call:

- terraform-plugin-sdk v2 → `resource <type> doesn't support import`
- terraform-plugin-framework → `Resource Import Not Implemented`

So `digest tf` loads the Terraform provider (`pkg/tfprovider`) and calls
`ImportResourceState` once per distinct resource type with a dummy ID. The
provider is never configured, no credentials are involved, and no API calls are
made. Any other outcome — a successful read, a rejected ID format, a missing
remote object — means the type *is* importable.

Probes are memoized per provider and type, so a 159-resource digest spanning 40
types costs 40 probes.

`digest tf` also computes, for each flagged resource, the data injection needs
later: Pulumi outputs, the raw state delta, and the Terraform schema version —
using the same provider connection the probe already opened. For the
line-level trace of that computation and how the pieces move between commands,
see [docs/pipeline-schema-and-state.md](pipeline-schema-and-state.md#s2b--the-non-importable-enrichment-new-on-this-branch).

## What each command does

`digest tf` sets `"nonImportable": true` on the flagged resources in
`tf-digest.json`. Pass `--skip-import-check` to skip the check entirely (it
avoids downloading Terraform provider binaries).

`resolve tf` leaves flagged resources out of the generated import file — an
entry for them is guaranteed to fail — and writes them to a sidecar next to
`--out`, e.g. `imports-ready.non-importable.json`:

```json
{
    "resources": [
        {
            "type": "aws:ec2/vpnGatewayRoutePropagation:VpnGatewayRoutePropagation",
            "name": "net-route_prop0",
            "terraformAddress": "module.net.aws_vpn_gateway_route_propagation.route_prop0",
            "id": "vgw-0a1b2c3d4e5f60718_rtb-0a1b2c3d4e5f60001",
            "attributes": { "route_table_id": "rtb-…", "vpn_gateway_id": "vgw-…" }
        }
    ]
}
```

### Sensitive attributes

The digest replaces attributes Terraform marked sensitive with the placeholder
`(sensitive)`, so the sidecar's `attributes` for those fields are **not real
values** — injecting them would write `(sensitive)` into state. The real value
is not lost: `digest tf` stores it in Pulumi stack config as a secret (unless
`--skip-secrets`). The sidecar records where:

```json
"redactedAttributes": { "shared_key": "route_shared_key" }
```

Resolve each from that config key before writing the resource to state, and
write it with Pulumi's secret envelope rather than in plaintext — the same
resolution `patch-state` already performs for sensitive fields
(`configSecrets` in `pkg/state_patcher.go`).

## What to do with them

Write them into the stack's state directly with `patch-state tf
--non-importable`. These types have working `Read` implementations even
without an importer, so the provider can read them back once their state
object is present with the right ID; the command builds that object from the
sidecar and a preview of the program.

### Stack mode — the tool runs the previews, backs up, imports and verifies

```bash
pulumi plugin run import -- patch-state tf \
  --digest tf-digest.json \
  --fields data/aws-import-diff-fields.json \
  --config-dir ./terraform \
  --non-importable imports-ready.non-importable.json \
  --project-dir . --stack dev
```

Omitting `--state`/`--out` selects stack mode. In order, the command:

1. exports the stack's current deployment;
2. writes a backup of that export and prints its absolute path;
3. takes a baseline preview, before any mutation;
4. patches state and injects the non-importable resources;
5. imports the result;
6. takes a verifying preview;
7. reverts to the pre-mutation export if verification fails.

The backup contains **decrypted secrets** — `pulumi stack export --show-secrets`
is what the Automation API runs underneath. Delete it once the change is
confirmed. `--backup-dir` chooses where it is written (default: the current
directory); create it somewhere with appropriate access if the default isn't
suitable.

Stack mode needs a runnable program and live credentials, because it runs
`pulumi preview` itself, twice.

### File mode — offline; you run the pulumi steps

```bash
pulumi preview --json > preview.json
pulumi stack export > state.json
pulumi plugin run import -- patch-state tf \
  --state state.json --out injected.json \
  --digest tf-digest.json \
  --fields data/aws-import-diff-fields.json \
  --config-dir ./terraform \
  --non-importable imports-ready.non-importable.json \
  --preview-json preview.json
pulumi stack import --file injected.json
pulumi preview   # must report zero operations
```

File mode is selected by passing both `--state` and `--out`. `--non-importable`
still requires a preview to source injected resources' URN, parent, provider
and dependencies from — supply it with `--preview-json` since there is no stack
to preview directly, and `--preview-json` is rejected in stack mode for the
same reason.

### How verification works

Verification is not "the preview must be clean." A stack mid-migration
legitimately still has outstanding diffs — `patch-state` is often run
iteratively against exactly that stack — so demanding a perfectly clean preview
after every pass would revert almost every legitimate run.

The actual gate, comparing the verifying preview against the baseline taken
before any mutation:

- every injected resource's URN must report `same`;
- no resource that was `same` (or absent) in the baseline may become non-`same`
  afterward;
- the total count of non-`same` steps outside the injected set must not
  increase.

What must not happen is regression. If either check fails, stack mode reverts
the stack to the pre-mutation export and reports why.

### Verify with preview, not refresh

Refresh normally corrects state from what is deployed — it calls the provider's
`Read` and writes back whatever comes out. Its reach is bounded by what `Read`
returns, and for these two types `Read` does not fetch attributes from AWS:

| Type | What `Read` does |
|---|---|
| `aws_vpn_gateway_route_propagation` | Calls no `d.Set()` at all. It confirms the propagation exists and returns (clearing the ID if it is gone). Terraform's read starts from the prior state and changes only what it sets, so refresh writes back exactly the values it was given. |
| `aws_vpn_connection_route` | Sets `destination_cidr_block` and `vpn_connection_id` — but parses both out of the resource **ID**, not out of an API response. |

So the value actually checked against AWS is the **ID**: it drives the existence
lookup. The attributes are either left untouched or re-derived from the ID.
Refresh can make attributes self-consistent with the ID; it cannot tell you the
ID names the object you meant.

Two consequences:

- **`pulumi refresh --preview-only` reporting "unchanged" is weak evidence.** It
  confirms the IDs resolve to something that exists. It would report the same
  for a resource whose attributes are wrong.
- **`pulumi preview` showing zero operations is the real check.** The program is
  the source of truth for inputs, so preview diffs the program against the
  injected state. If the injected values disagree, preview says so.

Get the ID right and `VpnConnectionRoute`'s attributes reconcile themselves on
the first refresh. `VpnGatewayRoutePropagation`'s attributes have to be right at
injection time, because nothing will ever reconcile them with AWS. The program
still polices them — the next `up` diffs against them — but `routeTableId` and
`vpnGatewayId` are force-new, so a mismatch surfaces as a **replace**, not a
repair: a disable-then-re-enable against live infrastructure, when the point of
injecting was a zero-op migration. If the ID is wrong in the same way, that
replace deletes whatever route table the ID names.

### Why `Read` semantics are not detected automatically

Importability can be probed because the SDKs answer it *before* doing any work:
the missing-importer check is the first thing `ImportResourceState` does, so an
unconfigured provider settles it for free. There is no equivalent for "does
`Read` populate its attributes".

Nothing in the schema records it — `ReadWithoutTimeout` is a Go func field, the
same blind spot as `Importer`. And the only way to observe it is to run a read
that succeeds, which needs a configured provider, live credentials, and a real
object to read; against a resource that does not exist, every provider returns
"gone" regardless of what its `Read` would have set. There is no cheap, safe
probe to add here.

This repo already treats that class of knowledge as curated data:
[`data/aws-import-diff-fields.json`](aws-import-diff-fields.md) records, per
type, the fields the provider does not return on import (`not_read`), so
`patch-state` can fill them from the digest. Read gaps for injected resources
belong in the same place — established by observation, not derivation.

## When the provider cannot be probed

Probing needs the Terraform provider binary at a known version, taken from
`.terraform.lock.hcl` in the Terraform directory. With no lock file (run
`terraform init`), no network, or an air-gapped environment, the tool falls back
to the curated list in `pkg/importsupport/fallback.json` and warns. Every entry
there was confirmed by probing a real provider. The list is a floor, not an
inventory: a type it doesn't cover is treated as unknown rather than guessed
either way.
