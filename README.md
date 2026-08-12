# pulumi-tool-import

`pulumi-tool-import` helps migrate existing infrastructure into Pulumi using the
native [`pulumi import`](https://www.pulumi.com/docs/iac/adopting-pulumi/import/)
workflow — analyzing the source, computing the import IDs Pulumi needs, and
patching imported state so `pulumi preview` comes back clean.

It works from two sources, onto the same bridged Pulumi providers (AWS first):

- **Terraform / OpenTofu** — reads state + HCL configuration
- **AWS CDK / CloudFormation** — reads a deployed CloudFormation stack

The commands form a pipeline:

```
digest (tf|cfn)  →  resolve (tf|cfn)  →  import  →  patch-state  →  zero-diff preview
```

- **`digest`** — analyze the source (Terraform state/HCL, or a deployed
  CloudFormation stack) into an agent-safe JSON sidecar; auto-discovers secrets.
- **`resolve`** — fill a `pulumi preview --import-file` skeleton with the real
  import IDs, matching source resources to the Pulumi program.
- **`import`** — run the import in batches, isolating per-resource failures so one
  run reports every bad import ID.
- **`patch-state`** — patch imported state with field values the cloud API doesn't
  return (write-only fields, IaC-only defaults, asset sentinels) to eliminate
  post-import diffs.
- **`set-secrets`** — extract secret values from the source and set them as
  encrypted Pulumi config secrets.

For general migration guidance, see the
[official Pulumi migration docs](https://www.pulumi.com/docs/iac/guides/migration/migrating-to-pulumi/from-terraform/).

## Installation and invocation

Every command runs through the Pulumi plugin runner:

```bash
pulumi plugin run import -- <command> [flags]

# e.g.
pulumi plugin run import -- digest tf --help
```

The plugin reads cloud credentials from the environment (for the AWS lookups in
`digest cfn` and `patch-state`). Wrap the command with `pulumi env run <esc-env> --`
if you source credentials from ESC.

> **Compatibility aliases.** The earlier flat commands `tf-digest` and
> `import-id-match` remain as hidden aliases of `digest tf` and `resolve tf` so
> existing scripts keep working. New usage should prefer the subcommand names.

## The migration pipeline

A typical end-to-end migration:

```bash
# 1. Digest the source into an agent-safe sidecar (+ auto-set secrets)
pulumi plugin run import -- digest tf \
  --from ./terraform --state-file terraform.tfstate \
  --pulumi-project myproject --pulumi-stack dev \
  --out /tmp/digest.json

# 2. Generate the import skeleton from a Pulumi preview
pulumi preview --import-file import.json

# 3. Fill the skeleton's import IDs from the digest
pulumi plugin run import -- resolve tf \
  --digest /tmp/digest.json --import-file import.json \
  --mapping-file mappings.yaml --out imports-ready.json

# 4. Import (batched, failure-isolating)
pulumi plugin run import -- import \
  --file imports-ready.json --project-dir . --stack dev

# 5. Patch state so preview is clean
pulumi stack export > state.json
pulumi plugin run import -- patch-state tf \
  --state state.json --digest /tmp/digest.json \
  --fields data/aws-import-diff-fields.json \
  --mapping-file mappings.yaml --config-dir ./terraform \
  --out patched-state.json
pulumi stack import --file patched-state.json
```

The CloudFormation path swaps steps 1 and 3 for `digest cfn` / `resolve cfn` (and
`patch-state cfn`), and is otherwise identical.

---

## `digest tf`

Digests Terraform configuration and state into a `tf-digest.json` sidecar that
describes Terraform module instances, their interfaces (inputs/outputs), and the
Pulumi URNs of the resources belonging to each module instance. This is the
agent-safe artifact the rest of the pipeline reads instead of raw state.

State can come from a local file (`--state-file`) or a TFC-compatible remote
backend (`--hostname`, `--organization`, `--workspace`, `--token-env`) —
Terraform Cloud/Enterprise or Scalr.

```bash
# From a local state file
pulumi plugin run import -- digest tf \
  --from ./terraform \
  --state-file terraform.tfstate \
  --out /tmp/tf-digest.json \
  --pulumi-project myproject --pulumi-stack dev

# From a TFC-compatible remote
pulumi plugin run import -- digest tf \
  --from ./terraform \
  --hostname app.terraform.io --organization my-org \
  --workspace my-workspace-dev --token-env TFC_TOKEN \
  --out /tmp/tf-digest.json \
  --pulumi-project myproject --pulumi-stack dev
```

Key flags: `--from` (Terraform root), `--state-file` or the `--hostname/--organization/--workspace/--token-env`
remote set, `--out`, `--pulumi-project`/`--pulumi-stack` (for URN generation),
`--project-dir` and `--skip-secrets` (secret handling, below).

**Secrets.** Sensitive attributes are discovered and set as encrypted Pulumi
stack-config secrets in `--project-dir`; pass `--skip-secrets` to leave them out.
Because a digest can still embed sensitive values in non-sensitive string fields,
treat the digest as sensitive and `.gitignore` it.

## `digest cfn`

Digests a deployed CloudFormation stack into an agent-safe digest JSON. The
**deployed stack is the source of truth** — the digest reads the live template
(`GetTemplate`) plus stack resources, resolving intrinsics (`Ref`, `Fn::GetAtt`,
`Fn::ImportValue`, `Fn::Join`) against the account, and pre-resolves import IDs
for the resource types that require a live AWS lookup.

```bash
pulumi plugin run import -- digest cfn \
  --stack-name my-service-dev \
  --region us-east-1 \
  --out /tmp/cfn-digest.json \
  --pulumi-project myproject --pulumi-stack dev
```

Key flags: `--stack-name`, `--region`, `--out`, `--pulumi-project`/`--pulumi-stack`
+ `--project-dir` (secret extraction), `--skip-secrets`.

**Secrets.** By default, sensitive inline property values (e.g. SecretsManager
`SecretString`, RDS `MasterUserPassword`) are discovered, redacted from the
digest, and set as encrypted stack-config secrets — this requires `--pulumi-stack`
and `--pulumi-project`. With `--skip-secrets` the values stay in the digest as
plaintext, so `.gitignore` it.

## `resolve tf`

Matches Terraform resources from a `tf-digest.json` to the entries in a Pulumi
import file (from `pulumi preview --import-file`) and fills in the placeholder
import IDs.

```bash
pulumi plugin run import -- resolve tf \
  --digest /tmp/tf-digest.json \
  --import-file import.json \
  --mapping-file mappings.yaml \
  --out imports-ready.json
```

**Matching algorithm.** Resources are matched by **type + name within each mapped
module/component pair**. Module-to-component mappings tell the tool which Pulumi
component instance corresponds to which Terraform module path; resource-level
mappings handle individual resources that don't follow the naming convention and
are applied first. Composite/derived import IDs (e.g. Lambda Permission
`FunctionName/StatementId`, Route53 records, security-group rules) are composed
from the digest's attributes via a shared resolver core.

**Mappings** may be passed inline (`--map 'module.X=componentName'`, repeatable) or
via `--mapping-file`:

```yaml
modules:
  "module.core_rds": "core_rds"
  "module.console_ui[\"mysvc\"]": "console_ui[\"mysvc\"]"
resources:
  "aws_s3_bucket.my_bucket": "my_bucket"
```

## `resolve cfn`

Fills a Pulumi import skeleton from a `cfn-digest.json`, matching each entry to a
CFN resource by logical ID and composing the import ID via the same shared
resolver core `resolve tf` uses.

```bash
pulumi plugin run import -- resolve cfn \
  --digest /tmp/cfn-digest.json \
  --import-file import.json \
  --provider classic \
  --out imports-ready.json
```

Key flags: `--digest`, `--import-file`, `--out`, `--mapping-file` (optional YAML
map of import-entry name → CFN logical ID), and `--provider`:

- **`classic`** (default) — emit `aws` (aws-classic) import IDs everywhere.
- **`native`** — emit `aws-native` (Cloud Control) import IDs, scoped to the API
  Gateway resource family where classic explodes and the Cloud Control
  identifier-order quirks matter.

Some CFN resource types have no import path at all in the provider (pure
association/toggle resources). Those are surfaced rather than emitted as
guaranteed-to-fail entries.

## `import`

Imports the resources in a prepared import file in **batches**, and reports every
resource that failed — instead of aborting the whole run on the first bad ID.

```bash
# Inspect the plan without importing
pulumi plugin run import -- import \
  --file imports-ready.json --project-dir . --stack dev --dry-run

# Import, 50 resources per batch
pulumi plugin run import -- import \
  --file imports-ready.json --project-dir . --stack dev --batch-size 50
```

A single malformed import ID fails a whole `pulumi import` deployment, but
already-succeeded steps are committed to state and not rolled back. When a batch
doesn't fully land, this command re-imports the batch's missing resources one at a
time to identify exactly which failed, records them, and carries on — so one run
surfaces every bad import ID. Success is determined by reading stack state
afterward, not the importer's exit status; resources already in state are skipped,
so a run can be repeated after fixing IDs (`--no-resume` disables this). Resources
are always imported unprotected and without code generation.

Key flags: `--file`, `--project-dir`, `--stack`, `--batch-size` (default 100),
`--dry-run`, `--no-resume`.

## `patch-state tf` / `patch-state cfn`

Patches a Pulumi stack state (from `pulumi stack export`) with field values that
the cloud API doesn't return on import — write-only fields, IaC-only defaults, and
asset sentinels — so `pulumi preview` is a strict no-op. Re-import the result with
`pulumi stack import --file <output>`.

Both variants read a **curated fields file** (`--fields`,
[`data/aws-import-diff-fields.json`](./data/aws-import-diff-fields.json)) that
lists, per resource type, which fields are not returned by the cloud API and how
to fill them. For each matching resource with a nil input, the value comes from
(1) the digest's per-resource attribute, else (2) the fields-file default.
Falsy-default suppression (keyed on provider version) avoids re-introducing
phantom diffs on newer providers.

```bash
# Terraform
pulumi plugin run import -- patch-state tf \
  --state state.json --digest /tmp/tf-digest.json \
  --fields data/aws-import-diff-fields.json \
  --mapping-file mappings.yaml --config-dir ./terraform \
  --out patched-state.json

# CloudFormation
pulumi plugin run import -- patch-state cfn \
  --state state.json --digest /tmp/cfn-digest.json \
  --fields data/aws-import-diff-fields.json \
  --region us-east-1 --artifacts-dir ./artifacts \
  --out patched-state.json
```

`patch-state cfn` additionally downloads deployed **Lambda code** zips for
functions present in the migrated state into `--artifacts-dir`, referencing them
as local `FileArchive`s so preview is clean without embedding CDK build artifacts.

Both can read secret config values from a stack (`--project-dir` + `--stack`) to
avoid re-patching secret fields.

## `set-secrets`

Extracts specific secret values from Terraform state and sets them as encrypted
Pulumi config secrets — so an agent can orchestrate secret migration without ever
seeing the values. The stack is initialized automatically if it doesn't exist.

```bash
pulumi plugin run import -- set-secrets \
  --state-file terraform.tfstate \
  --project-dir ./pulumi --stack prod \
  --map 'dbPassword=aws_ssm_parameter.db_password:value' \
  --map 'apiKey=aws_secretsmanager_secret_version.api_key:secret_string'
```

`--map configKey=terraform.address:attribute` is repeatable.

## `show-state` (debug)

Loads Terraform/OpenTofu state (via `tofu show -json`) and pretty-prints it as
JSON — a convenience for inspecting what the tool sees.

```bash
pulumi plugin run import -- show-state --state-file terraform.tfstate
pulumi plugin run import -- show-state --project-dir ./terraform --workspace dev
```

---

## Security note

Digests and state exports frequently contain secrets, and a digest may embed
sensitive values inside otherwise non-sensitive string fields. Treat generated
artifacts (`tf-digest.json`, `cfn-digest.json`, exported state) as sensitive and
`.gitignore` them. See [`SECURITY.md`](./SECURITY.md) to report vulnerabilities.

## Contributing

See [`CONTRIBUTING.md`](./CONTRIBUTING.md). Run `make check` (fmt-check + vet + lint)
before opening a PR.
