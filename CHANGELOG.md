# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

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
  consumes them, so it needs no provider of its own.

### Fixed

- **Secrets could reach state in plaintext, three ways.** State in
  `tofu show -json` format got no redaction at all — the format is selected
  automatically on the presence of a `format_version` key, with no flag to
  indicate it — because `AttrSensitivePaths` was never populated for it.
  Sensitive attributes nested below the top level were never redacted at any
  depth. And a raw state delta's Replace nodes, which carry the provider's
  verbatim values, were written without the secret envelope the bridge applies
  to the deltas it writes itself.
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
  `"1.2345678901234568e+18"` — which injection then wrote into state as the
  resource's real secret.
- **Verification could pass on a stack it had made worse.** An operation
  escalating from `update` to `replace` or `delete` was invisible, because the
  check compared only counts and newly-dirty resources. Since many `not_read`
  fields are ForceNew, a wrongly patched value produces exactly that, and the
  next `pulumi up` would destroy and recreate a live resource.
- **`--non-importable` with an empty sidecar verified nothing**, then imported
  the result into the live stack. It was the only path through `patch-state`
  that ran no integrity check at all.
- **A secret exported without `--show-secrets` reverted every patch.** The
  `{sig, ciphertext}` shape produced by the documented file-mode workflow
  (`pulumi stack export > state.json`) recovered as a plain object, so the
  bridge's `Recover` failed and the run discarded its own patches while
  reporting success.
- **Injection could write values nothing could distinguish from real ones.** A
  resource with no import ID was accepted and later panicked the engine; the
  engine's unknown-value sentinel was copied into state verbatim when an
  injected resource referenced another injected resource; and a masked input
  whose Terraform name could not be derived was silently deleted rather than
  reported.
- **A raw state delta containing a Replace node was silently corrupted.** It was
  serialized via `Marshal().Mappable()`, which emits SDK-internal field names;
  reading it back produced no error and an *empty* delta, so the resource
  reconstructed the wrong Terraform state on every operation.
- **A hung or crashed provider was classified as importable** (#31).
  `"Plugin did not respond"` — what OpenTofu emits when a plugin stops
  answering — was missing from the transport-failure list, and the fallthrough
  is "importable". Since every probe after a crash fails identically, one
  downed plugin could mark a whole run's resources importable.
- Duplicate `create` steps for the same type and name no longer block injection
  outright; the ambiguity is reported only if a sidecar entry actually needs it.
  Two same-named resources under different components are legal.
- `resolve tf` no longer mis-parses a resource name containing `::`, such as a
  `for_each` key derived from an ARN.

### Changed

- `patch-state` now reports `Deltas validated (imported)` and
  `Deltas attached (injected): X of Y` rather than an unqualified
  `Delta validated`. The two count different populations with different
  producers — the bridge writes deltas during `pulumi import`, while injected
  resources never reach the provider — and the previous labels made them look
  like one number.
- `make vet` and `make lint` now also check the `e2e`-tagged build, which no
  target or CI job previously compiled.

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
