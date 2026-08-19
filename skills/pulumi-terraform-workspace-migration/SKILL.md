---
name: pulumi-terraform-workspace-migration
description: Orchestrate a full Terraform workspace migration (state + HCL config) to a hand-authored Pulumi TypeScript program, driven node-by-node to a zero-diff preview. Covers digesting TF state safely with `tf-digest`, mapping modules to Pulumi components, generating and filling the import file with `import-id-match`, handling resource types that cannot be imported at all, importing (including batched imports), eliminating post-import diffs with `patch-state`, classifying the diffs that remain, migrating secrets to encrypted stack config and ESC, and staging the first `pulumi up`. Use when migrating a Terraform workspace to Pulumi, importing Terraform-managed resources into Pulumi state, investigating a post-import diff, or replacing `terraform_remote_state` cross-stack references. Companion skills - pulumi-terraform-module-to-component and pulumi-component-authoring.
---

# Terraform workspace → Pulumi migration

Migrates a Terraform workspace (state + HCL config) to a hand-authored Pulumi
TypeScript program, importing the live resources and driving each node to a
zero-diff `pulumi preview` before moving to the next.

**Core principle:** the deployed state is the source of truth. `tf-digest` turns
TF state into an agent-safe digest; you hand-author idiomatic components and
program code, then import and drive each node to zero diff.

## Prerequisites

- `pulumi` CLI, with ESC access if the org uses it.
- **`pulumi-tool-import`** — install it from the repo's GitHub releases, then
  invoke as `pulumi plugin run import -- <cmd>`:

  ```bash
  pulumi plugin install tool import \
    --server github://api.github.com/pulumi-proserv/pulumi-tool-import
  ```

  With no version it installs the latest release; append a version (e.g.
  `tool import v0.2.0`) to pin one. The `github://api.github.com/<owner>/<repo>`
  form is required — a plain `https://github.com/...` server 404s, because
  Pulumi then looks for the archive at the repo root rather than under
  `/releases/download/<tag>/`. Confirm with `pulumi plugin run import -- version`.

  Alternatively build from the repo (`go build -o bin/pulumi-tool-import .`) and
  call the binary directly. Provides `tf-digest`, `import-id-match`,
  `patch-state`, `set-secrets`.
- `pulumi-linter` (`pulumi plugin install tool linter`), optional but used in the
  final checklist.
- Terraform state — a local `.tfstate` **or** remote backend credentials
  (Scalr/TFC/TFE). **Never read the tfstate directly; it contains secrets.**
- The Terraform config directory containing the `.tf` files.
- Cloud credentials for the commands that touch AWS. If the org uses ESC, wrap
  those commands: `pulumi env run <esc-env> -- <cmd>`.

**Companion skills:** **pulumi-terraform-module-to-component** (per-module
component generation, including the logical-naming rule that import matching
depends on) and **pulumi-component-authoring** (component interface design,
packaging, publishing, tests).

## Artifact handling — do this before generating anything

**Put every migration artifact in a `.import/` directory inside the Pulumi
project, and gitignore the whole directory.**

```bash
mkdir -p pulumi/.import
echo '.import/' >> pulumi/.gitignore
```

Artifacts belong in the project directory, **not `/tmp`** — `patch-state
--config-dir` and asset-path resolution expect stable paths, and `/tmp` is
cleaned between sessions. But several of these files contain **decrypted
secrets**:

| Artifact | Secret content |
|---|---|
| `*-state-export.json` | Produced with `pulumi stack export --show-secrets` — all secrets in plaintext |
| `*-state-patched.json` | `patch-state` output; embeds plaintext secret sentinels |
| `*-tf-digest.json` | TF-flagged sensitive attributes are redacted, but secrets *embedded inside* non-sensitive string values are not (e.g. an API key inside a CloudFormation `template_body`) |
| `preview*.json` | Resource inputs; may include sensitive values |

**Ignore the directory, not filename patterns.** Pattern-based ignores fail open:
a `.gitignore` covering `*-state-export.json` and `*-tf-digest.json` but not
`*-state-patched.json` leaves a file full of plaintext secret sentinels
untracked-but-not-ignored — one `git add -A` from committing credentials. A
directory-level ignore cannot miss a new artifact type the tooling starts
emitting.

When resuming an existing migration, verify before staging anything:

```bash
git check-ignore -q <artifact> && echo IGNORED || echo "NOT IGNORED"
```

Never commit these, even to "save state for reference". Share a redacted digest
instead.

## Phase 1 — Analyze

### 1a. Run `tf-digest`

**From a local state file:**

```bash
pulumi plugin run import -- tf-digest \
  --from "$TF_CONFIG_DIR" \
  --state-file "$STATE_FILE" \
  --out ".import/${WORKSPACE}-tf-digest.json" \
  --pulumi-stack <stack-name> \
  --pulumi-project <project-name> \
  --project-dir ./pulumi
```

**From a TFC-compatible remote backend (Scalr, TFC, TFE):**

```bash
pulumi env run <esc-env-with-token> -- \
  pulumi plugin run import -- tf-digest \
    --from "$TF_CONFIG_DIR" \
    --hostname <api-hostname> \
    --organization <org-name> \
    --workspace <workspace-name> \
    --token-env <TOKEN_ENV_VAR> \
    --out ".import/${WORKSPACE}-tf-digest.json" \
    --pulumi-stack <stack-name> \
    --pulumi-project <project-name> \
    --project-dir ./pulumi
```

Either `--state-file` or the full set of remote flags is required.
`--token-env` names the environment variable holding the API token (e.g.
`SCALR_TOKEN`, `TFC_TOKEN`) — inject it from an ESC environment with
`pulumi env run`.

**Secrets are handled here.** `tf-digest` discovers every sensitive attribute in
TF state and sets it as an encrypted Pulumi config secret via
`pulumi config set --secret`; `--project-dir` tells it where the Pulumi project
lives. The agent never sees the values. Pass `--skip-secrets` to disable this
(e.g. re-running the digest without overwriting existing secrets).

**Import support is checked here too.** Some Terraform resource types declare no
importer — `aws_vpn_gateway_route_propagation`, `aws_vpn_connection_route`,
`aws_iam_policy_attachment` and other association/toggle resources — and no ID
can import them. The digest asks the provider directly and marks them
`"nonImportable": true`, so Phase 4 can keep them out of the import file instead
of failing the run. This loads the Terraform provider pinned in
`.terraform.lock.hcl`, so run `terraform init` (or `tofu init`) first;
`--skip-import-check` skips it, at the cost of not detecting them.

### 1b. Read the digest — and only the digest

The digest is the agent-safe representation of TF state, containing everything
needed for the migration with sensitive values replaced by `"(sensitive)"`. The
tool determines what to redact from the provider schema's `Sensitive` markings.

- `modules{}` — keyed by module name, including the `for_each` key where present
- `modules[].terraformPath` — the full Terraform address
- `modules[].source` — the module's source
- `modules[].interface.inputs[]` — name, type, required, default, expression,
  evaluatedValue
- `modules[].interface.outputs[]` — output values
- `modules[].resources[]` — `mode` (`managed`/`data`), `translatedUrn`,
  `terraformAddress`, `importId`, `attributes`
- `rootResources[]` — the same, for resources outside any module

A `"(sensitive)"` attribute means the resource holds a secret there, and that
`tf-digest` has already set it as an encrypted stack-config secret.

### 1c. Read the TF source for structure

The digest gives values; the HCL gives relationships. Read the config directory
for: `data` blocks (the digest holds their resolved values — the source shows who
consumes them), `locals {}` (intermediate transformations), `variable {}` blocks
and `*.auto.tfvars` (non-secret config), and each `module "..." {}` call (how
data sources, locals, and variables wire in).

### 1d. Plan the data-source replacements

**Never replace a dynamic TF data source with a hardcoded value.** See
`references/data-sources-and-cross-stack.md` for the equivalence tables, the
`terraform_remote_state` → ESC pattern, and how to handle `null_resource` and
unbridged providers.

### 1e. Group modules into components

Map each TF module to a Pulumi component, and decide where those components will
live (a local `components/` package during migration, or shared component repos).
See the **pulumi-terraform-module-to-component** skill.

## Phase 2 — Initialize the project

```bash
mkdir pulumi && cd pulumi
pulumi new typescript --name <project-name> --yes
```

Then choose a component approach — **start with single-language local
components** (a `file:` dependency, no SDK generation) for the tight
edit → build → preview loop that iterating to zero diff demands, and convert to
published multi-language packages once the migration is done. Both approaches,
and the local-development loop, are in the **pulumi-component-authoring** skill's
`references/packaging-and-publishing.md`.

Create a stack config (`Pulumi.<env>.yaml`) per environment.

## Phase 3 — Incremental per-node loop (the core)

Work through TF modules and bare resources in **dependency order** — roots first,
then downstream. For each node:

1. Write or fix the component (module) or program code (bare resource).
2. Write the program code that instantiates it with the correct inputs.
3. Import just this node's resources (Phase 4).
4. Validate with a targeted preview.
5. **Reach zero real diffs before starting the next node.**

### 3a. Dependency order

Derive it from the TF source; module input expressions in the digest
(`interface.inputs[].expression`) show which modules depend on which. Start with
nodes that have no upstream dependencies. A typical ordering:

networking (VPC, security groups) → data (RDS, S3, DynamoDB) → secrets →
services (ECS, Lambda) → frontend (CloudFront, WAF) → DNS → SSM parameters

### 3b. Value tracing

Every value in the Pulumi code must trace back to its TF source:

| TF source | Pulumi equivalent | Digest role |
|---|---|---|
| `var.foo` | `config.require("foo")` / `config.requireSecret("foo")` | Confirms this workspace's actual value |
| `local.bar` (varies by workspace) | `config.require("bar")`, then derive in-program | Shows the evaluated local |
| `local.baz` (static everywhere) | In-program constant computed from config values | Confirms the static value |
| `module.x.output_y` | The component's output property | Shows the resolved output |
| `resource.x.attr` | The resource's output property | Shows the resolved attribute |
| `data.terraform_remote_state.x.outputs.y` | `config.require()` — values arrive via an ESC env ref | Shows the resolved cross-stack value |
| Literal | Hardcoded literal, only if genuinely static across all envs | Confirms the literal |

**Rules:**

- A hardcoded string that varies by stack is a bug. If TF declares it as a
  variable, it belongs in stack config.
- If the TF repo keeps separate code per environment, even a literal in the HCL
  may differ between workspaces — when in doubt, put it in stack config.
- The digest makes value bugs obvious: if the program hardcodes a value that
  matches the digest, but the TF code computes it from variables, the program
  must compute it too.

**TF locals:** classify each one — derived from `var.*` (compute from config),
derived from other locals (chain it), derived from resource outputs (use Pulumi
output references), or a true static literal (in-program constant). For complex
locals that build a structure passed into a module, check the digest's
`interface.inputs[].evaluatedValue`: if every underlying variable is in config,
reconstruct the structure in code; if not, use `config.requireObject<T>()` with
the evaluated value set via `pulumi config set --path`.

### 3c. Validate with a targeted preview

```bash
pulumi preview --target <component-or-resource-urn> --target-dependents
```

For component nodes, target the component URN with `--target-dependents` to
include its children; for bare resources, list each URN with `--target`. Get URNs
by cross-referencing TF addresses against `mappings.yaml` and resolving the
logical names against `pulumi stack export`.

### 3d. Resolve the diffs

| Diagnosis | Action |
|---|---|
| Component bug | Fix the component, rebuild, reinstall |
| Program bug (wrong input, hardcoded value) | Fix the program code |
| Known post-import diff class | Classify and track — see `references/diff-taxonomy.md` |
| TF drift (live differs from TF code) | Match the deployed state; document the decision |
| URN changed (logical name changed) | `pulumi state delete <old-urn>`, re-import under the new URN |
| Computed/dynamic field (`version`, `versionId`) | Usually a consequence of another diff — investigate |

Priorities: **`replace` diffs first** — they mean the program would destroy and
recreate a live resource. `name` replaces on IAM roles/policies and buckets are
almost always a wrong naming pattern in the program. `tags`/`tagsAll` diffs are
usually a wrong `Name` tag or a tag the TF module sets and the program doesn't.
`policy` diffs are either structural (hand-built JSON instead of
`getPolicyDocumentOutput`) or genuinely different actions/resources.

**Manual drift** — a value present in the deployed state and the digest, but not
in the TF code — should be **included** in the Pulumi program to preserve it, and
sourced from stack config if it could vary by workspace. Check the digest, not
just the HCL.

Re-import is only needed when **URNs change**. Program-only changes just need
another preview.

The full classification taxonomy, the investigation procedure for an unknown
field, and the staged-`up` ordering are in `references/diff-taxonomy.md`.

### 3e. Zero-diff gate and commit

The node is done when its targeted preview shows zero diffs excluding the known
post-import classes. Commit component changes and program changes as a logical
pair, with messages naming the TF module or resource group.

Tips:

- **Shared components:** fix the component once for the first instance, then
  validate each further instance's *program inputs* separately.
- **SecretsManager `secretString`:** JSON keys must be alphabetically ordered to
  match TF's `jsonencode`, or the secret shows a replace diff.
- **Check every workspace** before concluding a value is static.

## Phase 4 — Import mechanics

Generating the import skeleton, writing `mappings.yaml`, filling IDs with
`import-id-match`, multi-provider setup, and batched imports:
**`references/import-mechanics.md`**.

**Resources that cannot be imported.** `import-id-match` drops any resource the
digest flagged `nonImportable` and records it in a `*.non-importable.json`
sidecar next to the output. These resources **already exist**, so letting the
next `pulumi up` create them is not a safe fallback — for association and toggle
resources the create fails against the pre-existing object and dies partway
through the stack. Write them into state with `patch-state tf --non-importable`,
which takes each resource's URN, parent, provider and dependencies from a
preview of the program you have already written, fills in the ID and outputs
from the sidecar, and verifies the result with a second preview — restoring its
backup if any injected resource does not preview as unchanged:

    pulumi plugin run import -- patch-state tf \
      --digest tf-digest.json \
      --fields data/aws-import-diff-fields.json \
      --config-dir ./terraform \
      --non-importable imports-ready.non-importable.json \
      --project-dir . --stack dev

`pulumi refresh` reporting "unchanged" is never the check: it only proves the
IDs resolve, not that the values are right. Full background:
**`docs/non-importable-resources.md`** in the tool repo.

Eliminating the diffs that survive import with `patch-state` (including falsy
default suppression and how to tell that a field simply needs adding to the
fields file): **`references/patch-state.md`**.

## Phase 5 — Stack config

**Secrets are automatic.** `tf-digest` (Phase 1a) discovers every `"(sensitive)"`
attribute and sets it via `pulumi config set --secret`. Verify afterwards by
checking `Pulumi.<env>.yaml` for `secure:` entries.

For secrets not in TF state, use the standalone `set-secrets` command — the agent
supplies mappings, never values:

```bash
pulumi plugin run import -- set-secrets \
  --state-file "$STATE_FILE" \
  --project-dir ./pulumi \
  --stack <stack-name> \
  --map 'configKey1=terraform.address.of.resource1:attribute' \
  --map 'configKey2=terraform.address.of.resource2:attribute'
```

**Non-secret config** — the rest of `Pulumi.<env>.yaml` — comes from the digest
attributes (bucket names, environment names, CIDRs) or the TF config's
`*.auto.tfvars`.

**ESC environments** — if the stack draws cross-stack values or cloud credentials
from ESC, reference them in the stack config:

```yaml
environment:
  - <esc-project>/<env>
```

## Phase 6 — Verify and open the PR

Run a full preview with no `--target`:

```bash
pulumi preview 2>&1 | tee .import/full-preview.txt
```

Every remaining diff should be a known post-import class. Real diffs at this
stage are cross-node interactions the targeted previews missed — investigate.

For the diff counts the migration report needs, summarize the JSON preview:

```bash
pulumi preview --json > .import/preview.json

# Operation counts
jq -r '.steps[] | select(.op != "same") | .op' .import/preview.json \
  | sort | uniq -c

# Which fields are still diffing, most frequent first
jq -r '.steps[] | select(.op == "update") | .diffReasons[]?' .import/preview.json \
  | sort | uniq -c | sort -rn

# Same, broken out by resource type — usually the most actionable view
jq -r '.steps[] | select(.op == "update")
       | (.urn | split("::")[2]) as $t
       | .diffReasons[]? | "\($t)\t\(.)"' .import/preview.json \
  | sort | uniq -c | sort -rn
```

Each remaining field then gets a category and a justification, per
`references/diff-taxonomy.md`.

Checklist:

1. **Linter passes** — `pulumi-linter --language typescript src/`, zero violations.
2. **Preview clean** — only known post-import classes remain.
3. **Value tracing** — every value traces to a TF source; no hardcoded evaluated
   values.
4. **Secrets** — no plaintext secrets in source; all sensitive values in
   encrypted config or ESC.
5. **Components consumable** — `pulumi package add <repo>` works, if publishing.
6. **No artifacts staged** — `git status` shows nothing from `.import/`.

The PR carries the component changes, the program and stack configs, the ESC
environment definitions, and a written record of every TF-drift decision and the
remaining diff counts.

## Troubleshooting

| Issue | Solution |
|---|---|
| `tf-digest`: `Warning: could not load tfjson state` | Non-fatal — URN translation may be incomplete, but module interfaces still resolve |
| `tf-digest` returns no resources | The state file may be empty — confirm it has resources |
| Import ID mismatch | Cross-reference the digest's `attributes` and `importId` for that resource |
| Component type error | Verify the args interface matches the digest's `interface.inputs` |
| Cross-stack reference fails | Ensure the referenced stack is migrated and its outputs are exported |
| `error: parse resource provider reference: expected '::' …` during import | Cosmetic; the import still succeeded. Verify the count with `pulumi stack` |
