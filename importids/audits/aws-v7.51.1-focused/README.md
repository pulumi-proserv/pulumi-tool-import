# AWS 7.51.1 focused identity audit

Execution of `importids/generate-catalog.prompt.md` for five selected managed
resources. This is a complete audit of the selected batch, **not a full-provider
baseline**. Source inspection and synthetic regression tests were used; no cloud
resources were created and no live imports were performed for this audit.

## Pins

- Pulumi AWS 7.51.1: `ca34048d18e00d42c77ea43fa758568b3051fa0d`.
- Its upstream, Terraform AWS 6.69.1: `d0cb9698b41b21c587043949fde0bac0459e335a`.
- Historical source, Terraform AWS 3.74.3: `dbf2b9c4d6a7fd3355d4e9931b34f0de89070217`.

The Pulumi upstream gitlink and release commit message establish the destination
pin. Modeled acceptance-test helper hashes are unchanged from the previous pin.
`provider/resource_overrides.go` was checked for all five resources; its overrides
affect naming/types/replacement semantics, not the import-ID grammar. The downstream
patch set has no changes to these five resource implementations or the inspected
SDK import wrappers. These observations are scoped to this release.

## Findings

| Resource | Terraform 3.74.3 state | Destination state/import | Result |
|---|---|---|---|
| CloudTrail | Create stores the trail **name**, Read stores `arn`; docs import by name | Create stores ARN; ARN importer parses ARN and checks explicit region consistency | Confirmed historical mismatch directly from both pinned implementations. Preserve current ARN; normalize matching historical name using `arn`. |
| User policy attachment | Create stores generated user-prefixed ID; import accepts slash form and stores hyphen form | Create now stores slash form; import accepts `user/policy_arn` | Existing generated template remains justified by an explicit acceptance helper. Add typed input and conflict handling. |
| Group policy attachment | Generated group-prefixed ID; imported state uses hyphen form | Still generates IDs on Create; importer accepts slash form and stores hyphen form | Compose `group/policy_arn`; do not mistake its stored ID for an import ID. |
| Role policy attachment | Generated role-prefixed ID; import accepts slash form and stores hyphen form | Create now stores slash form; import accepts `role/policy_arn` | Existing generated template remains justified by an explicit acceptance helper. Add typed input and conflict handling. |
| EventTarget | Hyphen-joined ID plus explicit bus/rule/target attributes | Still hyphen-joined state ID; import parses slash forms, including ARN/partner buses | Existing composer has the right shapes. Reject redacted/non-string components, invalid slash-bearing buses, and conflicting import-form IDs. |

The user/role creation changes **do not currently remove their catalog entries**:
their acceptance tests still explicitly compose the slash import form. This is
useful evidence against assuming either that all tests are pass-through or that
current ID changes necessarily cause catalog drift.

### New defensive checks

The previous CloudTrail composer accepted any string beginning with `arn:` and
silently preferred ID over a conflicting `arn` attribute. It now checks the trail
ARN structure, rejects conflicts, and verifies a historical name matches the ARN's
trail name. This is a conservative resolution policy; it is not a claim that every
syntactically rejected string was exercised against the AWS API.

IAM templates stringify arbitrary JSON values and do not reconcile a valid
import-form ID with attributes naming a different attachment. The v7 runtime now
uses reviewed composers while retaining generated template evidence. Valid slash
IDs can supply missing components; redacted/non-string attributes and conflicts
remain unresolved. Generated/hyphenated state IDs remain opaque.

EventTarget attributes remain authoritative for opaque hyphenated IDs; those IDs
cannot generally be parsed unambiguously. For an existing slash import ID, a
conflict is rejected. An ARN/partner-bus legacy ID is compared with the complete
expected legacy form before considering slash-ID conflicts.

## Artifacts and coverage

- `identity-report.json`: contracts, source representations, evidence, dependency
  watch paths, and explicit limitations. Evidence source aliases resolve through
  the immutable `sources` map.
- `identity-vectors.json`: synthetic cases grounded in the inspected source shapes;
  not copies of customer state. Executed by `pkg/import_identity_audit_test.go`.

The v7 regeneration adds eight unrelated mechanically extracted templates and
updates documentation/evidence locations. No existing format rules were removed
or changed by that regeneration. Those eight additions are **not** included in
this five-resource semantic audit.

Remaining boundaries: provider-account permissions, actual resource existence,
and provider configuration are checked by native import, not by ID composition.
No release range between 3.74.3 and 6.69.1 was exhaustively audited. The v6 module
was not subjected to this new audit and retains its prior behavior.

## Incremental follow-up

After a full-provider baseline, diff immutable source snapshots and invalidate
resources by dependency, not merely by their primary resource file. Watch helpers,
registration/identity metadata, docs, tests, bridge overrides, patches, and SDK
dependency versions. Treat additions/removals/renames explicitly. Changes to the
prompt, catalog schema, or analyzer semantics can invalidate the baseline too.

This report records conservative watch paths, but its dependency closure is not
certified complete. It therefore cannot authorize skipping resources in a future
full-provider audit. Unknown impact should widen the audit, never imply unchanged
behavior. Historical source pins and their evidence remain inputs even when only
the destination version changes.
