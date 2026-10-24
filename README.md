# actionlint plugin registry

This registry lists actionlint plugins and automatically accepts authenticated
releases with verified checksums and discovery metadata. Publishers host their own
binaries. Host compatibility is recorded separately and does not control publication.

- Publication API: **https://registry.actionlint.kjanat.dev**
- Static catalogue: **https://plugins.actionlint.kjanat.dev/**
- Publishers do not need individual approval. The registry accepts every authorized
  submission that passes admission checks.
- OIDC identifies who authorized a submission. Acceptance means the release passed
  the admission checks; it does not guarantee that the plugin is safe or correct.

## Download counts

The catalogue displays GitHub's cumulative `download_count` for each accepted
plugin asset. Only the admitted asset ID, name and SHA-256 digest are counted;
sidecars such as schemas and checksums are excluded. These are downloads, not
unique users or installations: validation, CI and repeated downloads contribute,
while cached checks do not.

[`downloads.json`](https://plugins.actionlint.kjanat.dev/downloads.json) contains
the observation time, release identities and counts. `available` includes a
nonnegative `count` (including zero); `unavailable` and `unsupported` omit it.
GitLab counts are currently unsupported. Failed requests and rate limits do not
become zero and do not block catalogue publication. Counts are separate from
immutable publication records and installer indexes.

The Pages workflow refreshes counts on publication and on a six-hour schedule
(GitHub may delay scheduled runs). Each run retains a timestamped
`download-counts-*` artifact for 90 days, providing observation history without
rewriting publication records. The public JSON contains the latest snapshot;
history starts when this collection is enabled. There is no client telemetry.

To collect a snapshot locally using `GH_TOKEN` or existing GitHub CLI credentials:

```sh
go run ./cmd/registry export --store registry/plugins --output dist --download-counts
```

Omit `--download-counts` for a network-free export. A refresh has a one-minute
budget; assets not observed within that budget are marked unavailable.

## Catalogue website

The Go registry exports JSON only. The separate TypeScript application in [`site/`](site/)
reads that catalogue and builds the homepage and per-plugin pages. Installer indexes,
release records, statuses and download observations keep their existing schemas.

```sh
mise i
mise run setup
mise run check:site
mise run build:site
```

`build:site` exports registry JSON into `dist/`, then runs the Bun site builder over
the same directory. To render an existing export directly:

```sh
bun site/cli.ts --input dist --output dist
```

The Pages workflow refreshes the JSON, builds the site and uploads the combined
output. CI checks site types and behavior and builds against the actual registry
records. `mise run test:site` runs the frontend tests alone.

## Publishing

### Check a candidate

Check packaged candidate bytes before creating a tag or release. The shared
[candidate check](docs/publishing.md) creates a temporary catalogue, installs the
plugin with the real actionlint CLI, runs your behavioral fixture, then checks
offline execution and missing-cache failure. It writes a receipt for the exact
artifact, host and fixture. Nothing enters the public registry during this check.

Use [`check/action.yml`](check/action.yml) in PR CI and
[`publish/action.yml`](publish/action.yml) in the tag workflow. Publication verifies
the receipt and package, uploads the same bytes, waits for registry admission and
checks delivery with the same fixture. Keep build and plugin-specific assertions
in the plugin repository; the registry owns upload, submission and recovery tooling.

Publish a tagged release with an embedded `actionlint.plugin` custom section.
The [metadata policy](docs/metadata.md) describes both supported WASM formats.
Submit the SHA-256 checksum of the file you distribute. For `.wasm.gz` files,
calculate the checksum of the compressed file.
The Worker records the CI provider, project ID, and tag from the authenticated
request. The admission service then checks the project's identity and the commit referenced
by the tag against the hosting provider's API.

### GitHub Actions

The shared publish action requires `contents: write` and `id-token: write` in a job
running on the release tag. Enable immutable releases in the repository first.
Its [inputs and workflow requirements](docs/publishing.md#github-actions) cover candidate
receipts, package directories and host/fixture selection.

If another workflow already publishes the release, the root action submits that
existing release using only `contents: read` and `id-token: write`:

```yaml
on:
  release:
    types: [published]
permissions:
  contents: read
  id-token: write
jobs:
  register:
    runs-on: ubuntu-latest
    steps:
      # Obtain the digest of the exact uploaded asset in your release workflow.
      - uses: kjanat/actionlint-registry@master # Pin a reviewed commit SHA.
        with:
          asset: plugin.wasm
          sha256: ${{ vars.PLUGIN_RELEASE_SHA256 }}
          kind: core
```

Replace the example checksum with the checksum produced by your release job.
The action uses the current repository and tag unless you supply them explicitly.
You do not need a personal access token or write access to the registry repository.
The action returns the request identifier through its `submission-id` output.

### GitLab.com

You can submit plugins from tag pipelines in public GitLab.com projects, including
projects in nested groups. Publish a release and upload the file to a generic
package named `actionlint-plugin` in the same project. Use the exact release tag as
the package version.

The only accepted download URL is:

```text
https://gitlab.com/api/v4/projects/<project-id>/packages/generic/actionlint-plugin/<tag>/<asset>
```

Use a separate job to submit the plugin so that it receives a fresh OIDC token.
The registry rejects tokens issued more than ten minutes ago, even if GitLab gives
them a later expiration time.

Request this audience:

```yaml
register:
  id_tokens:
    REGISTRY_ID_TOKEN:
      aud: https://registry.actionlint.kjanat.dev
  rules:
    - if: $CI_COMMIT_TAG
  script:
    - bun install --cwd registry-client --frozen-lockfile
    - bun registry-client/scripts/cli.ts submit
  variables:
    PLUGIN_PROVIDER: gitlab
    PLUGIN_REPOSITORY: $CI_PROJECT_PATH
    PLUGIN_TAG: $CI_COMMIT_TAG
    PLUGIN_ASSET: plugin.wasm
    PLUGIN_KIND: component
```

Set `PLUGIN_SHA256` to the checksum produced by the release build. Check out a
reviewed registry commit in `registry-client/` and install Bun before running the job.
Keep the ID token masked; never print it or use shell tracing. The client reads it
from its environment. No long-lived registry credential is required.

GitLab allows publishers to change releases and packages. The registry records
`upstream_immutable: false` for these releases and permanently associates each
accepted version with its checksum. If the downloaded file changes, checksum
verification fails. GitHub submissions must use immutable releases, and the registry
records that guarantee separately. Because the registry does not mirror binaries,
plugins become unavailable if their publishers stop hosting them.

Supporting another host requires code to verify its OIDC tokens and resolve its
releases. Publishers cannot supply arbitrary token issuers or download URLs, and
a webhook alone cannot authorize publication. Once a host is supported, its
publishers do not need individual approval.

## Authorization and processing

1. The publisher's CI job posts to `/v1/submissions` with an OIDC token whose audience is the publication API's origin.
2. The Worker verifies the token's signature, issuer, audience, timestamps, and project and tag claims.
3. A SQLite Durable Object records the request and a notification in the same transaction. It prevents the token or version from being reused for a different submission.
4. The registry's GitHub App starts the admission workflow. That workflow authenticates with OIDC and claims a queued request. A scheduled run every five minutes provides a fallback.
5. A job with read-only repository access downloads the file and verifies its checksum, metadata and identity against existing registrations. It writes a separate candidate delta without executing the plugin.
6. A separate job checks that delta against the latest registrations and commits the accepted records using the registry App's bot identity. GitHub Pages then publishes the generated catalogues.
7. The workflow marks the submission `published` only after Pages succeeds.

The registry uses its own GitHub App to start admission and publish accepted
records. The App is installed only on the registry repository; publishers do not
need to install it. See [GitHub App setup](docs/github-app.md) for its permissions
and deployment configuration. GitHub may delay workflows, but queued requests
remain stored until a workflow processes them.
Opening an issue or adding a repository to the discovery list does not authorize
publication. Running the workflow manually processes existing authorized requests.

Use `GET /v1/submissions/<submission-id>` to check a request's status. Proven invalid
artifact content or identity is rejected immediately, including conflicts found
during publication. Infrastructure failures return the claim for retry after 60
seconds, then 120 seconds. After three failed attempts, the request becomes `failed`.
Resubmitting identical metadata with fresh OIDC starts another bounded sequence
under the original submission ID. Rejected and published requests are terminal.
Status responses expose the validator run while work is active and `retry_at` when
queued again. If cleanup cannot run, the one-hour lease expiry is the fallback.
The shared waiter retries transient HTTP failures within its deadline. Retrying
cannot replace accepted bytes.
The [Worker documentation](worker/README.md) explains authentication, quotas, and
how claims expire.

OIDC proves which CI identity authorized a digest. It does not prove those bytes
were built from that commit; build attestations are separate work.

## Storage and consumption

Authoritative records are independent files:

```text
registry/plugins/<sha256(issuer + newline + project-id)>/
  plugin.json
  versions/<sha256(exact-tag)>.json
```

The JSON records retain the original publisher, repository, and tag names. Directory
and file names use hashes to avoid case collisions, reserved Windows filenames,
and path traversal through tag names. Each version records its download URL,
checksum, authenticated submission receipt, embedded metadata, and release evidence.
A new release can change its ABI, component interface, asset name, or container
format. These belong to the immutable version record. Schema-1 `plugin.json`
contains only the publisher and plugin identity; it has no `asset` or `kind`.
Previously accepted version files are never rewritten. Repository
transfers and changes to the plugin ID still require an explicit identity migration.

`registry/sources.json` is an explicit discovery seed, not publication authority.
It names the preferred artifact for future discovery; it is not inferred from
version order. Currently admission consumes authenticated submissions directly,
and no scheduled job reads the seed. Historical releases continue resolving from
their own version records, regardless of the current preferred artifact.

The generated root `index.json` contains plugin summaries and links. Per-plugin indexes
contain version summaries and links; version records contain full evidence and metadata.
The registry regenerates these indexes from accepted records. It does not choose a
`latest` version by sorting tag names alphabetically.

```sh
go run ./cmd/registry resolve --store registry/plugins \
  --plugin github.com/owner/plugin --version v1.0.0
# Prints the publisher URL with #sha256=...
actionlint --plugin 'https://publisher.example/plugin.wasm#sha256=...'
```

Actionlint can install declared registry plugins with `actionlint plugin install`;
its adjacent lock pins the accepted artifact. Disable a conflicting built-in
analyzer when replacing it, for example `--shellcheck=`. [Schemas](schemas/) describe
submission, receipt, plugin, version and catalogue data.

[Release status](docs/release-status.md) defines deprecation, withdrawal and
permanent digest revocation. Audited status events remain separate from immutable
publication records; every catalogue export includes `statuses.json`.

## Host compatibility

Publication does not imply compatibility with any actionlint release. A release
without compatibility reports is untested. Unknown ABIs and component contracts
remain discoverable; the registry does not interpret their rule descriptors or facts.

The optional [actionlint adapter](adapters/actionlint/) owns its own Go module and
host dependency. The registry has no dependency on the actionlint SDK. Other host
versions can use independent checker executables implementing the
[report protocol](schemas/compatibility.schema.json).

A trusted operator can run a checker against a local copy of an accepted artifact:

```sh
go -C adapters/actionlint build -o ../../actionlint-checker .
go run ./cmd/registry check --checker ./actionlint-checker \
  --artifact plugin.wasm --plugin github.com/owner/plugin --version v1.0.0
```

Omit `--plugin` and `--version` to check unpublished bytes without creating a tag
or accepted version. The report is an observation of that artifact and host; it
does not authorize publication. The full candidate check additionally exercises
installation and the plugin's behavioral fixture through the supported CLI.

Reports are independent immutable files under
`registry/compatibility/<artifact-sha256>/<report-sha256>.json`. They identify the
exact artifact, host revision, checker executable checksum, policy, time, and outcome.
The catalogue links to these reports per version. Adding a report never rewrites
an accepted release. Existing records retain their historical inline validation.

The checker is trusted operator code. It runs with a deadline and bounded output,
without inherited registry credentials. This process boundary is not an operating
system sandbox. Admission never starts a checker, and a missing or crashing checker
cannot block publication. See [metadata and checks](docs/metadata.md).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development requirements, validation
commands, and deployment instructions.
