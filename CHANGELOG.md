# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `version` command, printing the version stamped into the binary at release
  time. Previously `pkg/version.Version` was set via ldflags but no command
  surfaced it, so an installed plugin could not identify itself.
- Install instructions in the README and in the skills that use the tool. The
  plugin installs from GitHub releases with
  `pulumi plugin install tool import --server github://api.github.com/pulumi-proserv/pulumi-tool-import`
  (omit the version for the latest release). The `github://` server form is
  required; a plain `https://github.com/...` server 404s because Pulumi then
  looks for the archive at the repo root rather than under
  `/releases/download/<tag>/`.
- `patch-state tf --non-importable <sidecar>` writes resources that cannot be
  imported into the stack's state, closing the loop opened in v0.2.0 when
  `resolve tf` began detecting them (#22).
- `patch-state tf` gains a stack mode: given `--project-dir` and `--stack` with
  no `--state`/`--out`, it exports the deployment, writes a timestamped backup,
  patches and injects, imports the result, and verifies it with
  `pulumi preview`, restoring the backup if any injected resource does not
  preview as unchanged.
- `digest tf` now records the Pulumi outputs, raw state delta, and Terraform
  schema version for resources it flags as non-importable, computed while the
  provider it already starts for the import-support probe is open. `patch-state`
  consumes them, so it needs no provider of its own. Where any of them cannot be
  computed — five distinct causes, most seriously the import-support probe and
  the Pulumi bridge resolving different providers — the reason is recorded and
  warned at digest time and carried into the sidecar, so `patch-state` says
  which resources fell back to raw attribute renaming and why (#26).

### Fixed

- **Secrets could reach state in plaintext, two ways.** State in
  `tofu show -json` format got no redaction at all — the format is selected
  automatically on the presence of a `format_version` key, with no flag to
  indicate it — because `AttrSensitivePaths` was never populated for it.
  Sensitive attributes nested below the top level were never redacted at any
  depth.
- **A sensitive attribute the Terraform state did not mark was written to disk
  in plaintext.** Redaction and the stack-config discovery that recovers the
  values are both driven by the state's own `AttrSensitivePaths`, so one
  missing mark defeats both at once, silently — which is exactly what the
  `tofu show -json` path did. The provider schema's `Sensitive` flags are now
  consulted as an independent second source: a top-level attribute the schema
  marks and the state does not is redacted from the digest *and* written to
  stack config, so it is recoverable at injection time exactly as a marked one
  would be. The stack config key is derived from the resource address and
  attribute name, never from the marks, which is what makes recovery possible
  at all.

  A **nested** attribute in the same position fails the digest instead, naming
  the paths but never the values. Recovering a nested secret from stack config
  is not implemented anywhere in the pipeline (#28), so redacting one would
  replace a leak with a placeholder nothing can resolve.

  Both paths are backed by a check that runs for every resource, so a
  redaction that runs and does nothing can no longer report success.
- **Colliding stack config keys wrote one resource's secret into another.**
  Two sensitive attributes that flatten to the same config key were deduped
  with a `_2` suffix, but nothing could read a suffixed key back, so the second
  resource resolved to the first one's secret — nondeterministically, since the
  discovery walk was unsorted. `digest tf` now **fails** and names both
  addresses, and the walk is sorted. This is a behaviour change: a digest that
  previously warned now errors.
- **A large sensitive integer was corrupted in stack config.**
  `DiscoverSensitiveSecrets` decoded with a plain `json.Unmarshal` and
  stringified with `%v`, turning `1234567890123456789` into
  `"1.2345678901234568e+18"`, which is then what any consumer of the stack
  config gets as the secret's value.
- **A secret exported without `--show-secrets` reverted every patch.** The
  `{sig, ciphertext}` shape produced by the documented file-mode workflow
  (`pulumi stack export > state.json`) recovered as a plain object, so the
  bridge's `Recover` failed and the run discarded its own patches while
  reporting success. Stack mode is unaffected, since `auto.Stack.Export` passes
  `--show-secrets`.
- **A hung or crashed provider was classified as importable** (#31).
  `"Plugin did not respond"` — what OpenTofu emits when a plugin stops
  answering — was missing from the transport-failure list, and the fallthrough
  is "importable". Since every probe after a crash fails identically, one
  downed plugin could mark a whole run's resources importable.
- **`tofu show -json` state silently rounded every integer above 2^53.**
  `tfjson.State` has a custom unmarshaller running its own decoder, so setting
  `UseNumber` at the call site had no effect; `State.UseJSONNumber(true)` is the
  only hook that reaches it. The value became a different integer that is still
  valid JSON, so nothing downstream could detect it (#27).
- **`patch-state cfn` and `resolve cfn` rounded integers above 2^53 too**, from
  a plain `json.Unmarshal` of the digest. CFN resources map to aws-classic and
  share the state-writing path with the tf side, so the rounded value reached
  Pulumi state the same way (#27).

### Changed

- `patch-state` now names every resource the fields file covers that matched
  no digest entry, instead of counting them into an aggregate nothing printed.
  A migration that silently patched 40 of 41 resources previously looked
  identical to one that patched all 41 (#37). The deliberate asymmetry between
  patching's name-guessing and injection's exact-or-fail matching is now
  documented at both matchers.

- `patch-state` now reports `Deltas validated (imported)` and
  `Deltas attached (injected): X of Y` rather than an unqualified
  `Delta validated`. The two count different populations with different
  producers — the bridge writes deltas during `pulumi import`, while injected
  resources never reach the provider — and the previous labels made them look
  like one number.
- `make vet` and `make lint` now also check the `e2e`-tagged build, which no
  target or CI job previously compiled.

### Removed

- The unused schema-driven sensitivity subsystem (`BuildSensitivityMap` and
  everything behind it). It had no callers at all, tests included, and was
  repeatedly cited as the mechanism that would fix the nested-path and
  `tofu show -json` redaction gaps — both of which are now closed in the
  redaction path that actually runs. Keeping it meant 249 lines that read as a
  working alternative and were not one. The schema's `Sensitive` flags are
  still consulted, but at one boundary and with a caller: the cross-check
  above.

## [0.2.0] - 2026-08-12

### Added
- **Detection of resource types that cannot be imported.** Terraform types that
  declare no importer fail `pulumi import` with a misleading "resource '<id>'
  does not exist", and dropping them from the import file silently turns them
  into creates against existing infrastructure. `digest tf` now asks the
  provider itself (via `ImportResourceState`, unconfigured and credential-free)
  and flags them `nonImportable`; `resolve tf` leaves them out of the import
  file, records them in a `*.non-importable.json` sidecar for state injection,
  and warns. Opt out with `digest tf --skip-import-check`. See
  [docs/non-importable-resources.md](docs/non-importable-resources.md).

### Fixed
- `aws_route` import IDs are now translated. Terraform state carries an opaque
  `r-rtb-…` hash, which `pulumi import` rejects; the ID is now composed as
  `ROUTETABLEID_DESTINATION` from `route_table_id` plus whichever of
  `destination_cidr_block`, `destination_ipv6_cidr_block`, or
  `destination_prefix_list_id` is set.

### Changed
- The test workflow caches provider downloads (`~/.pulumi/plugins`,
  `~/.pulumi/dynamic_tf_plugins`, `~/.pulumi/mapping-cache`) and enables the
  Terraform plugin cache, so runs no longer re-download every Pulumi plugin,
  bridge mapping, and Terraform provider. These downloads are the job's least
  reliable step — transient registry 503s and GitHub release timeouts have
  failed runs with nothing wrong in them.

## [0.1.0] - 2026-08-12

First release under the new repository.

### Added
- Production-readiness scaffolding: `golangci-lint` config, `Makefile`, Dependabot
  config, CI lint / `go vet` / gofmt / `govulncheck` jobs, and community/governance
  files (`CONTRIBUTING`, `SECURITY`, `CODE_OF_CONDUCT`, issue/PR templates,
  `CODEOWNERS`).

### Changed
- **Repository moved to `pulumi-proserv/pulumi-tool-import`** (a fresh, non-fork,
  public repo). The Go module path is now
  `github.com/pulumi-proserv/pulumi-tool-import`, and the plugin is invoked as
  `pulumi plugin run import` (previously `pulumi plugin run terraform-migrate`).
- Test workflow simplified to a secret-free run (local Pulumi backend; the tests
  need neither Pulumi Cloud nor AWS credentials).
- `aws-import-diff-fields.json` moved to `data/`.
- CI now runs on pushes to the default branch (previously `pull_request` only).

### Fixed
- Release workflow now derives the Go toolchain from `go.mod` instead of a pinned
  (and stale) version, and uses `actions/checkout@v4`.

### Removed
- The upstream `stack` state-translation command and all code reachable only from
  it (~5,300 LOC). The tool now focuses solely on the `pulumi import` workflow.
- Committed `.envrc` containing a developer-specific AWS profile (replaced with
  `.envrc.example`).

[Unreleased]: https://github.com/pulumi-proserv/pulumi-tool-import/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/pulumi-proserv/pulumi-tool-import/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/pulumi-proserv/pulumi-tool-import/releases/tag/v0.1.0
