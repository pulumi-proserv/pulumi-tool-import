# IAM policy documents

`aws.iam.getPolicyDocumentOutput()` is the standard way to build IAM policies in
both components and programs. It is the direct replacement for Terraform's
`data "aws_iam_policy_document"` and for CDK's `PolicyStatement` grants, and it
is strongly preferred over hand-built JSON strings.

## Why it matters (especially for imports)

- **It produces canonical JSON that matches what AWS returns.** AWS normalizes
  stored policy JSON — reorders keys alphabetically, reorders statements,
  collapses single-element arrays to strings. The provider's Read function
  returns that normalized form. Policy JSON built with `JSON.stringify()`
  reflects *your* construction order, which almost certainly differs, producing a
  **permanent diff on every preview**: the program sends one ordering, the
  provider reads back another. `getPolicyDocumentOutput` emits the same canonical
  form AWS returns, so the policy round-trips with zero diff.
- It natively resolves `Input<string>` in `resources`, `identifiers`, `values`,
  and `conditions` — no `.apply()` needed.
- Its `.json` property is a typed `Output<string>` that plugs directly into
  `policy` / `assumeRolePolicy` fields on roles, policies, bucket policies, EFS
  policies, etc.
- It catches structural errors (missing required fields) at compile time.

## Basic pattern — inline in a resource

```typescript
const role = new aws.iam.Role(`${name}-role`, {
    assumeRolePolicy: aws.iam.getPolicyDocumentOutput({
        statements: [{
            actions: ["sts:AssumeRole"],
            principals: [{ type: "Service", identifiers: ["lambda.amazonaws.com"] }],
        }],
    }).json,
}, { parent: this });
```

## Dynamic values in `resources` and `identifiers`

Both accept `Input<string>`, so resource ARNs, `pulumi.interpolate` expressions,
and component outputs go in directly:

```typescript
const policy = aws.iam.getPolicyDocumentOutput({
    statements: [{
        effect: "Allow",
        actions: ["s3:GetObject"],
        resources: [bucket.arn, pulumi.interpolate`${bucket.arn}/*`],
    }],
});

new aws.iam.Policy(`${name}-policy`, { policy: policy.json }, { parent: this });
```

> When a statement needs to be built inside a resource's own args (rather than as
> a standalone `Output`), use `GetPolicyDocumentStatementArgs` — the `*Args`
> variant is the one that accepts `Output<string>` members.

## Component interfaces that accept policy statements

When a component accepts caller-defined statements (e.g. for an EFS file system
policy or a custom resource policy), model the interface to map cleanly onto
`getPolicyDocumentOutput`'s shape. Use `Input<string>` for fields callers may
populate with outputs, and export every nested interface (the remote SDK
generator rejects anonymous inline types):

```typescript
export interface PolicyPrincipal {
    type: string;
    identifiers: pulumi.Input<string>[];
}

export interface PolicyCondition {
    test: string;
    variable: string;
    values: string[];
}

export interface PolicyStatement {
    sid?: string;
    effect?: string;
    actions?: string[];
    principals?: PolicyPrincipal[];
    conditions?: PolicyCondition[];
}
```

Inside the component, map them and append any component-internal statements:

```typescript
const statements = (args.policyStatements ?? []).map((stmt) => ({
    sid: stmt.sid,
    effect: stmt.effect ?? "Allow",
    actions: stmt.actions,
    resources: [fs.arn],
    principals: (stmt.principals ?? []).map((p) => ({
        type: p.type,
        identifiers: p.identifiers,
    })),
    conditions: stmt.conditions,
}));

statements.push({
    sid: "DenyNonsecureTransport",
    effect: "Deny",
    actions: ["*"],
    resources: [fs.arn],
    principals: [{ type: "AWS", identifiers: ["*"] }],
    conditions: [{ test: "Bool", variable: "aws:SecureTransport", values: ["false"] }],
});

const policy = aws.iam.getPolicyDocumentOutput({ statements });
```

## Never build policy JSON manually

Avoid `fs.arn.apply(arn => JSON.stringify({ Statement: [...] }))` — it bypasses
type checking, doesn't resolve nested `Input` values, is harder to read, and
reintroduces the ordering diff described above. If you find yourself inside
`.apply()` assembling a policy string, refactor to `getPolicyDocumentOutput`.
