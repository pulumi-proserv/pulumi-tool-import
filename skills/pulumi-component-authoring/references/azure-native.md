# Azure-specific guidance (`@pulumi/azure-native` v3)

The main skill is written against `@pulumi/aws`. Azure differs in ways that
produce silent misconfiguration or failed deploys rather than type errors.
Apply this file whenever a component imports `@pulumi/azure-native`.

## Naming

**Globally named resources.** Any resource that gets a public DNS name is
unique across all of Azure, not just the subscription. Common ones: App
Service sites (`.azurewebsites.net`), SQL logical servers
(`.database.windows.net`), storage accounts, Key Vault, Container Registry,
Cosmos DB, Redis, Service Bus / Event Hubs namespaces, PostgreSQL / MySQL
flexible servers, Cognitive Services / OpenAI, API Management, AI Search,
Front Door endpoints, Traffic Manager labels, Data Factory.

Rules for components:

- Take the global name as an explicit `Input<string>` arg (`siteName`,
  `serverName`, `storageAccountName`). Do not rely on azure-native auto-naming
  for it; the caller must be able to make it stable across environments.
- The name does not drive control flow, so it stays `Input<string>`, not
  plain `string`. Use `pulumi.interpolate` when it feeds another value.
- Keep the generic name constraints in the doc comment: storage accounts are
  3-24 lowercase alphanumerics with no hyphens; Container Registry is
  alphanumeric only; most others allow hyphens but not underscores.

**Names that stay burned.** A failed create or a soft delete can hold the name
after the resource is gone, and Pulumi state correctly shows nothing to
manage:

| Resource | What holds the name |
|---|---|
| SQL logical server | A failed create leaves a reservation tied to the original region. Retrying the same name in another region returns 409 `InvalidResourceLocation`. |
| Key Vault | Soft delete, 7 to 90 days. With purge protection the name cannot be freed early. |
| Cognitive Services / OpenAI, API Management | Soft delete, 48 hours, purgeable. |
| Log Analytics workspace | Soft delete, 14 days. Same name in the same RG recovers the old workspace. |
| Storage account | Released within minutes; immediate retry can hit `AlreadyExists`. |

Mitigation in programs: derive the name from inputs that change when the
resource genuinely must move, such as hashing the region into a SQL server
name. Mitigation in components: document the soft-delete behavior on the name
arg.

**Auto-naming.** azure-native appends a random hex suffix to the logical name
for resources whose `<type>Name` arg is omitted. That is fine for
resource-group-scoped resources. Resource group names themselves get a suffix
too; export them if callers need the real name.

## Resource groups and location

- Every resource takes `resourceGroupName`. Components should accept it as
  `Input<string>` and never create the resource group themselves, since the
  RG is the customer's lifecycle and RBAC boundary.
- In the program, parent each component to its resource group
  (`{ parent: rg, provider }`) so preview and the console show the hierarchy
  Azure has. Children inherit the parent's provider, so this also removes the
  need to repeat `provider` on every child. Re-parenting changes URNs; on an
  existing stack add `aliases: [{ parent: pulumi.rootStackResource }]` for one
  update, then remove it.
- `location` is required on most resources. Some must be the literal string
  `"global"`: Front Door profiles and their child resources, private and
  public DNS zones, Traffic Manager profiles. Passing a region to those fails.
- A private endpoint's `location` must be the VNet's region, which can differ
  from the region of the resource it fronts. Give the private endpoint its
  own optional `location` arg.
- Provisioning of some services is restricted per subscription per region
  (`ProvisioningDisabled`, typical for Azure SQL in busy regions). Let the
  component's `location` differ from the program's default so a single
  service can move without the whole stack moving.
- App Service quota is per SKU per region per subscription, and a new or
  internal subscription can have 0 for most tiers (`Operation cannot be
  completed without additional quota ... Current Limit (Total VMs): 0`).
  Check before choosing a plan SKU:
  `az quota list --scope /subscriptions/<sub>/providers/Microsoft.Web/locations/<region>`.
  SQL region availability: GET `Microsoft.Sql/locations/<region>/capabilities`
  and look for `status: Available` (not `Visible`).

## Size and SKU values are enumerated, not ranges

Many Azure sizes accept only listed values. DTU-based SQL elastic pools use
decimal megabytes (a Basic 50 eDTU pool must be exactly 5000 MB, not 5 GiB),
while vCore tiers use binary GiB. Expose a byte-precise or MB-precise arg
alongside the friendly GB one, and read allowed values from the capabilities
API (`?include=supportedElasticPoolEditions`) rather than guessing. The same
applies to per-database DTU caps inside a pool (Basic allows only 5).

## Module and type locations in v3

These are the mistakes that cost the most time because they surface only at
compile or preview:

| Resource | Correct module | Common wrong guess |
|---|---|---|
| Application Insights component | `applicationinsights.Component` | `insights.Component` |
| Diagnostic settings | `monitor.DiagnosticSetting` | `insights.DiagnosticSetting` |
| Front Door WAF policy | `frontdoor.Policy` | `network.Policy` |
| Front Door Standard/Premium | `cdn.Profile`, `cdn.AFDEndpoint`, `cdn.AFDOriginGroup`, `cdn.AFDOrigin`, `cdn.Route` | `frontdoor.*` (that is classic Front Door) |
| Private DNS records | `privatedns.PrivateRecordSet` | `network.PrivateRecordSet` |

Input types live at the package root, not on the module:

```typescript
import { types } from "@pulumi/azure-native";
const rule: types.input.web.IpSecurityRestrictionArgs = { ... };
// not: web.types.input.IpSecurityRestrictionArgs
```

Check a resource's args locally rather than guessing:

```bash
grep -nE '^\s+[a-zA-Z]+\??: pulumi.Input' node_modules/@pulumi/azure-native/<module>/<resource>.d.ts
```

## Superseded properties

Newer API versions replace properties rather than remove them, and the old
name is still accepted and silently ignored. A property that "does not
work" on the default version usually has a successor. Before pinning an
older version, search the SDK types for `Previously called` and for a
site-level object that covers the same setting:

```bash
grep -n 'Previously called' node_modules/@pulumi/azure-native/types/input.d.ts
```

Known case: on `Microsoft.Web/sites` from API 2024-11-01,
`siteConfig.vnetRouteAllEnabled` is superseded by the site-level
`outboundVnetRouting` object (`applicationTraffic` is the old route-all flag;
`allTraffic`, `imagePullTraffic`, `contentShareTraffic`, `backupRestoreTraffic`
are the other classes). Sending only the old flag leaves route-all off with
no drift shown, because the GET returns null for the old property.

Pinning a resource to an older API version is possible (the provider serves
every version as tokens such as `azure-native:web/v20240401:WebApp`, usable
through a `pulumi.CustomResource` subclass), but it is a last resort: it
forgoes newer properties and a later un-pin replaces the resource.

## Networking pitfalls

- **Never mix inline subnets and standalone `network.Subnet` resources on the
  same VNet.** The VNet reconciles its inline `subnets` list and deletes
  anything it did not declare, giving a perpetual create/delete loop. Use
  standalone subnets and put `ignoreChanges: ["subnets"]` on the VNet.
- **Serialize subnet writes.** Azure rejects concurrent writes to one VNet
  (`AnotherOperationInProgress`). Chain each `Subnet` with `dependsOn` on the
  previous one.
- App Service VNet integration needs a subnet delegated to
  `Microsoft.Web/serverFarms`, and that subnet cannot also host private
  endpoints.
- Private endpoint subnets should set `privateEndpointNetworkPolicies:
  "Disabled"` explicitly rather than relying on the portal default.

## Private endpoints and DNS

The pattern is always the same three resources, so factor it into a shared
helper rather than repeating it per component:

1. `network.PrivateEndpoint` with `privateLinkServiceConnections[0].groupIds`
   set to the service's sub-resource (`sites`, `sqlServer`, `blob`, `vault`).
2. DNS registration in the `privatelink.<service>` zone, either
   `network.PrivateDnsZoneGroup` (Azure writes the records) or explicit
   `privatedns.PrivateRecordSet` A records through a provider for the zone's
   subscription.
3. The endpoint's `customDnsConfigs` output carries the FQDN and private IP.
   Some services add a second FQDN (App Service adds `.scm.`); select by FQDN,
   not by array index.

The zone usually lives in a central connectivity subscription. Offer both
registration modes as a plain-string `mode` arg and validate that `provider`
is only supplied in the explicit-record mode. Many enterprises register
endpoints by Azure Policy instead, so make DNS registration optional.

## Explicit providers

Declare the subscription in the program rather than inheriting it from
`ARM_SUBSCRIPTION_ID` alone, and pass the provider to every component:

```typescript
const provider = new azure_native.Provider("azure", {
    subscriptionId: cfg.require("subscriptionId"),   // or process.env.ARM_SUBSCRIPTION_ID
    location: cfg.require("location"),
});
const net = new Networking("net", { ... }, { provider });
```

Credentials still come from the environment (an ESC environment exporting
`ARM_*`), so the provider block is about pinning *where*, not *who*.

## Cross-subscription resources

Create a second provider and pass it per resource. Provider functions also
need `{ parent: this, provider }` or they run against the default
subscription:

```typescript
const hubProvider = new azure_native.Provider("hub", { subscriptionId: hubSubscriptionId });
new network.HubVirtualNetworkConnection(`${name}-hubconn`, { ... }, { parent: this, provider: hubProvider });
```

Resources that are children of something in another subscription (vWAN hub
connections, private DNS records) are created there, so the deploying
identity needs write rights in that subscription. Make the provider an
optional arg on the component and say so in its doc comment.

## Ordering and slow operations

- Front Door: origin group before origins before routes. Pass `.id` outputs
  and add `dependsOn` from routes to origins. Profile deletion takes 10 to 30
  minutes.
- vWAN hub creation takes 5 to 30 minutes. Do not put one in a smoke test
  unless the test budget allows it.
- Front Door Premium Private Link origins create a private endpoint
  connection on the origin that the origin owner must approve. The connection
  name is generated, so approval needs discovery at deploy time (a
  `command.local.Command` running `az network private-endpoint-connection`)
  or an out-of-band step.

## Authentication

Prefer `ARM_*` environment variables from an ESC environment over an
interactive `az login`. The CLI's federated token expires after an hour and a
long deploy fails midway with `AADSTS700024: Client assertion is not within
its valid time range`. An ESC environment can only attach to a stack in the
same Pulumi organization.

`Pulumi.<stack>.yaml` is keyed by stack name, not organization. `pulumi stack
rm org-a/dev` deletes the file that `org-b/dev` also uses, silently dropping
its config and its ESC environment. After removing a same-named stack, check
`pulumi config env ls` and `pulumi config` before the next `up`.
