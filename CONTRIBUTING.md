# Contributing

Thanks for your interest in improving `pulumi-tool-import`. This is a
Pulumi Professional Services tool for migrating Terraform (and, increasingly, AWS
CDK/CloudFormation) infrastructure into Pulumi via the standard `pulumi import`
workflow.

## Getting started

Prerequisites:

- Go (the version pinned in [`go.mod`](./go.mod))
- [`golangci-lint`](https://golangci-lint.run/) v2
- [OpenTofu](https://opentofu.org/) and the Pulumi CLI (for the integration tests)

Common tasks are wired up in the [`Makefile`](./Makefile):

```bash
make build      # compile the CLI to ./bin
make test       # run the Go test suite
make lint       # run golangci-lint
make fmt        # gofmt the tree
make check      # fmt-check + vet + lint + import-id-formats-check (local gate)
```

`make check` also regenerates the AWS import-ID table from the Terraform
provider source and fails on any difference from the committed
`pkg/importid/aws-import-id-formats.json`. The first run sparse-clones
terraform-provider-aws (network, one to two minutes); later runs reuse the
checkout cached under your OS cache directory
(`~/Library/Caches/pulumi-tool-import/` on macOS). If the table is stale, run
`make update-import-id-formats` and commit the result.

## Tests

The suite mixes fast unit tests with heavier integration tests that download and
bridge real Pulumi providers and warm up OpenTofu. Those integration tests need
network access and a Pulumi access token, and some skip themselves when the
required infrastructure or credentials are absent. `make lint` needs neither,
so run it for a quick local signal; `make check` needs network on its first run
for the provider clone described above, but no credentials.

## Pull requests

- Branch off `main`; do not commit directly to `main`.
- Keep PRs focused; write a clear description of the change and its motivation.
- Ensure `make check` passes and add tests for new behavior.
- CI runs lint, `go vet`, a gofmt check, `govulncheck`, and the Go test suite.
  All required checks must be green before merge.

## Branch protection

`main` is a protected branch. The recommended ruleset (applied by a repo admin —
see the commands in [the setup notes below](#applying-branch-protection)) is:

- Require a pull request before merging, with at least one approving review.
- Require the `lint`, `vet`, and `govulncheck` status checks to pass.
- Dismiss stale approvals on new pushes.
- Disallow force-pushes and direct pushes to `main`.

### Applying branch protection

A repository admin can apply the baseline with the GitHub CLI:

```bash
gh api -X PUT \
  repos/pulumi-proserv/pulumi-tool-import/branches/main/protection \
  --input - <<'JSON'
{
  "required_status_checks": {
    "strict": true,
    "contexts": ["lint", "vet", "govulncheck"]
  },
  "enforce_admins": false,
  "required_pull_request_reviews": {
    "required_approving_review_count": 1,
    "dismiss_stale_reviews": true
  },
  "restrictions": null,
  "allow_force_pushes": false,
  "allow_deletions": false
}
JSON
```

Adjust the `contexts` to match the exact CI job names, and add
`"require_code_owner_reviews": true` only once [`CODEOWNERS`](./.github/CODEOWNERS)
lists a valid owner.

## License

By contributing, you agree that your contributions are licensed under the
[Apache License 2.0](./LICENSE). New source files should carry the standard
Pulumi Apache header (the `goheader` linter enforces this).
