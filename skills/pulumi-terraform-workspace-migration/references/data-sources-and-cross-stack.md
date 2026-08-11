# Data sources, cross-stack references, and non-resource TF patterns

## The rule

**Never replace a dynamic TF data source with a hardcoded value.** If the TF code
references a data source or a resource output, the Pulumi code must use a dynamic
reference too. Hardcoded values:

- silently drift when the underlying resource changes,
- break when deployed to a different environment or account,
- hide dependencies Pulumi needs for correct ordering.

The only acceptable hardcoded values are ones that were hardcoded in the TF
source too (e.g. a genuinely static URL).

## AWS data-source equivalents

| TF data source | Pulumi equivalent | Notes |
|---|---|---|
| `aws_caller_identity` / `aws_region` / `aws_partition` | `aws.getCallerIdentityOutput()`, etc. | Define once in a shared config module |
| `aws_cloudformation_stack` | `aws.cloudformation.getStackOutput()` | In-process lookup |
| `aws_iam_policy_document` | `aws.iam.getPolicyDocumentOutput()` | Strongly preferred over hand-built JSON |
| `aws_kms_alias` / `aws_kms_key` | `aws.kms.getAliasOutput()` | Dynamic lookup |
| `aws_instance` (lookup) | `aws.ec2.getInstanceOutput()` | Or a config value for the instance ID |
| `aws_ip_ranges` | `aws.getIpRangesOutput()` | Direct equivalent |
| `archive_file` | `pulumi.asset.FileArchive` / `AssetArchive` | Built-in |

Provider functions need `{ parent: this }` inside a component — see the
**pulumi-component-authoring** skill.

## Non-data-source TF patterns

| TF pattern | Pulumi replacement |
|---|---|
| `null_resource` with provisioners | Decide whether the provisioner is still needed. If it was a one-time action (seeding data, a migration) that has already run and would be destructive to repeat, omit it with a comment recording the decision. If it is still needed, use `pulumi-command` (`local.Command` / `remote.Command`) — but note `command:local:Command` **cannot be imported** (there is no physical resource to read), so it executes on the first `pulumi up`. Verify that is safe. |
| `null_resource` as a dependency trigger | Remove it — Pulumi models dependencies natively via `dependsOn` |
| A provider with no native Pulumi package | Check the [Pulumi Registry](https://www.pulumi.com/registry/) and [Pulumiverse](https://github.com/pulumiverse) first. Only if neither has it, use the [dynamically bridged provider](https://www.pulumi.com/registry/packages/terraform-provider/): `pulumi package add terraform-provider <provider-source>` |

## Replacing `data.terraform_remote_state`

**Preferred — the ESC `terraform-state` provider.** Define an ESC environment (as
a `pulumiservice.Environment` resource in a shared project) that pulls outputs
from the TF workspace, reference that environment from the consuming stack's
config, and read the values in the program as ordinary config.

```typescript
// In a shared ESC-environments project:
import * as pulumiservice from "@pulumi/pulumiservice";

new pulumiservice.Environment("<project>-<env>", {
    organization: "<org>",
    project: "<esc-project>",
    name: "<env>",
    yaml: new pulumi.asset.StringAsset(`imports:
  - <credentials-env>
values:
  remote-state:
    fn::open::terraform-state:
      organization: <tf-org>
      hostname: <tf-api-hostname>
      token: \${pulumiConfig.tf_token}
      workspace: <tf-workspace-name>
  pulumiConfig:
    networkOutputs: \${remote-state.network}
`),
});
```

```yaml
# In the consuming stack's Pulumi.<env>.yaml:
environment:
  - <esc-project>/<env>
```

```typescript
// In the consuming program — the value arrives as config:
const network = config.requireObject<NetworkOutputs>("networkOutputs");
```

See the [ESC Terraform state docs](https://www.pulumi.com/docs/esc/integrations/infrastructure/terraform/terraform-state/).

**Alternative — `getRemoteReferenceOutput`.** Reads TF remote state directly in
code, which requires the auth token in stack config and couples the program to
the TF backend:

```typescript
import * as terraform from "@pulumi/terraform";

const networkState = terraform.state.getRemoteReferenceOutput({
    hostname: "app.terraform.io",
    token: config.requireSecret("tfToken"),
    organization: "<org>",
    workspaces: { name: "<workspace>" },
});

const vpcId = networkState.outputs.apply(o => o["vpc_id"]);
```

## Pulumi-to-Pulumi cross-stack references

Once the upstream workspace is itself migrated, use a `StackReference`:

```typescript
const networkStack = new pulumi.StackReference("<org>/<project>/<stack>");
const certificateArn = networkStack.getOutput("certificateArn");
```

Migrate in dependency order so downstream stacks can switch from ESC-mediated TF
state to `StackReference` as their upstreams land.

## Verification sweep

After writing the program, grep for values that should have been dynamic:

- hardcoded account IDs → `aws.getCallerIdentityOutput().accountId`
- hardcoded ALB / CloudFront / API Gateway DNS names → resource or component outputs
- hardcoded ARNs containing account IDs or resource IDs → `pulumi.interpolate` with dynamic references
- hardcoded KMS alias ARNs → `aws.kms.getAliasOutput()`
- hardcoded CIDR blocks that came from VPC/subnet data sources → VPC component outputs
