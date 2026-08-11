# Import mechanics

## Contents

- [Generate the import skeleton](#generate-the-import-skeleton)
- [Write mappings.yaml](#write-mappingsyaml)
- [Fill IDs with import-id-match](#fill-ids-with-import-id-match)
- [Import-file hygiene](#import-file-hygiene)
- [Multi-provider setup](#multi-provider-setup)
- [Run the import](#run-the-import)
- [Digest caveats](#digest-caveats)

## Generate the import skeleton

```bash
pulumi preview --import-file .import/imports.json
```

The skeleton's types and names come from your hand-authored program, so write the
program code for a node *before* generating its import entries.

## Write `mappings.yaml`

Two sections:

```yaml
modules:
  # TF module path → Pulumi component instance name
  "module.my_rds": "my_rds"
  "module.my_ui[\"app\"]": "my_ui[\"app\"]"
resources:
  # TF resource address → Pulumi resource name (only where the names differ)
  "module.my_rds.aws_rds_cluster.aurora_cluster": "my_rds-cluster"
  "aws_s3_bucket_acl.my_bucket": "my_bucketBucketAcl"
```

**Module mappings** come from the digest's module keys. They are often identical
to the component instance name — watch for bracket-to-dash differences
(TF `module.vpc["app"]` → Pulumi `vpc-app`).

**Resource mappings** are needed when the Pulumi resource name suffix doesn't
match the TF resource name — which happens for:

- **Component children with generic names.** A component may name a child
  `cluster` / `sg` / `instance-0` where TF has `aurora_cluster` /
  `db_security_group` / `this[0]`. Read the component source, compare to the
  digest, and write the mapping. Better: rename the child to match the TF
  resource name — see the **pulumi-terraform-module-to-component** skill.
- **Root resources with auto-suffixed names.** Pulumi appends the type name to
  resources created at root level without a parent (`my_bucketBucketAcl`,
  `cf["app"]ApiMapping`, `rds["admin"]Secret`). Map these to their TF addresses.
- **Resources with no TF equivalent.** Look up the import ID format in the Pulumi
  registry and set it manually (e.g. bucket ownership controls import by bucket
  name; `RolePolicyAttachment` imports as `<role-name>/<policy-arn>`).

## Fill IDs with `import-id-match`

```bash
pulumi plugin run import -- import-id-match \
  --digest .import/tf-digest.json \
  --import-file .import/imports.json \
  --mapping-file mappings.yaml \
  --out .import/imports-ready.json
```

The command fills placeholder IDs from the digest using the module + resource
mappings, translates TF import IDs into Pulumi format for many resource types
(S3 BucketObject, WAFv2, ECS, Lambda, IAM, …), and preserves `provider` fields
and the `nameTable` from the input file. The output is ready to import — no
post-processing.

Review the fill rate and iterate on the mappings and the program until it is
high. Common causes of a low rate:

- **Type mismatch.** Use the current AWS provider major version and the
  non-`V2` types — TF `aws_s3_bucket` maps to `aws:s3/bucket:Bucket`, not
  `BucketV2`.
- **The program defines different resources than TF does.** Fix the program to
  match TF state exactly.
- **Missing resources.** If TF manages resources the program doesn't create, add
  them.
- **Unsupported import-ID format.** When an import fails with "resource does not
  exist", treat it as a *wrong ID format*, not a missing resource: check the
  type's documented format (`pulumi package get-schema aws`, the `## Import`
  section). If the built-in translation doesn't cover the type, add it to
  `TranslateImportIDs` in the tool, or set the ID via a resource mapping.
- **Non-importable types.** Some TF types map to Pulumi types that cannot be
  imported — e.g. `aws:iam/policyAttachment:PolicyAttachment`. Use
  `aws:iam/rolePolicyAttachment:RolePolicyAttachment` instead.

## Import-file hygiene

- **Keep `component: true` entries.** `pulumi import` needs them so children can
  resolve their `parent` through the `nameTable`; without them the import fails
  with `has no entry in 'nameTable'`. The components themselves are not imported
  as cloud resources.
- **Remove `command:local:Command` entries.** There is no physical resource to
  read.
- **Remove resources absent from the digest.** A resource that appears in the
  preview but has no digest entry (e.g. something TF declared but never applied)
  will be *created* on the first `pulumi up` rather than imported.
- **Don't let components create resources TF doesn't manage.** If a component
  creates extra resources, put them behind a flag and disable it in the migration
  program.
- **Set config that gates conditional resource creation *before* importing.** If
  the program creates resources conditionally (`isImport ? undefined : new
  RandomPassword(...)`), set the flag first so those resources never enter the
  import file. Otherwise they import from TF state and then show as deletes,
  because the program no longer creates them. If that already happened, remove
  them with `pulumi state delete --force`.

## Multi-provider setup

When TF uses multiple provider aliases (e.g. `aws` and `aws.useast2`):

1. Create an explicit `aws.Provider` per region in the program.
2. Pass providers to components (`{ providers: [provider] }`) and bare resources
   (`{ provider: provider }`).
3. Set `s3UsEast1RegionalEndpoint: "legacy"` on the `us-east-1` provider to avoid
   S3 `PermanentRedirect` errors during import.
4. **Run `pulumi up --target <provider-urns>` BEFORE `pulumi import`.** Providers
   must already exist in state; providers created during the import cause S3
   operations to fail with 301 redirects.

## Run the import

For small stacks (< ~100 resources):

```bash
pulumi import --file .import/imports-ready.json --yes --protect=false --generate-code=false
```

Both flags are required: `--protect` defaults to true, so omitting it leaves
every imported resource protected and produces a permanent `~protect` diff on
every subsequent preview; `--generate-code=false` matches the hand-authored
migration workflow, which explicitly forbids generated code.

For larger stacks, use the tool's `import` command. It batches the file, puts
**all** `component: true` entries in every batch so parent references resolve,
and — when a batch does not fully land — re-imports that batch's resources
individually to identify exactly which import IDs are bad:

```bash
# Inspect the plan first
pulumi plugin run import -- import \
  --file .import/imports-ready.json \
  --project-dir . --stack <stack-name> --dry-run

# Import (wrap in `pulumi env run <esc-env> --` if you use ESC for credentials)
pulumi plugin run import -- import \
  --file .import/imports-ready.json \
  --project-dir . --stack <stack-name> --batch-size 100
```

The run ends with a summary and, if anything failed, a table naming each failed
resource, the import ID attempted, and the error. Fix those IDs and re-run:
resources already in state are skipped automatically (`--no-resume` disables it).

Success is determined by reading stack state, not by the importer's exit status,
so neither the cosmetic `parse resource provider reference` message nor the
Automation API's spurious `failed to read generated code` error (which it returns
after a *successful* import whenever code generation is off) is mistaken for a
failure.

Then run `pulumi preview --diff` and classify what comes back — see
`diff-taxonomy.md` and `patch-state.md`.

`-tags` / `~tagsAll` diffs are expected: imported resources carry explicit `tags`
read from the cloud, while the program supplies them through `aws:defaultTags`.
Removing the explicit `tags` is a no-op because `tagsAll` still carries the same
values. To keep the initial diff small, seed `defaultTags` with the tag the old
tooling wrote (e.g. `createdBy: "TerraformCloud"`) and switch it to `"Pulumi"`
when you are ready — then call that out as the one intentional change.

## Digest caveats

- **The digest can contain embedded secrets** in *non-sensitive* TF attributes —
  a secret inside a larger string value (e.g. an API key in a CloudFormation
  `template_body`) is not redacted, because only TF-flagged sensitive attributes
  are. Always keep the digest inside the gitignored `.import/` directory.
- Data-source attributes (especially `template_body` from
  `aws_cloudformation_stack`) can be very large as well as secret-bearing.
