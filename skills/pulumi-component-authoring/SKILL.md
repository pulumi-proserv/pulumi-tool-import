---
name: pulumi-component-authoring
description: Author, package, publish, and smoke-test Pulumi TypeScript ComponentResource classes. Covers component interface design (create-vs-pass-through supporting resources, Input types vs plain types, array input/output types, discriminated unions, runtime validation), Output lifting, IAM policy documents, package layout (PulumiPlugin.yaml, tsconfig, lockfiles), publishing via `pulumi package add`, local development against a consuming project, and pulumitest smoke tests. Use when writing a new ComponentResource, reviewing or fixing an existing one, deciding a component's argument types, packaging a component repo, or wiring component CI. Also the shared foundation for the pulumi-terraform-module-to-component and cdk-construct-to-component skills.
---

# Authoring Pulumi TypeScript Components

Reference for building a `ComponentResource` class that is correct at plan time,
pleasant to consume, and publishable as a Pulumi package.

## Design the interface before writing code

Present the interface design for approval before implementing. The decisions
below are hard to reverse once consumers exist.

### Create vs pass-through for supporting resources

For each supporting resource (security groups, IAM roles, key pairs, …), decide
whether the component should:

1. **Always create it internally** — when the resource has well-defined, stable
   property requirements that don't vary between callers. Example: a Transfer
   server's logging IAM role always has the same assume-role policy and the same
   managed policy attachment.
2. **Always accept it as input** — when the resource is commonly shared across
   components or has complex, caller-specific configuration. Example: security
   groups whose ingress rules are driven by network topology.
3. **Either/or** — accept an existing resource reference OR the params needed to
   create one, as mutually exclusive args with runtime validation. Example:
   `keyName` (use existing) vs `publicKey` (create new key pair);
   `iamInstanceProfile` (use existing) vs `managedPolicyArns` (create role +
   profile).

The deciding question: **how locked down are the resource's property
requirements?** If the component knows exactly how to configure it and callers
never customize it, create it internally. If callers need flexibility, accept it.

### `Input<T>` vs plain `T`

Default to `pulumi.Input<T>`. Use plain `T` only when the **resolved value drives
control flow that determines which resources get created**.

**Use `Input<T>`** (the default) when the value is passed through to child
resource properties — including:

- values used in `pulumi.interpolate` string building
- values passed directly to resource properties
- values used in `pulumi.jsonStringify` for policy documents
- values where only **presence** (`!== undefined`) is checked, never the
  resolved value
- arrays/maps iterated inside `.apply()` or `pulumi.jsonStringify`

**Use plain `T`** only when the resolved value drives `if` statements that decide
whether to create a resource, or `for`/`.map()` loops that decide how many
resources to create at the TypeScript level (not inside an apply):

```typescript
createBucketUser: boolean;   // drives `if (createBucketUser) { new aws.iam.User(...) }`
allowFromPublic: boolean;    // drives whether a VPC endpoint is created
```

Common mistake — these do **not** need to be plain:

```typescript
whitelist?: Input<string[]>;               // `!== undefined` works fine with Input
iamRoleNames?: Input<string[]>;            // iterated inside .apply() to build JSON
bucketName?: Input<string>;                // used in pulumi.interpolate for naming
secretData: Record<string, Input<string>>; // resolved via pulumi.output()
```

One further constraint: **fields used in a resource's logical name must be plain
`string`.** See "Logical names" below.

### Array input types

**`Input<string>[]`** — when the array length drives resource creation (e.g. one
EIP per subnet). The length must be known at plan time, because resources created
inside `.apply()` are hidden from preview. Callers build the array from
individual `Output<string>` values:

```typescript
// Component interface
subnetIds: pulumi.Input<string>[];

// Caller
new TransferServer("sftp", {
    subnetIds: [vpc.publicSubnetPrimary, vpc.publicSubnetSecondary],
});
```

**`Input<Input<string>[]>`** — the most flexible type, accepting the widest range
of caller values but providing the least information to surrounding code. You
cannot know the length, `.map()`, or index into it without `.apply()`. Use it
only for arrays passed straight through to a child resource property.

All of these satisfy `Input<Input<string>[]>`:

```typescript
subnetIds: ["subnet-aaa", "subnet-bbb"]              // string[]
subnetIds: [subnet1.id, subnet2.id]                  // Output<string>[]
subnetIds: [subnet1.id, "subnet-bbb"]                // Input<string>[]
subnetIds: config.requireObject<string[]>("subnets") // string[] from config
subnetIds: someRemoteComponent.subnetIds             // Output<string[]>
```

Only the last form *requires* `Input<Input<string>[]>` over the simpler
`Input<string>[]`. Its main real-world source is the SDK serialization quirk
described under "Array outputs" — normal in-process code produces `string[]` or
`Output<string>[]`, both of which satisfy the simpler type.

**Avoid creating resources inside `.apply()`** — they are hidden from preview and
cannot be planned deterministically.

### Array outputs

Array outputs — `Output<string[]>` on regular resources, or `Output<string>[]` in
component source, which becomes `Output<string[]>` in the generated SDK — hide the
array length inside the Output. Consumers cannot `.map()` over them to create
resources without `.apply()`.

**When a fixed-size array drives resource creation in consumers, expose
individual named outputs instead:**

```typescript
// Problematic — consumers can't iterate to create resources without .apply()
public readonly privateSubnets: pulumi.Output<string>[];

// Solution — each is Output<string>; consumers build arrays directly
public readonly privateSubnetPrimary: pulumi.Output<string>;
public readonly privateSubnetSecondary: pulumi.Output<string>;
public readonly privateSubnetTertiary: pulumi.Output<string>;
```

For arrays only passed through to child resource properties, `Output<string[]>`
is fine — the consuming component accepts `Input<Input<string>[]>` and forwards it.

### Runtime validation for conditional dependencies

When one arg requires another, validate in the constructor:

```typescript
if (!allowFromPublic && args.netConfig === undefined) {
    throw new Error("netConfig is required when allowFromPublic is false");
}
```

### Discriminated unions for mutually exclusive params

When two params are mutually exclusive *and imply different resource shapes*, a
discriminated union documents the intent at the type level:

```typescript
interface FullAccessUser { hasFullAccess: true; }
interface RestrictedUser { hasFullAccess: false; allowedPaths: string[]; }
type SftpUserDefinition = FullAccessUser | RestrictedUser;
```

**Discriminated unions do NOT flow through to the generated SDK.** The SDK
flattens all fields into one interface with everything optional. Always add
runtime validation in the constructor so SDK callers get the same guarantees.

For simpler either/or cases (pass existing vs create new), two optional params
plus validation is cleaner than a union:

```typescript
interface Ec2InstanceArgs {
    keyName?: Input<string>;   // use existing
    publicKey?: Input<string>; // create new — mutually exclusive with keyName
}

if (args.keyName !== undefined && args.publicKey !== undefined) {
    throw new Error(
        "keyName (use an existing key pair) and publicKey (create a new key pair " +
        "resource from a public key string) are mutually exclusive"
    );
}
```

Write error messages that explain what each param does, not just that they clash.

## Writing the component

### Prefer Pulumi helpers over manual `.apply()`

- **`pulumi.interpolate`** instead of `.apply(v => ...)` for string building
- **`pulumi.jsonStringify`** instead of `.apply(v => JSON.stringify(v))` — it
  deep-resolves nested Input values
- **`pulumi.output(obj)`** to deep-resolve an object/array of Inputs
- **`aws.iam.getPolicyDocumentOutput`** for IAM policy documents — see
  `references/iam-policy-documents.md`

### Output lifting — avoid unnecessary `.apply()`

Accessing a property on an `Output<{foo: string}>` returns `Output<string>`
without `.apply()`, and this nests:

```typescript
// Bad
const targetDomain = apiGwDomain.domainNameConfiguration.apply(c => c.targetDomainName);

// Good
const targetDomain = apiGwDomain.domainNameConfiguration.targetDomainName;

// Bad — .apply() just to append a string
values: [alb.dnsName.apply(d => `${d}.`)]

// Good
values: [pulumi.interpolate`${alb.dnsName}.`]
```

`.apply()` is still needed for bracket indexing into a record
(`stack.outputs["key"]` doesn't lift — TypeScript can't type-narrow dynamic
string keys on Output) and for transformations beyond property access or string
interpolation.

### Provider functions need `{ parent: this }` too

Provider functions (`aws.getRegion`, `aws.getCallerIdentity`,
`aws.iam.getPolicyDocument`, …) take options just like resources. Without
`{ parent: this }` they use the **default** provider instead of inheriting the
component's:

```typescript
// Bad — default provider, possibly wrong region/account
const region = aws.getRegion({});

// Good
const region = aws.getRegion({}, { parent: this });
```

This bites when the component runs under an explicit provider (e.g. a
cross-region `us-east-1` provider while the default targets another region): the
function returns data for the wrong region, producing diffs like a
`kms:ViaService` condition naming the wrong region.

### Logical names

The first argument to every `new aws.*.*()` call is the **logical name**. It must
be a plain string — never an `Output`/`Input`:

```typescript
// Bad — args.bucketName is Input<string>
const bucket = new aws.s3.Bucket(`${name}-${args.bucketName}`, { ... }, { parent: this });
```

Build it from the component's `name` parameter plus a stable suffix. Design the
args interface accordingly: keep naming fields plain `string`, keep value fields
`Input<T>`.

**When the component's resources will be imported from existing infrastructure,
the suffix is not free** — it must match the source system's resource identifier
so import tooling can pair the two up. See the
**pulumi-terraform-module-to-component** and **cdk-construct-to-component**
skills for the respective rules.

### Component type URN

The first argument to `super()` is `<package>:<module>:<type>`:

| Segment | Convention | Example |
|---------|-----------|---------|
| package | short package name, without the `pulumi-` prefix | `data-components`, `network-components` |
| module  | always `index` for single-module packages | `index` |
| type    | PascalCase class name | `Vpc`, `AuroraCluster` |

When consumed via an SDK generated by `pulumi package add`, the SDK rewrites the
package segment to match the `Pulumi.yaml` packages key (e.g.
`pulumi-data-components:index:SsmParameters`). The URN in source is what's used
for **direct** consumption (local `file:` deps in tests), so keep it matching the
short package name.

### Class skeleton

```typescript
import * as pulumi from "@pulumi/pulumi";
import * as aws from "@pulumi/aws";

export interface MyComponentArgs {
    propertyName: pulumi.Input<string>;
    optionalProp?: pulumi.Input<number>;
}

export class MyComponent extends pulumi.ComponentResource {
    public readonly outputName: pulumi.Output<string>;

    constructor(name: string, args: MyComponentArgs, opts?: pulumi.ComponentResourceOptions) {
        super("<package-short-name>:index:MyComponent", name, {}, opts);

        const resource = new aws.s3.Bucket(`${name}-<suffix>`, {
            // ...
        }, { parent: this });

        this.outputName = resource.arn;

        this.registerOutputs({
            outputName: this.outputName,
        });
    }
}
```

Re-export from `index.ts` and add the file to `tsconfig.json`'s `files` array.

### Use non-deprecated AWS resources

| Deprecated | Use instead |
|---|---|
| `aws.s3.BucketV2` | `aws.s3.Bucket` |
| `aws.s3.BucketAclV2` | `aws.s3.BucketAcl` |
| `aws.s3.BucketServerSideEncryptionConfigurationV2` | `aws.s3.BucketServerSideEncryptionConfiguration` |
| `aws.s3.BucketObjectv2` | `aws.s3.BucketObject` |

**S3 ACL ordering:** new buckets disable ACLs by default, so
`BucketOwnershipControls` must exist before `BucketAcl` — wire it with `dependsOn`:

```typescript
const ownership = new aws.s3.BucketOwnershipControls(`${name}-ownership`, {
    bucket: bucket.id,
    rule: { objectOwnership: "BucketOwnerPreferred" },
}, { parent: this });

new aws.s3.BucketAcl(`${name}-acl`, {
    bucket: bucket.id,
    acl: "private",
}, { parent: this, dependsOn: [ownership] });
```

## Reference material

- `references/iam-policy-documents.md` — `getPolicyDocumentOutput`, why it is
  required for zero-diff policies, and how to model caller-supplied statements in
  a component interface.
- `references/packaging-and-publishing.md` — `PulumiPlugin.yaml`, `package.json`,
  `tsconfig.json`, lockfile hygiene, publishing via `pulumi package add`, the
  local-development loop against a consuming project, and CI workflows.
- `references/smoke-tests.md` — component-tests project layout, writing test
  instances, and pulumitest assertions.

## Troubleshooting

| Issue | Solution |
|-------|----------|
| `Unsupported type for component property` | The remote SDK generator cannot serialize anonymous inline object types. Extract every nested object into a named exported interface and export it from `index.ts`. |
| `ERR_UNSUPPORTED_NODE_MODULES_TYPE_STRIPPING` | Package `main` points at a `.ts` file in a context that needs compiled output — point it at the built `.js` in `bin/`. |
| `AccessControlListNotSupported` on S3 | `BucketOwnershipControls` must be created before `BucketAcl` — use `dependsOn`. |
| Subpath import fails | Import from the package root, not a subpath. Re-export from `index.ts`. |
| Deprecated-resource warnings | Use the non-`V2` names (table above). |
| Idempotency smoke test fails with a plugin-download 403 | Reuse the same `PulumiProgram` instance for the preview — see `references/smoke-tests.md`. |
