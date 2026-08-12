---
name: cdk-construct-to-component
description: Turn the constructs of a deployed AWS CDK app into hand-authored Pulumi TypeScript ComponentResource classes that import the existing resources to a zero-diff preview. Covers recovering the construct grouping from CloudFormation logical-ID prefixes in the `digest cfn` output, mapping construct props to component args, the logical-ID naming rule that `resolve cfn` depends on, translating CDK idioms (grants, `from*` lookups, implicit L2 defaults, Duration/Size, removal policies, assets, custom resources) into Pulumi, and mixing the classic and aws-native providers inside one program. Use when authoring components during a CDK or CloudFormation migration, or when deciding how to group migrated resources into components. Companion to the cdk-to-pulumi-classic skill; builds on pulumi-component-authoring.
---

# CDK construct → Pulumi component

Authoring the Pulumi `ComponentResource` classes for a CDK / CloudFormation
migration, such that each component's children **import cleanly to zero diff**.

**Load these too:**

- **cdk-to-pulumi-classic** — the overall migration workflow, the digest, import
  mechanics, and `patch-state cfn`. This skill covers only the component
  authoring inside that workflow.
- **pulumi-component-authoring** — general component design (`Input<T>` vs plain
  `T`, array types, runtime validation), Output lifting, IAM policy documents,
  packaging, publishing, and smoke tests. Everything here assumes it.

## The central constraint

Unlike greenfield component work, **you do not get to choose the child resource
names.** Each child's logical-name suffix MUST equal the CloudFormation logical
ID of the resource it imports, because `resolve cfn` matches import entries to
digest entries by that suffix.

```typescript
// CFN logical ID: ApiHandlerServiceRole9F2C1A3B
new aws.iam.Role(`${name}-ApiHandlerServiceRole9F2C1A3B`, { ... }, { parent: this });
```

Design the component interface around that constraint, not the other way around.

## Phase 1 — Recover the construct grouping

CDK constructs flatten at synth time, but **the construct path survives inside
the logical IDs**. Group the digest's resources by logical-ID prefix and each
group is (approximately) one construct — which is the natural component boundary:

```
<apiconstruct>RestApi…, <apiconstruct>Resource…, <apiconstruct>Method…  → an API component
<queueconstruct>Queue…, <queueconstruct>Policy…                          → a queue component
<providerconstruct>framework-onEvent…                                    → CDK Provider framework (usually dropped)
```

Sanity-check the grouping against the CDK source if you have it, but treat the
**deployed** stack as the source of truth — the app may have drifted from HEAD.

Not every group deserves a component. Keep as bare program resources: one-off
resources with no reuse, and anything you are deliberately leaving unmanaged
(region-wide singletons, cross-stack shared resources — see the
cdk-to-pulumi-classic skill).

## Phase 2 — Design the interface

CDK construct props are the starting point for the component args, but do not
copy them mechanically:

- **Only include props the app actually varies.** A CDK construct may accept
  dozens of props while the app sets five. Check the deployed values in the
  digest — everything else is a hardcoded internal detail of the component.
- **CDK context values (per-environment) become stack config**, passed into the
  component as args — not read from config inside the component.
- **Values flagged `cdkHashedName` in the digest** carry a construct-path hash and
  differ per environment. They must be settable args sourced from stack config —
  never hardcoded, and never regenerated (there is no hash to recompute).
- **Values flagged `serverAssigned`** must NOT be settable at all. Leave the
  underlying resource's `name` unset so import preserves the CFN-generated
  random-suffix name.
- **Naming args must be plain `string`**, not `Input<string>`, wherever they feed
  a resource's logical name (see pulumi-component-authoring).

## Phase 3 — Child naming rules

1. **Suffix = CFN logical ID**, exactly. Do not rename, shorten, or prettify.
2. **When several Pulumi resources map to one CFN logical ID, keep every name
   distinct while still ending in the logical ID.** A classic `aws.s3.Bucket`
   plus its `BucketServerSideEncryptionConfiguration`,
   `BucketPublicAccessBlock`, and `BucketOwnershipControls` all correspond to one
   `AWS::S3::Bucket`. Naming them identically makes
   `pulumi preview --import-file` disambiguate by appending the type, which
   breaks the suffix match. Use distinct prefixes instead:

   ```typescript
   new aws.s3.Bucket(`${name}-${id}`, ...)
   new aws.s3.BucketServerSideEncryptionConfiguration(`${name}-sse-${id}`, ...)
   new aws.s3.BucketPublicAccessBlock(`${name}-pab-${id}`, ...)
   new aws.s3.BucketOwnershipControls(`${name}-own-${id}`, ...)
   ```

3. **Resources with no CFN counterpart** (things you are adding, like an
   `aws.lambda.Invocation` replacing a custom resource) can use any descriptive
   suffix — they are created, not imported.

## Phase 4 — Translating CDK idioms

| CDK idiom | Pulumi equivalent | Notes |
|---|---|---|
| `resource.grantRead(role)` and friends | `aws.iam.getPolicyDocumentOutput()` → `aws.iam.RolePolicy` / `Policy` | Grants synthesize to inline or managed policies; check the digest for which. `getPolicyDocumentOutput` reproduces AWS's canonical JSON — hand-built JSON round-trips to a permanent diff |
| `Vpc.fromLookup`, `SecurityGroup.fromLookupById`, `Secret.fromSecretNameV2` | `aws.ec2.getVpcOutput`, `aws.ec2.getSecurityGroupOutput`, `aws.secretsmanager.getSecretOutput` | Read-only in CDK; never import these as managed resources — they aren't in the digest |
| `Duration.seconds(30)` / `Size.mebibytes(512)` | plain numbers in the units the resource expects | e.g. Lambda `timeout: 30`, `memorySize: 512` |
| `RemovalPolicy.RETAIN` | `retainOnDelete: true` in the resource options | `protect: true` is a different guarantee (blocks deletion via Pulumi) — choose deliberately |
| `lambda.Code.fromAsset(...)` | `new pulumi.asset.FileArchive("./artifacts/<fn>.zip")` | **Project-relative path only.** Matches what `patch-state cfn` writes — see the cdk-to-pulumi-classic skill |
| L2 construct implicit defaults | Explicit properties on the Pulumi resource | See below |
| `Provider` framework + `CustomResource` | Usually dropped; add an `aws.lambda.Invocation` for the real work | See the custom-resources section of cdk-to-pulumi-classic |
| Nested stacks | Separate components (or separate Pulumi stacks) | Each nested stack is its own CFN stack — digest it separately |

### Implicit L2 defaults are the main source of diffs

A CDK L2 construct silently sets properties that the classic Pulumi resource
either defaults differently or does not default at all. `BucketEncryption.S3_MANAGED`,
for example, implies a specific server-side encryption configuration, public
access block, and object ownership setting.

**Do not guess these.** Read the deployed values — from the digest first, and
from the cloud API where the digest doesn't carry them (`aws s3api
get-bucket-encryption`, `get-public-access-block`, …) — and set them explicitly.
`references/cdk-gotchas.md` in the cdk-to-pulumi-classic skill lists the ones
that bite in practice per resource type.

## Phase 5 — Mixed providers inside a program

The API Gateway family is authored with `@pulumi/aws-native`; everything else with
classic `@pulumi/aws`. That split lands at the **component** boundary: an API
component imports aws-native resources, its neighbours import classic ones, and
both live in the same program.

- Set both `aws:region` and `aws-native:region`.
- A component that needs an explicit provider takes it through
  `ComponentResourceOptions` (`{ providers: [...] }`) and passes `{ parent: this }`
  on children so they inherit it — including provider *functions*
  (`aws.getRegion({}, { parent: this })`).
- Never wire aws-native API Gateway children from a parent's `.id` — it is the
  composite Cloud Control identifier. Use the raw-id output (`.resourceId`,
  `.deploymentId`). This is the single most common cause of a cascading replace;
  the details are in `references/cdk-gotchas.md` of the cdk-to-pulumi-classic
  skill.

## Phase 6 — Verify per component

A component is done when its own targeted preview is clean:

```bash
pulumi preview --diff --target <component-urn> --target-dependents
```

Zero real diffs — excluding only the documented write-only classes (Lambda
`code`/`lastModified`/`publish`, Secret input-only defaults) if you have not yet
run `patch-state cfn`. Do not start the next component before this one is clean:
diffs compound, and a wrong shared value (a policy document, a role ARN) is far
cheaper to find against one component than against thirty.

Trace every value in the component to a digest value or to a config value. A
value you cannot trace is a value you guessed.
