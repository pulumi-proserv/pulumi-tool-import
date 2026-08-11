# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
