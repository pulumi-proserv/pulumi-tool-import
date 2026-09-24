# Provider-major import-ID catalogs

Each provider-major module owns its embedded table, custom composers, source
provenance, and regression tests. The major in the directory and module path is
the **Pulumi provider major**, not the upstream Terraform major.

| Module | Generated for Pulumi | Upstream version | Upstream revision |
|---|---|---|---|
| `aws/v6` | `v6.83.4` | `v5.100.0` | `f7a3b98da589ab1d52756b0dcee0dbf2de83d635` |
| `aws/v7` | `v7.48.0` | `v6.66.0` | `351a07f09bffcde77bddcc35060e3da3e9a591ee` |

These revisions are the `upstream` submodule commits in the corresponding
`pulumi/pulumi-aws` release tags. `catalog` is a small, standard-library-only Go
module defining the table schema, expansion, and table/composer bundle. Provider
modules depend on it, never on the root CLI or on one another.

## Selection and compatibility

The CLI selects the destination Pulumi AWS major from an import entry's explicit
`version`, or from the digest's resolved `aws@version` pin. Terraform/OpenTofu
registry spellings are equivalent. An unversioned/dynamic pin, conflicting pins,
or an unsupported major produces a diagnostic and no automatic composition for
affected AWS entries. Other providers are unaffected.

Within each supported major the latest generated snapshot is used. The recorded
Pulumi version is generation provenance, not a claim of exact coverage for every
minor. Newer destination versions warn; maintainers should retain regression
cases for older releases they support and add version-specific behavior when
upstream import semantics actually differ. There is no fallback across majors.

`--import-id-formats` overrides JSON only, not Go composers. It must declare
`provider: hashicorp/aws`, `pulumiVersion` in the destination major, and an upstream
`version` in the corresponding upstream major. A mismatched override leaves that
entry unresolved with a diagnostic. The chosen module always supplies composers.

## Regenerating

1. Choose a new Pulumi provider release within the major. Inspect its exact
   upstream source pin:
   ```sh
   gh api 'repos/pulumi/pulumi-aws/contents/upstream?ref=v7.48.0' --jq .sha
   ```
2. Update that module's `source.json` with the Pulumi release, upstream version,
   and full upstream commit. This generation pin is independent of the CLI's
   default migration-provider recommendation in `pkg/providermap`.
3. Inspect any changed modeled acceptance-test helpers. To print their hashes:
   ```sh
   go run ./internal/tools/importidscrape \
     --provider-version <upstream-commit> --print-helper-hashes
   ```
   Update `helpers` only after checking the corresponding Go bodies against the
   classifier's semantics. Only helpers in that pin's verified profile can
   establish a template. v6 currently models one helper; v7 models four.
4. Regenerate and review the resulting table and composers together:
   ```sh
   make update-import-id-formats AWS_CATALOG_MAJORS=7
   make test
   make check
   ```
   Omit `AWS_CATALOG_MAJORS` to regenerate both supported majors. The cache must
   be clean and at the requested commit. `--check` compares bytes without
   rewriting committed artifacts.

When a generated template replaces a manual entry, remove its obsolete composer
from that major's module. For example, v7 now derives the Kinesis stream name
from a template, while v6 retains its own composer. Conversely, v7's API Gateway
method test now delegates to an upstream helper; v7 carries a verified custom
composer for its `rest_api_id/resource_id/http_method` format.

## Modules, tests, and releases

Root `go test ./...` does not visit nested Go modules. `make test-catalogs` and
`make vet-catalogs` run each independently with `GOWORK=off`. `make lint-catalogs`
runs the linter in each module's directory. `make test` and
`make check` include those targets; CI invokes them explicitly too.

The root and provider modules use local `replace` directives for checked-out
development. The provider modules have their own Go release versions, independent
of `pulumiVersion` in their catalogs: a composer fix can ship without changing
the upstream source snapshot.

When publishing these modules, tag the shared contract first, then the provider
modules after confirming their `require` versions refer to a published contract:

| Module | Initial release tag |
|---|---|
| `importids/catalog` | `importids/catalog/v0.1.0` |
| `importids/aws/v6` | `importids/aws/v6.0.0` |
| `importids/aws/v7` | `importids/aws/v7.0.0` |

Go's major-version-directory convention excludes the terminal `/v6` or `/v7`
from the tag prefix. Local replacements are ignored when a downstream module
imports a published catalog, so the shared contract must be available at its
required version. These module tags are separate from the CLI's `vX.Y.Z` releases;
generating tables or running checks does not publish tags.
