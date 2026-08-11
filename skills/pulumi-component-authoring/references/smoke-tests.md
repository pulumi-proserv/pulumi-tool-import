# Component smoke tests

Smoke tests deploy the component against real cloud infrastructure to verify
resource creation and idempotency. Three parts:

1. **A component-tests Pulumi project** in `component-tests/` — a standalone
   program that consumes the component via a locally generated SDK.
2. **TypeScript test instances** in `component-tests/index.ts` — component
   instances exercising each creation path, exporting outputs.
3. **Python assertions** in `tests/test_smoke.py` — deploy via pulumitest,
   assert on outputs, verify idempotency.

## Project layout

```
component-tests/
  Pulumi.yaml          # references the parent package by relative path
  package.json         # file: dep on the generated SDK
  tsconfig.json
  index.ts             # test instances
  sdks/                # generated SDK (gitignored)
```

**`Pulumi.yaml`:**

```yaml
name: component-tests
description: A minimal TypeScript Pulumi program
runtime: nodejs
config:
  pulumi:tags:
    value:
      pulumi:template: typescript
packages:
  pulumi-my-components: ../
```

**`package.json`** — `@pulumi/aws` is *not* a direct dependency; it arrives
transitively through the generated SDK:

```json
{
    "name": "consumer",
    "main": "index.ts",
    "devDependencies": {
        "@types/node": "^18",
        "typescript": "^5.0.0"
    },
    "dependencies": {
        "@pulumi/pulumi": "^3.113.0",
        "@pulumi/pulumi-my-components": "file:sdks/pulumi-my-components"
    }
}
```

**`tsconfig.json`:**

```json
{
    "compilerOptions": {
        "strict": true,
        "outDir": "bin",
        "target": "es2020",
        "module": "nodenext",
        "moduleResolution": "nodenext",
        "sourceMap": true,
        "experimentalDecorators": true,
        "pretty": true,
        "noFallthroughCasesInSwitch": true,
        "noImplicitReturns": true,
        "forceConsistentCasingInFileNames": true
    },
    "files": ["index.ts"]
}
```

Run `pulumi install` in `component-tests/` to generate the SDK before first use.

## Writing test instances (`component-tests/index.ts`)

1. **Exercise every creation path.** If the component has either/or params
   (create vs pass-through), write an instance for each path plus the default.
2. **Use valid resource inputs.** The cloud provider validates at creation time —
   fake SSH keys, invalid ARNs, and placeholder account IDs all fail. Generate
   real test keys with `ssh-keygen`, use real managed-policy ARNs, and get the
   account ID from `aws.getCallerIdentityOutput()`.
3. **Export outputs for assertion.** Every instance exports its key outputs, and
   the exported names must stay in sync with the names the Python tests assert on.
4. **Reuse shared infrastructure.** If several instances need a VPC/subnet/SG,
   look up the default VPC once at the top of the file and share it.

```typescript
// Test 1: Minimal — exercises default behavior
const minimal = new MyComponent("test-minimal", { name: "test-minimal" });
export const minimalId = minimal.id;

// Test 2: With optional params — exercises the creation path
const withOptions = new MyComponent("test-options", {
    name: "test-options",
    publicKey: "ssh-ed25519 AAAA...",
    managedPolicyArns: ["arn:aws:iam::aws:policy/..."],
});
export const withOptionsKeyPairName = withOptions.keyPairName;

// Test 3: Passthrough — exercises the existing-resource path
const passthrough = new MyComponent("test-passthrough", {
    name: "test-passthrough",
    keyName: externalKeyPair.keyName,         // Output<string> → Input<string>
    iamInstanceProfile: externalProfile.name, // Output<string> → Input<string>
});
export const passthroughId = passthrough.id;
```

## Test dependencies (`tests/requirements.txt`)

```
pulumitest @ git+https://github.com/pulumi-labs/pulumitest-python.git@main
pytest>=8.0
```

`pulumitest` is not on PyPI — install it from the GitHub repo.

## Assertions (`tests/test_smoke.py`)

A module-scoped `program` fixture deploys the stack once; individual tests assert
on outputs. The idempotency test calls `preview()` on that **same** program
instance:

```python
@pytest.fixture(scope="module")
def program(request: pytest.FixtureRequest) -> PulumiProgram:
    prog = PulumiProgram(COMPONENT_TESTS_DIR, ...)
    prog.up()
    return prog

@pytest.fixture(scope="module")
def outputs(program: PulumiProgram) -> dict:
    return program.current_stack.outputs()

def test_my_component(outputs: dict) -> None:
    assert "myOutput" in outputs
    assert outputs["myOutput"].value == "expected"

def test_idempotency(program: PulumiProgram) -> None:
    preview_result = program.preview()
    preview_result.has_no_changes()
```

Patterns:

- Assert the output **exists** in the dict first, then assert its **value**.
- For ARNs, assert the service segment is present (e.g. `":s3:::"`, `":aoss:"`).
- For counts, assert the exact number implied by the test input data.
- **Do not construct a new `PulumiProgram` for the idempotency preview** — a new
  instance re-triggers plugin downloads and fails (commonly with a 403).
