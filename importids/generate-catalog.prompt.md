# Generate a provider-version import-identity catalog

Use this prompt during catalog development, against pinned provider checkouts.
The released CLI consumes the resulting static tables and Go composers; it does
not invoke this prompt during migrations.

## Invocation

Supply the following task header, then execute the instructions below:

```yaml
catalogModule: importids/aws/v7
destination:
  repository: https://github.com/pulumi/pulumi-aws
  release: <exact release>
  commit: <full commit SHA>
  checkout: <local checkout at that commit>
upstream:
  repository: https://github.com/hashicorp/terraform-provider-aws
  release: <exact release>
  commit: <full upstream commit SHA>
  checkout: <local checkout at that commit>
historicalSources: [] # Optional entries with repository, release, commit, checkout.
resources: all       # Or an explicit list of Terraform resource types.
outputDirectory: <directory for the evidence report and regression vectors>
```

Read the selected module's `source.json` to obtain its existing provenance. Verify
the destination release's upstream pin and account for downstream patches; do not
substitute the latest upstream release. If a supplied pin cannot be verified,
report the missing evidence before making changes that depend on it.

An empty `historicalSources` list means no historical release matrix was supplied.
Inspect destination state upgraders for historical shapes, but do not attribute a
shape to a particular old release without inspecting that release or equivalent
immutable evidence. No customer state or cloud credentials are required.

## Task

Produce a reviewable catalog-generation patch, an evidence report, and regression
vectors for the selected destination version. Establish each resource's import
identity contract, including resources that import using their current state ID.
Use the repository's actual catalog schema and generation workflow.

Start by reading:

- `importids/README.md` and the selected module's source pin, table, and composers.
- `importids/catalog/formats.go` and `importids/catalog/catalog.go`.
- `internal/tools/importidscrape/{steps,classify,emit}.go`.
- `pkg/import_filler.go` and `pkg/importid/` for runtime selection and overrides.

The acceptance-test scraper is an evidence extractor, not a complete description
of accepted identifiers. Its omission of a type does not establish compatibility
with arbitrary source state.

## 1. Establish resource coverage

Enumerate managed resource registrations at the pinned destination, resolving the
Terraform-to-Pulumi mappings, aliases, and bridge overrides. Exclude data sources.
Reconcile this inventory with the requested resource selection.

Track every selected resource explicitly. A missing test, unsupported expression,
or unavailable document must produce an unresolved result, not an omitted row.
If the work is incomplete, identify the unprocessed resources and mark coverage
as partial. Do not describe a partial audit as a complete provider catalog.

## 2. Establish the destination import contract

For each resource, follow these sources and their relevant helpers:

1. Resource registration and import-support declaration, including custom import
   handlers, SDK passthrough handlers, framework importers, and identity schemas.
2. Import parsing, validation, normalization, and any additional resources emitted
   by import. Record behavior outside the CLI's one-entry model as a limitation.
3. Read/refresh implementation: how the supplied identifier reaches cloud API
   calls, and whether provider configuration supplies part of the identity.
4. Create/read assignments to the stored ID and associated identity attributes.
5. Version-matched import documentation and examples.
6. Acceptance-test import steps, explicit IDs, helper bodies, and assertions.
7. Pulumi bridge overrides and downstream patches affecting any of the above.

Record accepted forms, required components, conditional alternatives, relevant
account/region/partition/endpoint context, and normalization of the imported ID.
Distinguish documented forms, implementation-supported forms, and tested forms.
A single documentation example is not an exhaustive grammar. Cloud API acceptance
alone does not prove the provider accepts the same input.

For a test with no explicit import-ID override, trace the stored ID's construction.
For example, if Create assigns an ARN to ID and the test imports using that ID,
the test demonstrates ARN import. Copying that ID works for state with that
representation. Do not invent a current-version mismatch.

Preserve distinct accepted forms instead of choosing whichever test occurs first.
When sources conflict, report the discrepancy and narrow the conclusion to what
the evidence establishes. Do not silently promote a guess into a catalog rule.

## 3. Check historical representations

Inspect identity-related schema upgrades in the destination and the supplied
historical source releases' ID assignments, identity attributes, tests, and docs.

For each established source shape, record:

- Stored ID representation and available identity attributes.
- Applicable source commit/schema version and supporting evidence.
- Whether direct reuse satisfies the destination contract.
- Any required normalization and information unavailable from that shape.

Do not equate the cloud resource's creation version with the version/schema that
last wrote its state. A fresh native import does not load the old Terraform state
through the destination's Terraform state-upgrade pipeline.

Keep these conclusions separate:

- **Current representation:** source ID already satisfies the destination contract.
- **Historical representation:** an evidenced older shape needs normalization.
- **Unknown representation:** available evidence does not establish compatibility.

A state upgrader can establish a historical shape without establishing exactly
which releases wrote it. Mark that release range unknown rather than guessing.

## 4. Derive deterministic rules

Choose an explicit decision for each resource:

- `pass-through`: reuse ID for the established source representations; state the
  evidence and coverage boundary.
- `template`: compose identity attributes using the existing `{attribute}` / `{id}`
  template syntax.
- `composer`: implement deterministic conditional or validated normalization in
  the destination-major module.
- `unsupported-import`: provider evidence establishes import is unsupported.
- `unresolved`: evidence or identity information is insufficient.

For templates/composers, specify input precedence, preconditions, output form,
provider-context requirements, and behavior for already-normalized IDs. Specify
missing, empty, null, redacted, incorrectly typed, and conflicting inputs where
relevant. Do not silently select between identities that could name different
resources. Use only evidenced defaults.

Prefer explicit component attributes to splitting composite state IDs. Only split
when the provider contract makes the operation unambiguous; account for embedded
delimiters, ARN prefixes, and partner-bus paths. Do not fabricate account, region,
partition, or other identity components.

When all supplied source representations use the same rule, avoid unnecessary
source-version branching. When behavior depends on a version, ensure the CLI has
reliable information to select it; otherwise report the selection as unresolved.

## 5. Generate within this repository's architecture

Keep the evidence report separate from runtime `formats.json`. The report's
coverage statuses and identity contracts are not new runtime JSON fields.

- Improve the AST scraper when a general source pattern can be recognized safely.
  Verify modeled helper bodies/hashes against the exact pin.
- Regenerate `formats.json` through `make update-import-id-formats`; do not hand
  edit generated JSON or weaken the byte-for-byte drift check.
- Use provider-major composers for behavior that needs code. Keep table entries
  and composer registration consistent with the repository's invariants.
- Historical rules not represented by current import tests can use reviewed
  module-local additions, as CloudTrail does in `catalog.go`. Cite their evidence
  and ensure regeneration cannot erase them.
- A rule unsupported by the current schema must become an explicit composer or
  an unresolved proposal, not invented executable rule syntax.
- Retain unsupported/unresolved decisions in the report even when the runtime
  catalog cannot express them. Do not manufacture import-support metadata from
  the absence of a format entry.
- Report post-import input reconstruction or preview differences separately;
  they are not import-ID rules.

Do not create cloud resources, run native imports, refresh customer stacks, publish
releases, or commit/push changes unless the invoking task explicitly requests it.

## 6. Evidence report and regression vectors

Write `identity-report.json` with this structure (these are report fields):

```text
destination: {repository, release, commit, upstreamCommit}
historicalSources: [{repository, release, commit}]
coverage: {selectedResources: [type], processedResources: [type],
           unprocessedResources: [type], complete: boolean}
resources: [{
  terraformType, pulumiTypes: [token],
  decision: pass-through | template | composer | unsupported-import | unresolved,
  destinationContract: {acceptedForms: [description], contextRequirements: [description],
                        normalization: description, evidence: [evidenceId]},
  sourceRepresentations: [{commit: SHA or null, schemaVersion: value or null,
                          idShape, identityAttributes: [name], compatibility,
                          evidence: [evidenceId]}],
  resolution: {template: string or null, composer: symbol or null,
               preconditions: [description], inputPrecedence: [description],
               failureBehavior: description},
  uncertainties: [description],
  changesFromExistingCatalog: [description],
  evidence: [{id, repository, commit, path, startLine, endLine,
              symbol, observation, kind: implementation | documentation | test | upgrader}]
}]
```

Write `identity-vectors.json` containing cases with `terraformType`, destination
version, source provenance (or `synthetic`), input `id`, input `attributes`, provider
context, expected outcome (`resolved`, `unresolved`, or `unsupported`), expected ID
or null, expected diagnostic when relevant, and supporting evidence IDs.

Include applicable cases for current valid IDs, evidenced historical IDs,
conditional forms, delimiter-bearing components, missing/null/empty/redacted
attributes, and conflicts. Label synthetic fixtures honestly; a case using an old
version label is not proof it reproduces that release's state. Translate vectors
into executable tests exercising the actual catalog-selection/composition path.

For a CloudTrail-like case, specifically verify:

- A current ARN ID remains usable without inventing a conversion requirement.
- A name ID plus ARN attribute normalizes only when historical-shape and destination
  evidence support that rule.
- A name ID without the required identity attribute is diagnosed.
- A conflict between valid identities receives explicit handling.

## 7. Validate and report

Verify report coverage against the inventory, evidence locations against pinned
checkouts, and consistency between contracts, proposed rules, and test outcomes.
Run `make test` and `make check` using the repository's Go version and compatible
lint tooling. Check the final diff for unrelated edits and generation drift.

Summarize changed rules, unchanged pass-through resources, unresolved cases, and
historical coverage limits. List actual checks and their outcomes, including
skipped or blocked tests. Static analysis and unit fixtures establish evidence
and composition behavior; claim live import validation only if it was explicitly
authorized and actually performed.
