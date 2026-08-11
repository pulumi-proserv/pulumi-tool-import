# Packaging, publishing, and local development

## Contents

- [Choosing a component approach](#choosing-a-component-approach)
- [Package layout](#package-layout)
- [Publishing](#publishing)
- [Developing a component against a consuming project](#developing-a-component-against-a-consuming-project)
- [CI workflows](#ci-workflows)

## Choosing a component approach

**Option A — single-language local components (simpler iteration).**
TypeScript classes in a local package, referenced via `file:` in `package.json`.
No plugin install, SDK generation, or version bumps — edit the code and
`npm install`.

```
components/           # local package with ComponentResource classes
  package.json        # { "name": "@myorg/components", "main": "bin/index.js" }
  src/
    vpc.ts
    auroraCluster.ts
    index.ts
pulumi/
  package.json        # { "@myorg/components": "file:../components" }
  index.ts
```

Pick up component changes with:

```bash
cd components && npm run build
cd ../pulumi && npm install
```

Best for single-team projects, rapid iteration, and any case where
multi-language support is not needed.

**Option B — multi-language component packages.** Components live in their own
repo, published as Pulumi packages with auto-generated SDKs:

```bash
pulumi package add https://github.com/<org>/<component-repo>
```

Best for components shared across teams/projects and for multi-language
consumers, at the cost of more ceremony per iteration.

**Recommendation during a migration:** start with Option A. The tight
edit → build → `npm install` → preview loop matters a great deal while iterating
toward zero diff. Convert to Option B once the migration is done and the
component interfaces have stabilized.

## Package layout

### `PulumiPlugin.yaml`

Every component package **must** have this at the repo root so Pulumi discovers
it as a Node.js component rather than a binary plugin:

```yaml
runtime: nodejs
```

Without it, `pulumi install` fails with
`expected "" to have an executable named "pulumi-resource-" or a PulumiPlugin file`.

### `package.json`

Components are consumed as `file:` dependencies from SDK directories in the
consuming project (via `pulumi package add <repo>`). `main` points at `index.ts`
directly — Pulumi's Node runtime handles TypeScript natively. The `build` script
compiles to `bin/` for type-checking and SDK generation only.

```json
{
    "name": "pulumi-my-components",
    "version": "0.1.0",
    "main": "index.ts",
    "scripts": { "build": "tsc" },
    "dependencies": {
        "@pulumi/pulumi": "^3.232.0",
        "@pulumi/aws": "^7.27.0"
    },
    "devDependencies": { "typescript": "^5.0.0" }
}
```

### `tsconfig.json`

`outDir` is `bin/`. The `files` array must list every component source file
explicitly:

```json
{
    "compilerOptions": {
        "strict": true,
        "outDir": "bin",
        "target": "es2020",
        "module": "commonjs",
        "moduleResolution": "node",
        "sourceMap": true,
        "experimentalDecorators": true,
        "declaration": true,
        "noFallthroughCasesInSwitch": true,
        "noImplicitReturns": true,
        "forceConsistentCasingInFileNames": true,
        "skipLibCheck": true
    },
    "files": ["index.ts", "myComponent.ts"]
}
```

### `.gitignore`

```
node_modules/
bin/
**/sdks
tests/__pycache__
component-tests/sdks/
**/.idea
```

### `package-lock.json`

The lockfile **must** resolve packages from the public npm registry
(`https://registry.npmjs.org`), not a private registry. If your local npm config
points at a private registry (e.g. an artifact proxy), the lockfile records
private `resolved` URLs that fail in CI.

Generate a clean lockfile:

```bash
npm install --registry https://registry.npmjs.org --userconfig /dev/null
```

Verify the private registry hostname does not appear:

```bash
grep -c "<private-registry-host>" package-lock.json   # expect 0
```

## Publishing

Components are published as GitHub repos, not npm packages. Consumers add them
with:

```bash
pulumi package add github.com/<org>/pulumi-my-components
```

That generates a local SDK under `sdks/`, adds a `file:` dependency to
`package.json`, and records the package in the consuming `Pulumi.yaml`:

```yaml
packages:
  pulumi-my-components:
    source: github.com/<org>/pulumi-my-components
    version: 0.1.0
```

Import from the generated SDK package name (the component repo's `package.json`
`name` field):

```typescript
import { MyComponent } from "@myorg/pulumi-my-components";
```

**Cutting a release** — component repos publish from a semver tag:

1. Merge the component PR to `main`.
2. `git tag v<major>.<minor>.<patch> && git push origin v<major>.<minor>.<patch>`
3. The publish workflow creates a GitHub release and runs `pulumi package publish .`
4. Consumers install with `pulumi package add https://github.com/<org>/<component-repo>`

## Developing a component against a consuming project

To test unpublished component changes in a consuming project without pushing.

**One-time setup — switch from published to local:**

1. **Point `Pulumi.yaml` at a local path:**

   ```yaml
   packages:
     # FROM:
     pulumi-my-components: github.com/<org>/pulumi-my-components@v0.2.1
     # TO:
     pulumi-my-components: /path/to/pulumi-my-components
   ```

2. **Build the component:** `cd /path/to/pulumi-my-components && npm run build`

3. **Run `pulumi install`** in the consuming project. For a private component
   repo, supply a token:

   ```bash
   GH_TOKEN=$(gh auth token) GITHUB_TOKEN=$(gh auth token) pulumi install
   ```

   This generates a new SDK dir. **Important:** the generated SDK's npm package
   name comes from the component's `package.json` `name` field, which may differ
   from the published SDK name (e.g. `pulumi-my-components` vs
   `@myorg/pulumi-my-components`) — producing a differently-named `sdks/` dir.

4. **Repoint the `package.json` alias** to the locally-generated SDK. Program
   code imports the published package name, so only the `file:` path changes:

   ```json
   // FROM: "@myorg/pulumi-my-components": "file:sdks/myorg-pulumi-my-components"
   // TO:   "@myorg/pulumi-my-components": "file:sdks/pulumi-my-components"
   ```

5. **`npm install`**, then verify resolution:

   ```bash
   node -e "console.log(require.resolve('@myorg/pulumi-my-components'))"
   # → .../sdks/pulumi-my-components/bin/index.js
   ```

**Rebuild after subsequent changes** (the alias from step 4 persists):

```bash
cd /path/to/pulumi-my-components && npm run build
cd /path/to/consuming-project
rm -rf node_modules/@myorg/pulumi-my-components
pulumi install
```

`pulumi install` rebuilds the component plugin from the local path, regenerates
the SDK, and installs it. No version bumping or plugin-cache management needed.

**Revert to published:**

1. Restore the GitHub ref in `Pulumi.yaml`.
2. Restore the `package.json` alias to the published SDK dir.
3. `pulumi install && npm install`
4. `rm -rf sdks/<local-sdk-dir>`

## CI workflows

### Smoke test (`.github/workflows/smoke-test.yml`)

Runs on PRs and pushes to `main`: deploys the component-tests stack, runs the
pytest assertions, and tears the resources back down.

```yaml
name: Smoke Test

on:
  pull_request:
  push:
    branches: [main]

permissions:
  id-token: write
  contents: read

jobs:
  smoke-test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-python@v5
        with:
          python-version: "3.12"
      - uses: actions/setup-node@v4
        with:
          node-version: "20"

      - name: Authenticate with Pulumi Cloud (OIDC)
        uses: pulumi/auth-actions@v1
        with:
          organization: <your-pulumi-org>
          requested-token-type: urn:pulumi:token-type:access_token:organization

      - name: Install test dependencies
        run: |
          python -m pip install --upgrade pip
          pip install -r tests/requirements.txt

      - name: Run smoke tests
        run: pytest tests/ -v
        env:
          PULUMITEST_STACK_NAME: smoke-test-${{ github.run_id }}
```

The workflow also needs cloud credentials — add whatever your org uses (e.g. an
OIDC role assumption step) alongside the Pulumi Cloud auth.

### Publish (`.github/workflows/publish-pulumi-registry.yml`)

Triggered by version tags; publishes to the Pulumi private registry.

```yaml
name: "Publish Pulumi Component to Private Registry"

on:
  push:
    tags:
      - "v[0-9]+.[0-9]+.[0-9]+"

jobs:
  publish:
    name: "Publish to Pulumi private registry"
    runs-on: ubuntu-latest
    permissions:
      id-token: write
      contents: write
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
          ref: ${{ github.ref }}

      - name: Install Pulumi CLI
        run: |
          curl -fsSL https://get.pulumi.com | sh
          echo "$HOME/.pulumi/bin" >> "$GITHUB_PATH"

      - name: Authenticate with Pulumi Cloud (OIDC)
        uses: pulumi/auth-actions@v1
        with:
          organization: <your-pulumi-org>
          requested-token-type: urn:pulumi:token-type:access_token:organization

      - name: Set package version from tag
        run: npm version "${GITHUB_REF_NAME#v}" --no-git-tag-version

      - name: Create GitHub release
        env:
          GH_TOKEN: ${{ github.token }}
        run: gh release create "${{ github.ref_name }}" --generate-notes

      - name: Publish to private registry
        run: pulumi package publish .
```
