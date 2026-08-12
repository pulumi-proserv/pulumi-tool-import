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

## What to do with them

Write them into the stack's state directly. These types have working `Read`
implementations even without an importer, so a refresh will validate what you
inject. Append a state object per resource to a `pulumi stack export`, using the
ID and attributes from the sidecar, then `pulumi stack import` and confirm with
`pulumi refresh --preview-only` (expect "unchanged") and `pulumi preview`
(expect zero creates).

> `VpnGatewayRoutePropagation`'s `Read` sets **no** attributes — it only
> validates existence — so injected inputs and outputs are authoritative and are
> never corrected by a refresh. Getting them right matters.
> `VpnConnectionRoute`'s `Read` does set `destination_cidr_block` and
> `vpn_connection_id`.

## When the provider cannot be probed

Probing needs the Terraform provider binary at a known version, taken from
`.terraform.lock.hcl` in the Terraform directory. With no lock file (run
`terraform init`), no network, or an air-gapped environment, the tool falls back
to the curated list in `pkg/importsupport/fallback.json` and warns. Every entry
there was confirmed by probing a real provider. The list is a floor, not an
inventory: a type it doesn't cover is treated as unknown rather than guessed
either way.
