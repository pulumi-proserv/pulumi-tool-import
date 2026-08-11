---
name: cdk-to-pulumi-classic
description: Migrate a deployed AWS CDK app or CloudFormation stack into a hand-authored, component-structured Pulumi TypeScript codebase on the classic `aws` provider, importing the live resources to a zero-diff `pulumi preview` with no replacements or deletes. Covers digesting the deployed stack with `digest cfn`, per-node provider choice (classic by default, aws-native for the API Gateway family), composing import IDs with `resolve cfn`, reaching a literal zero-change preview with `patch-state cfn`, and handling custom resources and shared singletons. Use when the goal is a maintainable Pulumi codebase the team will keep and grow plus a provably non-disruptive cutover. Triggers include "migrate CDK to Pulumi with the classic provider", "hand-authored / component / zero-diff CDK migration", "import a CloudFormation stack into aws-classic". For a fast automated aws-native conversion instead, use pulumi-cdk-to-pulumi.
---

# CDK / CloudFormation → Pulumi (classic, hand-authored, zero-diff)

Migrate a **deployed** CloudFormation stack (authored by CDK or raw CFN) into a
**hand-authored, component-structured Pulumi TypeScript** program on the
**classic `aws` provider**, importing the existing live resources to a
**zero-diff `pulumi preview`** — one node at a time.

**Core principle:** the deployed stack is the source of truth. The tool
(`digest cfn` / `resolve cfn`) turns it into an agent-safe digest and composes
import IDs; you hand-author idiomatic components and drive each node to zero diff
before moving on.

## When to use this vs. the automated path

Use **this** skill when the goal is a **maintainable codebase the team keeps** and
a **provably non-disruptive** (zero-diff) cutover on the full-featured classic
provider.

Use **pulumi-cdk-to-pulumi** instead when the goal is a **fast automated
lift-and-shift** onto aws-native (flat generated code, `cdk2pulumi` bulk
convert). Do not mix the two — their provider defaults and import mechanics are
opposite.

## CRITICAL SUCCESS REQUIREMENTS

1. **Provider: classic `@pulumi/aws` by default; `@pulumi/aws-native` ONLY for
   the API Gateway family.** Classic is mature for hand-authored, day-2 code and
   imports cleanly for Lambda, IAM, S3, ECS, Secrets Manager, etc. **But classic
   explodes API Gateway** — one `AWS::ApiGateway::Method` becomes 4+ classic
   resources (method + integration + method-response + integration-response), and
   CORS/authorizer stacks multiply it (an 84-CFN-resource API → ~230 classic
   resources). For `AWS::ApiGateway::{RestApi,Resource,Method,Deployment,Stage,
   Authorizer,Integration}` use **aws-native** (1:1 with CloudFormation, imports
   by identifier). Mix providers freely in one program.

2. **Import into the LATEST `@pulumi/aws`.** Recent versions fixed the
   write-only / default-tags phantom-diff-on-import bugs (pulumi-aws #5215,
   #4030). An older version silently reintroduces a wall of phantom diffs. Pin
   `@pulumi/aws` to latest before importing.

3. **CloudFormation logical ID as the Pulumi resource name.** The child
   resource's logical-name suffix MUST equal the CFN logical ID — `resolve cfn`
   matches import entries to the digest by logical ID. Do not rename.

4. **Hand-author; do NOT ship generated code.** Import with
   `--generate-code=false`. Never fold `pulumi import` / `cdk2pulumi` generated
   flat code into the deliverable — it defeats the maintainability goal.

5. **Zero-diff, gated per node.** Work node-by-node in dependency order; each node
   must reach zero real diffs (excluding known write-only fields) BEFORE the next.
   Not a single bulk-import-then-check.

6. **Migration report** suitable for a PR: resource mapping, provider decisions,
   dropped/unmanaged resources with justification, known write-only diffs, and
   cutover steps.

## PLAN ADJUSTMENT REQUIRED

If you already produced a migration plan before loading this skill, revise it
against these requirements — especially the **aws-native-for-API-Gateway** rule
and the **`digest cfn` / `resolve cfn` tool** (a capable plan built without this
skill typically hand-authors API Gateway in classic and does the import-ID work
by hand — both are wrong here). Communicate the adjusted plan before proceeding.

## Prerequisites

- **Cloud credentials.** Run AWS-touching commands with your credential wrapper
  (e.g. `pulumi env run <esc-env> -- <cmd>` for ESC/OIDC).
- **The migration tool** — `pulumi-tool-import`
  (`pulumi plugin run import -- <cmd>`, or build with
  `go build -o bin/pulumi-tool-import .`). Provides `digest cfn`,
  `resolve cfn`, `patch-state cfn`, `set-secrets`. `patch-state cfn` needs the
  curated `aws-import-diff-fields.json` that ships with the tool.
- **`pulumi` CLI**, with the latest `@pulumi/aws` in the project.
- CDK CLI + Docker only if you need `cdk synth` — the digest reads the
  **deployed** template, so synth is optional (use it to confirm the app hasn't
  drifted from HEAD). Native build tooling (e.g. `dotnet lambda package`) can
  produce Lambda zips without Docker.

**Companion skills:** **cdk-construct-to-component** (authoring the Pulumi
ComponentResource classes from CDK constructs) and, beneath it,
**pulumi-component-authoring** (general component design, packaging, tests).

## Workflow

### Phase 0 — Posture

Confirm the classic/hybrid posture is what the customer wants (maintainable
codebase, zero-diff) rather than the automated native path. Pin the latest
`@pulumi/aws`.

### Phase 1 — Analyze (digest the deployed stack)

```bash
pulumi env run <esc-env> -- \
  pulumi plugin run import -- digest cfn \
    --stack-name <stack> --region <region> --out .import/<stack>-cfn-digest.json
```

**Read the digest, never the raw stack.** Keep it in a gitignored directory — it
holds resolved attribute values (sensitive ones are redacted to `(sensitive)`
when secret extraction runs; with `--skip-secrets` it may contain plaintext
secrets). Per resource it gives: logical ID, CFN type, Pulumi type hint, resolved
attributes, a resolved import ID for the AWS-lookup types, and two name flags
plus intrinsic markers:

- **`cdkHashedName: true`** — a settable name carrying a CDK construct hash
  (`...DefaultPolicyDFEB0894`). Route it to **stack config** (it varies per env).
- **`serverAssigned: true`** — a CloudFormation-generated name with a random
  suffix (`...ServiceRole-xQMUV6Ikl78Y`). Leave the Pulumi `name` **UNSET** —
  import preserves the computed value. Do NOT hardcode it; a hardcoded
  random-suffix name is brittle and defeats the point.
- **`<unresolved-intrinsic:Fn::GetAtt|Fn::Sub|...>`** markers — a nested intrinsic
  the digest intentionally did not resolve. Fine in informational attributes; if
  one appears in a value you need for an import identifier, resolve it yourself.

Then map **CDK context → stack config**, and infer the **component grouping from
logical-ID prefixes** — constructs flatten at synth, but the construct path
survives in the logical IDs (`<apiconstruct>…` is the API Gateway construct,
`<providerconstruct>…` the CDK Provider framework). Hand the grouping to the
**cdk-construct-to-component** skill.

### Phase 2 — Init the project

`pulumi new typescript`; set `aws:region`. Add `aws-native:region` once an API
Gateway node needs it. Start with single-language local components for a fast
edit → build → preview loop (see **cdk-construct-to-component**).

### Phase 3 — Incremental component/resource loop (the core)

For each node, in dependency order (roots → downstream):

1. Hand-author the component / bare-resource code (see
   **cdk-construct-to-component**), with the logical ID as the child name suffix.
2. Fill this node's import IDs with `resolve cfn` (Phase 4).
3. `pulumi import --file <ready>.json --generate-code=false --protect=false --yes`
   For a larger node (many resources), use the tool's `import` command instead
   — it consumes the same `<ready>.json` `resolve cfn` produces, batches it,
   and re-imports individually to isolate any resource whose ID is bad:
   `pulumi plugin run import -- import --file <ready>.json --project-dir . --stack <stack> --batch-size 100`
4. `pulumi preview --diff` targeted at this node.
5. Classify each diff and fix it; **drive to zero real diffs before the next node.**

### Phase 4 — Import mechanics

```bash
pulumi plugin run import -- resolve cfn \
  --digest .import/<stack>-cfn-digest.json \
  --import-file imports.json \
  --mapping-file mappings.yaml \
  --provider classic \        # or: native  (per node — native for the API Gateway family)
  --out imports-ready.json
```

- The import skeleton comes from `pulumi preview --import-file imports.json`
  (types and names from your hand-authored program). `resolve cfn` fills each ID
  by matching entry → digest on logical ID.
- `--generate-code=false` (hand-authored) and `--protect=false` (avoids a cosmetic
  `~protect`) are both required.
- **Per-node provider switch for API Gateway:** author that node with aws-native
  and run `resolve cfn --provider native`. It emits native identifiers, including
  the **reversed** `AWS::ApiGateway::Deployment` order
  (`DeploymentId|RestApiId`). Tame the aws-native cascade (make the RestApi a
  strict no-op: set every default it populates, plus `ignoreChanges:["tags"]`) and
  set the provider-populated integration defaults.
- Validate the import file before running — one malformed ID fails the whole
  `pulumi import` run. The failure is **partial**: the import is one deployment
  whose steps commit to state as they succeed, so a failed run typically leaves
  some resources imported and some not, and the error does not say which. Re-run
  with the `import` command (which reads state to determine what actually landed,
  and isolates the bad IDs) rather than assuming nothing was imported.

Per-type import-ID formats and the API Gateway family details:
`references/import-id-and-provider.md`.

### Phase 5 — Config & secrets

Non-secret CDK context → stack config. **Secrets are handled by `digest cfn`
itself:** pass `--pulumi-stack` + `--pulumi-project` and it discovers sensitive
inline property values (SecretsManager `SecretString`, RDS `MasterUserPassword`,
…), redacts them from the digest (→ `(sensitive)`), and stores them as encrypted
stack-config secrets. `--skip-secrets` leaves the values in the digest instead
(then be sure it is gitignored).

**NoEcho template parameters** are masked by CloudFormation and cannot be
extracted — `digest cfn` lists them so you can set them manually
(`pulumi config set --secret`). CDK often references secrets by name
(`Secret.FromSecretNameV2`), so inline secrets are frequently absent altogether.

### Phase 6 — Reach literal zero-diff (`patch-state cfn`)

After import, a clean `pulumi preview` still shows the fields the Cloud Control /
CloudFormation import cannot read back — Lambda `code` / `lastModified` /
`publish`, and input-only defaults (Secret `recoveryWindowInDays`). These are
import artifacts, not drift, and they clear on the first `up`. To reach a
**literal 0-change preview with no `up`**, patch them into state:

```bash
pulumi stack export --file state.json
pulumi plugin run import -- patch-state cfn \
  --state state.json \
  --digest .import/<stack>-cfn-digest.json \
  --fields aws-import-diff-fields.json \
  --region <region> \
  --artifacts-dir ./artifacts \      # downloaded Lambda zips land here
  --project-dir . --stack <stack> \  # optional: resolve config secrets (e.g. SecretVersion secretString)
  --out state-patched.json
pulumi stack import --file state-patched.json
pulumi preview   # 0 changes
```

- **Tier 1 (no AWS calls):** patches input-only defaults (Secret
  `recoveryWindowInDays` / `forceOverwriteReplicaSecret`, Lambda `publish`) from
  the fields file, plus any not-read value already resolved in the digest. Falsy
  defaults suppressed on new-enough providers are correctly skipped — e.g. **S3
  `Bucket.forceDestroy` needs nothing**, so buckets are zero-diff straight from
  import.
- **Tier 2 (AWS calls):** downloads each in-state Lambda's deployed zip
  (`GetFunction`) into `--artifacts-dir` and patches the `code` asset. Author each
  function's code as `FileArchive("./artifacts/<function-name>.zip")` — the path
  the tool writes to — so the imported code is zero-diff. The region comes from
  the digest per resource (a CFN stack is single-region). Only functions present
  in state are downloaded, and each patched resource is validated with the bridge
  Recover before being written.

#### Recognizing a not-read field — when to extend the tool

**The signal:** after import (+ `patch-state cfn`) a resource shows a **`+field`**
diff for a field you did NOT author — state has nothing there because the import
couldn't read it back. (A `~field` is a value mismatch = real drift or a wrong
authored value; a `-field` on an output like `lastModified` is just a downstream
consequence. The not-read signal is the bare `+field`.)

Not-read fields are **not a broad class** — they are a small, curated set of
**special fields**, each unreadable for a *specific, deliberate reason*. A plain
scalar AWS stores is returned by the import's Read and never lands here, so don't
reach for a generic "read any missing field" mechanism (Cloud Control included):
input-only defaults aren't stored anywhere, secrets are masked, and content is a
pointer — none are fetchable generically. Identify the specific reason and extend
the tool once:

1. **Input-only default** (no server-side value at all — `recoveryWindowInDays`,
   `forceDestroy`, `publish`): add the Pulumi type + field to
   `aws-import-diff-fields.json` with a `default`. Tier 1 patches it. **No code.**
2. **Security-masked value** (a secret the API won't return by design; it must be
   fetched explicitly): `digest cfn` already extracts `SecretString` /
   `MasterUserPassword` / etc. via `GetSecretValue` into encrypted stack config,
   and the companion `SecretVersion` imports from it. For a new secret-bearing
   property, add it to the tool's sensitive-property list. **No new fetch path.**
3. **Non-returnable content** (the file *bytes* — Lambda `code`, where the API
   returns a presigned download URL, not the source): a per-type fetcher
   downloads the bytes (`GetFunction`) and patches the asset. Add a small Go
   fetcher keyed by Pulumi type, mirroring the Lambda `code` fetcher.

### Phase 7 — Verify & PR

A full `pulumi preview` shows **0 changes** after `patch-state cfn` — or, if you
skip it, clean except the documented Lambda `code`/`lastModified`/`publish` and
Secret-default write-only diffs. Trace every value to a digest value or to
config. Open the PR with the migration report.

## Custom resources — replace, don't reproduce

A CDK custom resource / Provider framework usually should NOT be recreated.
**Read what the handler actually does** — it often ignores the CloudFormation
event entirely.

Canonical case (verified in a live migration): a CDK `Provider` (framework
onEvent lambda + role + policy) plus an `AWS::CloudFormation::CustomResource`
that invokes a migration lambda on deploy, where the handler ignores its event
and simply runs a database migration (and doubles as a real API handler). Migrate
it as:

- **Drop** the CustomResource and the framework's onEvent lambda/role/policy
  (unmanaged — they are cleaned up when the old CFN stack is torn down).
  `digest cfn` already skips the `AWS::CloudFormation::CustomResource`; the
  framework's lambda/role/policy appear as ordinary resources you simply don't
  recreate.
- **Keep** the real worker lambda + role (import as a normal
  `aws.lambda.Function`).
- **Add** a single `aws.lambda.Invocation` of the worker, triggered by the app
  version, so it runs on `up` / version change. (Or move it to a CI step after
  `pulumi up` if the team prefers it out of the resource graph.)

## Singleton / shared resources — leave unmanaged

Region-wide singletons and cross-stack shared resources should be left unmanaged,
not clobbered. Canonical case: `AWS::ApiGateway::Account` — region-wide, and its
`cloudWatchRoleArn` may point at a role owned by another stack. Drop it from the
program.

## Reference material

- `references/cdk-gotchas.md` — the per-resource zero-diff gotchas (ECS task-def
  expansion + env-var sorting, LogGroup ARN `:*`, Lambda write-only `code` +
  relative `FileArchive`, the API Gateway aws-native cascade / CFN tags /
  integration defaults, Lambda Permission SourceArn star-ification,
  tags/defaultTags).
- `references/import-id-and-provider.md` — per-type provider choice and import-ID
  format, the aws-native API Gateway family, extending the tool's resolver, and
  the inline-`AWS::IAM::Policy` manual-handling limitation.
- **REQUIRED companion:** the **cdk-construct-to-component** skill for authoring
  the Pulumi ComponentResource classes from CDK constructs.

## Common mistakes

| Mistake | Fix |
|---|---|
| Hand-authoring API Gateway in classic | Use **aws-native** for the API Gateway family — classic explodes it |
| Running `pulumi import` and keeping the generated code | `--generate-code=false`; hand-author idiomatic components |
| Hardcoding a `serverAssigned` random-suffix name | Leave `name` unset — import preserves the computed name |
| Bulk-importing everything, then checking the diff once | Zero-diff **gated per node**, in dependency order |
| Importing on an older `@pulumi/aws` | Pin the latest — older versions reintroduce phantom diffs |
| An absolute `FileArchive` path for Lambda code | Project-relative only — an absolute path baked into CI breaks it |
| Recreating the CDK custom-resource framework | Read the handler; usually drop it and add an `aws.lambda.Invocation` |
| Reconstructing IAM policy JSON by hand | Use `aws.iam.getPolicyDocumentOutput` — canonical JSON that round-trips zero-diff |
