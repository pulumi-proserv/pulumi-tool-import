---
name: pulumi-terraform-module-to-component
description: Convert a Terraform module into a Pulumi TypeScript ComponentResource class using the `tf-digest` output from pulumi-tool-import. Covers reading the digest module interface, choosing which TF variables become component args, Terraform-to-TypeScript type mapping, replacing TF data sources with Pulumi lookups, and the resource logical-naming rule that lets `import-id-match` pair component children with TF state automatically. Use when migrating a Terraform module (tfmod) to a Pulumi component, auditing an existing component against its TF module, or debugging import ID fill failures caused by name mismatches. Load together with the pulumi-component-authoring skill, which covers everything not Terraform-specific.
---

# Terraform module → Pulumi component

Converts a Terraform module into a Pulumi TypeScript `ComponentResource`.

**Companion skill (load it too):** **pulumi-component-authoring** — interface
design (`Input<T>` vs plain `T`, array types, discriminated unions), Output
lifting, IAM policy documents, packaging, publishing, and smoke tests. This skill
covers only what is specific to translating *from Terraform*.

## Prerequisites

- A `tf-digest` output for the workspace that calls this module, produced by
  `pulumi-tool-import` (see the **pulumi-terraform-workspace-migration**
  skill). The digest is the agent-safe view of TF state — **never read the raw
  `.tfstate`**, it contains secrets.

  If the tool is not installed yet:

  ```bash
  pulumi plugin install tool import \
    --server github://api.github.com/pulumi-proserv/pulumi-tool-import
  ```

  With no version it installs the latest release. The
  `github://api.github.com/<owner>/<repo>` form is required — a plain
  `https://github.com/...` server 404s.

## Phase 1 — Analyze the module

Read the digest entry for this module:

- `interface.inputs[]` — name, type, required, default, expression, evaluatedValue
- `interface.outputs[]` — output values
- `resources[]` — each with `mode`, `translatedUrn`, `terraformAddress`, `importId`

Read the module's `.tf` source for what the digest can't show: resource
relationships, conditional logic (`count`, `for_each`), local transformations,
and data-source lookups.

> Read the module source from the workspace's resolved copy —
> `<workspace>/.terraform/modules/<module_name>/` — not from the module's git
> repo at some other ref. Module versions differ between workspaces, and the
> resolved copy is the version whose state you are importing.

**Only include inputs that callers actually use.** Check each input's
`expression` in the digest — if it is empty/null across every call site, omit it
from the component interface. Large TF modules often declare 50–100 variables
while callers use a fraction. The interface can always be extended later.

## Phase 2 — Design the interface

Follow **pulumi-component-authoring** for the general rules. Terraform-specific
type mapping:

| Terraform type | Pulumi type |
|---|---|
| `string` | `pulumi.Input<string>` |
| `number` | `pulumi.Input<number>` |
| `bool` | `pulumi.Input<boolean>` |
| `list(string)` (pass-through) | `pulumi.Input<pulumi.Input<string>[]>` |
| `list(string)` (drives resource count) | `pulumi.Input<string>[]` |
| `map(string)` | `pulumi.Input<Record<string, pulumi.Input<string>>>` |

### Simplify TF idioms rather than mirroring them

HCL's type system forces workarounds that TypeScript does not need:

- **TF `list(object({key=string, value=string}))` → TS
  `Record<string, Input<string>>`** when the list is really a key-value map. HCL
  lacks map-with-dynamic-values support, so TF modules model maps as
  lists-of-objects. Use `pulumi.output(myRecord).apply(...)` to deep-resolve a
  record of Inputs.
- A TF `bool` variable that only gates `count = var.x ? 1 : 0` becomes a plain
  `boolean` in TypeScript (it drives control flow — see the authoring skill).

## Phase 3 — Replace data sources, never hardcode

**Key rule: never replace a dynamic TF data source with a hardcoded value.** If
the TF code references a data source or resource output, the Pulumi code must
also use a dynamic reference. Hardcoded values silently drift when the underlying
resource changes, break in another environment/account, and hide dependencies
Pulumi needs for correct ordering. The only acceptable hardcoded values are ones
that were hardcoded in the TF source too.

| TF data source | Pulumi equivalent | Notes |
|---|---|---|
| `aws_caller_identity` / `aws_region` / `aws_partition` | `aws.getCallerIdentityOutput()`, etc. | Direct equivalents |
| `aws_iam_policy_document` | `aws.iam.getPolicyDocumentOutput()` | See the authoring skill's IAM reference |
| `aws_kms_alias` / `aws_kms_key` | `aws.kms.getAliasOutput()` | Dynamic lookup |
| `aws_instance` (lookup) | `aws.ec2.getInstanceOutput()` | Or a config value for the instance ID |
| `aws_ip_ranges` | `aws.getIpRangesOutput()` | Direct equivalent |
| `aws_cloudformation_stack` | `aws.cloudformation.getStackOutput()` | In-process lookup |
| `archive_file` | `pulumi.asset.FileArchive` / `AssetArchive` | Built-in |
| `terraform_remote_state` | ESC environment values surfaced as stack config | See the workspace-migration skill |

**In-component vs caller-provided.** When the TF *module* references a data
source internally (e.g. `data.aws_caller_identity.current`), the component should
look it up internally too — don't push it onto the caller unless the value is
genuinely environment-specific config. When the TF *root* module passes a data
source result into the module as a variable, the component should accept it as an
`Input<T>` arg.

Remember that provider functions need `{ parent: this }` (authoring skill).

## Phase 4 — Resource logical names MUST match TF resource names

This is the rule that makes automated import work, and it is specific to this
migration path.

Build each child's logical name from the component's `name` parameter plus the
**Terraform resource name** as the suffix. The TF address format is
`module.<mod>.<resource_type>.<resource_name>` — use the part after the last `.`.

```typescript
// TF module has: aws_rds_cluster.aurora_cluster
//                aws_security_group.rds
const cluster = new aws.rds.Cluster(`${name}-aurora_cluster`, { ... }, { parent: this });
const sg      = new aws.ec2.SecurityGroup(`${name}-rds`, { ... }, { parent: this });
```

**Why:** `import-id-match` fills import-file IDs by extracting the resource-name
suffix from both the TF address and the Pulumi import entry. Matching suffixes
mean IDs are filled deterministically by type + name, with no heuristic
disambiguation:

```
TF digest:   module.data_rds.aws_rds_cluster.aurora_cluster  → name: "aurora_cluster"
Import file: name: "data_rds-aurora_cluster", parent: "data_rds" → suffix: "aurora_cluster"
                                                                    ✓ match
```

**For indexed resources** (`count` / `for_each`), include the instance key:

```typescript
// TF: aws_subnet.public[0], aws_subnet.public[1]
for (let i = 0; i < subnets.length; i++) {
    new aws.ec2.Subnet(`${name}-public_${i}`, { ... }, { parent: this });
}

// TF: aws_ssm_parameter.params["key_name"]
for (const [key, value] of Object.entries(args.parameters)) {
    new aws.ssm.Parameter(`${name}-params_${key}`, { ... }, { parent: this });
}
```

**For resources with no TF counterpart** (new resources the component adds), any
descriptive suffix works — they will not be import-matched. But see the next
point.

**Don't create resources the TF module doesn't manage.** A component that creates
extra resources (e.g. `BucketOwnershipControls` where TF has none) makes them
appear as creates during a migration preview. Put such resources behind a flag
that the migration program sets to `false`.

## Phase 5 — Audit an existing component against its module

When fixing a component rather than writing one, do a three-way comparison per
field:

1. **Digest** — the *evaluated* value: what TF actually produced.
2. **TF source** — the *computation logic*: how the value should be derived.
3. **Pulumi code** — verify it produces the same value using equivalent logic.

If the Pulumi code hardcodes the correct value while the TF code computes it
dynamically, fix it to compute dynamically — the hardcoded version will be wrong
in every other workspace.

## Troubleshooting

| Issue | Solution |
|-------|----------|
| `import-id-match` reports unmatched entries | The child's logical-name suffix doesn't equal the TF resource name. Fix the suffix, or add an explicit entry to the mappings file. |
| Component type error against digest inputs | Verify the args interface matches `interface.inputs` — check for a TF `list(object)` that should be a TS `Record`. |
| Import file shows a type mismatch | Use the current, non-`V2` Pulumi resource types (e.g. TF `aws_s3_bucket` → `aws:s3/bucket:Bucket`, not `BucketV2`). |
| A resource in the program isn't in TF state | It was never applied. It will be created on first `pulumi up` rather than imported — confirm that's intended. |
