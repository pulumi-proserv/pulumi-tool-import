# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
- The test workflow caches provider downloads (`~/.pulumi/plugins`,
  `~/.pulumi/dynamic_tf_plugins`, `~/.pulumi/mapping-cache`) and enables the
  Terraform plugin cache, so runs no longer re-download every Pulumi plugin,
  bridge mapping, and Terraform provider. These downloads are the job's least
  reliable step — transient registry 503s and GitHub release timeouts have
  failed runs with nothing wrong in them.

### Fixed
- `aws_route` import IDs are now translated. Terraform state carries an opaque
  `r-rtb-…` hash, which `pulumi import` rejects; the ID is now composed as
  `ROUTETABLEID_DESTINATION` from `route_table_id` plus whichever of
  `destination_cidr_block`, `destination_ipv6_cidr_block`, or
  `destination_prefix_list_id` is set.
- Release workflow now derives the Go toolchain from `go.mod` instead of a pinned
  (and stale) version, and uses `actions/checkout@v4`.

### Removed
- The upstream `stack` state-translation command and all code reachable only from
  it (~5,300 LOC). The tool now focuses solely on the `pulumi import` workflow.
- Committed `.envrc` containing a developer-specific AWS profile (replaced with
  `.envrc.example`).
