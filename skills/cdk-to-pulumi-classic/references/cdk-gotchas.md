# CDK → Pulumi zero-diff gotchas

Per-resource issues that cause a non-zero `pulumi preview` after import, and how
to fix each. Grouped by resource type. These are the diffs you will actually
chase.

Examples below use `<prefix>` for the program's component/resource name prefix
and `<logicalId>` for the CloudFormation logical ID.

## Names

- **CDK construct-hash names → stack config.** Inline-policy names
  (`...DefaultPolicyDFEB0894`), ECS task-def `Family` (`...task6FEE99BC`), and
  authorizer names (`...tokenauthorizer97C609E5`) carry a construct-path hash and
  vary per environment. The digest flags these `cdkHashedName`. Put them in stack
  config and reference from config — don't hardcode.
- **Server-assigned names → leave unset.** IAM roles/policies CDK left unnamed
  get CFN-generated names with random suffixes (`...ServiceRole-xQMUV6Ikl78Y`),
  flagged `serverAssigned`. Leave the Pulumi `name` unset — import preserves the
  computed name → zero diff. Hardcoding the random-suffix value is brittle and
  wrong.

## IAM

- **Policy documents: use `aws.iam.getPolicyDocumentOutput`.** AWS normalizes
  stored policy JSON (sorts keys, reorders statements, collapses single-element
  arrays). Hand-built `JSON.stringify` policy JSON round-trips to a permanent
  diff. `getPolicyDocumentOutput().json` produces the same canonical form AWS
  returns → zero diff. This also matches what CDK's `grant*` helpers produced.
- **Inline `AWS::IAM::Policy` is a manual case.** It maps to
  `aws:iam/rolePolicy:RolePolicy` (`role:policy-name` import ID via
  `list-role-policies`) and can bind multiple principals — `resolve cfn` does NOT
  auto-resolve it. Author it by hand and set the import ID manually. Managed
  policies (`AWS::IAM::ManagedPolicy`) are fine — the physical ID is the ARN.

## Lambda

- **`code` is a write-only diff on import.** `code` can't be read back, so every
  imported Lambda shows `+ code / - lastModified / + publish` — not real drift. It
  clears on the first `up` (which redeploys identical source), or you can
  eliminate it without an `up` via **`patch-state cfn`**, which downloads each
  deployed zip (`GetFunction`) and patches the `code` asset to a file-based
  `FileArchive` sentinel matching the program (skill Phase 6). Author the code as
  `FileArchive("./artifacts/<function-name>.zip")` so the patched path matches.
- **Reference the artifact by a project-RELATIVE `FileArchive` path.** Never
  `path.resolve` / `__dirname` / absolute — an absolute path baked into a CI
  workflow breaks the build. Build the zips into a gitignored relative
  `artifacts/` directory.
- **Lambda Permission SourceArn: star-ify path params.** The execute-api
  SourceArn for a parameterized route is `.../GET/info/user/*`, not
  `.../info/user/{userId}`. Emitting `{userId}` causes the permission to replace.
- **Lambda Permission `function`: use `fn.name`.** CDK sets
  `AWS::Lambda::Permission.FunctionName` from `Fn::GetAtt Function.Arn`, so the
  raw attribute is the full ARN. `resolve cfn` normalizes this to the bare
  function name, emitting the import ID as `<function-name>/<statement-id>`, so
  the imported state stores the bare name — author `function: fn.name`
  (idiomatic) for zero diff. (`function` is ForceNew; a mismatch replaces. If you
  hand-build an ARN-based import ID instead of using the tool, author `fn.arn` to
  match it.)
- **Lambda Permission `statementId`: leave unset for short names, set explicitly
  for long CDK names.** Unset preserves the imported Sid — but only if the
  provider can auto-name it. API-Gateway-generated permissions carry very long
  CDK logical IDs (`...ApiPermissionTest<Stack>...PUTmigrate3AE1F269`); with
  `statementId` unset the provider derives the physical Sid from the resource name
  and fails at preview: *"name '…-' plus 7 random chars is longer than maximum
  length 100"*. For these, read the deployed Sid (`aws lambda get-policy`, match
  by SourceArn) and set `statementId` to it verbatim — that both fixes the length
  error and is zero-diff.

## ECS TaskDefinition (finicky — both of these force a replace if wrong)

- **Container env vars are stored name-sorted** by the provider, even though
  `describe-task-definition` returns registration order. Emit them sorted.
- **Emit the fully-expanded container definition.** The provider stores all AWS
  default empty arrays/fields (`command:[]`, `mountPoints:[]`, `secretOptions:[]`,
  `cpu:0`, …). A minimal definition diffs against the expanded state.

## CloudWatch / Logs

- **LogGroup ARN `:*` suffix.** CloudFormation's `Fn::GetAtt LogGroup.Arn`
  includes a trailing `:*`; Pulumi's `logGroup.arn` omits it. IAM policies
  referencing the log group ARN must append `:*` to match.

## API Gateway (aws-native family)

Use aws-native for the whole family. Its zero-diff gauntlet:

- **RestApi update → cascade replace.** Any update to the aws-native RestApi
  marks `rootResourceId` `[unknown]` at preview; every child `parentId` goes
  unknown → forced replace, cascading down the tree. Fix: make the RestApi a
  strict no-op — set every default it populates (`apiKeySourceType`,
  `disableExecuteApiEndpoint`, `endpointConfiguration`, `securityPolicy`) plus
  `ignoreChanges:["tags"]`.
- **CFN-injected tags.** Cloud Control import reads
  `aws:cloudformation:logical-id/stack-id/stack-name` tags on taggable resources.
  Don't reproduce them — `ignoreChanges:["tags"]` on RestApi and Stage.
- **Import-ID identifier order varies by type.** Resource/Method/Stage use
  `RestApiId|...` first; **Deployment is reversed** (`DeploymentId|RestApiId`).
  `resolve cfn --provider native` handles this.
- **"Resource does not exist" on import = wrong identifier order,** not a missing
  resource. Check the format.
- **Never wire aws-native API Gateway resources by `.id` — it is the composite
  Cloud Control identifier.** Every aws-native apigateway resource's `.id` output
  is `RestApiId|...` (or `DeploymentId|RestApiId`), NOT the bare id the child
  field expects. Wiring a child from a parent's `.id` writes the composite into a
  raw-id field → the field diffs and (for id fields) forces a replace that
  cascades down the tree:
  - `Stage.deploymentId` ← `deployment.deploymentId` (not `deployment.id`)
  - `Resource.parentId` ← `parentResource.resourceId` (not `parentResource.id`)
  - `Method.resourceId` ← `resource.resourceId` (not `resource.id`)

  Symptom: `~ resourceId: "aeeubm" => "qitdtkyzlk|aeeubm"`. Always reference the
  raw-id output (`.resourceId`, `.deploymentId`), or hardcode the bare deployed id.
- **RestApi strict-no-op fields (verified on a live import):** set
  `securityPolicy` (e.g. `"TLS_1_0"`), `apiKeySourceType` (e.g. `"HEADER"`),
  `disableExecuteApiEndpoint: false`, and
  `endpointConfiguration: { types: [...], ipAddressType: "ipv4" }` to their
  deployed values — a missing `ipAddressType` / `securityPolicy` shows a diff and,
  via the cascade, can force a replace of the whole tree.
- **Integration defaults must be set to match:** `cacheNamespace` (= the resource
  id), `passthroughBehavior:"WHEN_NO_MATCH"`, `responseTransferMode:"BUFFERED"`,
  `timeoutInMillis:29000`; proxy methods also need `apiKeyRequired:false`. Stage
  needs `cacheClusterEnabled:false` plus methodSettings cache/throttling defaults.
  Authorizer needs `authType:"custom"` and `providerArns:[]`. (`patch-state` and
  `aws-import-diff-fields.json` carry these.)
- **CORS preflight (CDK `DefaultCorsPreflightOptions`)** auto-adds OPTIONS methods
  and MOCK integrations with specific request/response templates on every
  resource. Pull the exact deployed templates (`aws apigateway get-integration` /
  `get-integration-response`) and replicate them verbatim.

## Tags

- **`tags` → `tagsAll` via `defaultTags`.** Imported resources have explicit
  `tags`; a program using `aws:defaultTags` shows `-tags` / `~tagsAll`, which is a
  no-op (defaultTags supplies the same tags). To minimize the initial diff, seed
  `defaultTags` with the CDK tag (`createdBy: "CDK"`), then switch to `"Pulumi"`
  when ready — and call that out as the one intentional diff.

## S3

- **Use the latest, non-`v2` S3 resources.** Prefer the plain resource names over
  the `V2`/`v2` variants: `aws.s3.Bucket` (`aws.s3.BucketV2` is **deprecated** —
  the SDK flags it "deprecated in favor of s3.Bucket") and `aws.s3.BucketObject`
  (not `BucketObjectv2`). The tool's `aws-import-diff-fields.json` is keyed to
  these (`aws:s3/bucket:Bucket`, `aws:s3/bucketObject:BucketObject`), so
  authoring the `v2` types would also miss `patch-state`'s not-read fields.
- **Match the deployed bucket settings explicitly.** CDK's
  `BucketEncryption.S3_MANAGED` implies specific
  `ServerSideEncryptionConfiguration`, `PublicAccessBlock`, and `ObjectOwnership`
  settings. Read them (`aws s3api get-bucket-encryption` /
  `get-public-access-block` / `get-bucket-ownership-controls`) and set them
  explicitly — classic `aws.s3.Bucket` defaults differ. Use `aws.s3.Bucket` plus
  the separate sub-resources (`BucketServerSideEncryptionConfiguration`,
  `BucketPublicAccessBlock`, `BucketOwnershipControls`).
- **Give the sub-resources UNIQUE Pulumi names.** The bucket and its three
  sub-resources all map to the one CFN `AWS::S3::Bucket` logical ID, so it is
  tempting to name them all `<prefix>-<logicalId>`. Don't: when several resources
  share a name, `pulumi preview --import-file` disambiguates by appending the type
  to the generated name (`<prefix>-<logicalId>BucketPublicAccessBlock`), which
  breaks `resolve cfn`'s logical-ID-suffix match (`no digest match for …`). Name
  them `<prefix>-<logicalId>` / `<prefix>-sse-<logicalId>` /
  `<prefix>-pab-<logicalId>` / `<prefix>-own-<logicalId>` — distinct, and each
  still ends in the logical ID so the suffix match holds.

## Secrets Manager

- **The secret's version is a real resource but is NOT in the CFN digest — the
  tool handles it.** `AWS::SecretsManager::Secret` creates its initial version
  implicitly (via `GenerateSecretString` / `SecretString`), so CloudFormation
  lists only the secret, not a version. `digest cfn` enriches each owned secret
  from live AWS: it (a) records `secretVersionImportId` =
  `<arn>|<current-version-id>`, and (b) replaces the digest's `SecretString` with
  the **live current value** — which also covers `GenerateSecretString` secrets
  that have no template value at all. You still author the
  `aws.secretsmanager.SecretVersion` (name it `<prefix>-sv-<logicalId>`, distinct
  from the secret per the S3 unique-name rule, referencing the extracted config
  secret); `resolve cfn` then auto-fills its import ID from
  `secretVersionImportId`. No manual `list-secret-version-ids` / pre-fill needed.
- **Why live, not template:** the CFN template value drifts from the live current
  version after any rotation, which would make the imported `SecretVersion`
  replace (`~ secretString: [secret] => [secret]`). Live enrichment matches what
  import reads back. (Enrichment needs AWS access at digest time; without it, fall
  back to `get-secret-value --version-stage AWSCURRENT` into config plus a
  hand-built `<arn>|<version-id>` pre-fill.)
- **`recoveryWindowInDays` / `forceOverwriteReplicaSecret` show as an add on
  import** (`+ recoveryWindowInDays: 30`). They are input-only fields with no
  server-side value to read back, so they surface on the post-import preview and
  clear on the first `up` — the same class as the Lambda `code` write-only diff,
  not real drift.

## Data-source lookups (not managed resources)

- **`Vpc.FromLookup` / `SecurityGroup.FromLookupById` / `Secret.FromSecretNameV2`**
  are read-only in CDK — mirror them with `aws.ec2.getVpcOutput` /
  `aws.ec2.getSecurityGroupOutput` / `aws.secretsmanager.getSecretOutput`, never
  import them as managed resources. Only stack-OWNED resources are in the digest
  to import.
