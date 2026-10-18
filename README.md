# actionlint plugin registry

This registry lists actionlint plugins and automatically accepts releases that pass
its compatibility checks. Publishers host their own binaries; the registry stores
download links, checksums, metadata, and validation results.

The registry and Cloudflare Worker source code are public. Keep deployment
credentials and OIDC tokens out of Git.

- Publication API: **https://plugins.actionlint.kjanat.dev**
- Static catalogue: **https://kjanat.github.io/actionlint-plugin-registry/**
- Publishers do not need individual approval. The registry accepts every authorized
  submission that passes validation.
- OIDC identifies who authorized a submission. Acceptance means the plugin passed
  the documented checks; it does not guarantee that the plugin is safe or correct.

## Publishing

Publish a tagged release with an embedded `actionlint.plugin` custom section.
The [metadata policy](docs/metadata.md) describes both supported WASM formats.
Submit the SHA-256 checksum of the file you distribute. For `.wasm.gz` files,
calculate the checksum of the compressed file.
The Worker records the CI provider, project ID, and tag from the authenticated
request. The validator then checks the project's identity and the commit referenced
by the tag against the hosting provider's API.

### GitHub Actions

Enable immutable releases. Upload assets to a draft release before publishing it.
Submit the release from a workflow running on that tag in the same public repository:

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
      - uses: kjanat/actionlint-plugin-registry@master # Pin a reviewed commit SHA.
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
      aud: https://plugins.actionlint.kjanat.dev
  rules:
    - if: $CI_COMMIT_TAG
  script:
    - python3 registry-client/scripts/submit.py
  variables:
    PLUGIN_PROVIDER: gitlab
    PLUGIN_REPOSITORY: $CI_PROJECT_PATH
    PLUGIN_TAG: $CI_COMMIT_TAG
    PLUGIN_ASSET: plugin.wasm
    PLUGIN_KIND: component
```

Set `PLUGIN_SHA256` to the checksum produced by the release build. Copy
`scripts/submit.py` and `scripts/registry_api.py` from a reviewed registry commit
into `registry-client/scripts/`.
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
4. The registry's GitHub App starts the validation workflow. That workflow authenticates with OIDC and claims a queued request. A scheduled run every five minutes provides a fallback.
5. A job with read-only repository access downloads the submitted file and validates it using the selected actionlint host version.
6. A separate job commits the accepted records using the registry App's bot identity. GitHub Pages then publishes the generated catalogues.
7. The workflow marks the submission `published` only after Pages succeeds.

The registry uses its own GitHub App to start validation and publish accepted
records. The App is installed only on the registry repository; publishers do not
need to install it. See [GitHub App setup](docs/github-app.md) for its permissions
and deployment configuration. GitHub may delay workflows, but queued requests
remain stored until a workflow processes them.
Opening an issue or adding a repository to the discovery list does not authorize
publication. Running the workflow manually processes existing authorized requests.

Use `GET /v1/submissions/<submission-id>` to check a request's status. If a workflow
fails, its claim expires and the request can be retried. After three expired
attempts, the request becomes `failed`. There is no administrative recovery command
yet. Retrying a request cannot replace an accepted version with different bytes.
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
checksum, authenticated submission receipt, embedded metadata, release evidence,
and validation result. The registry does not yet support changes to a plugin's
identity or artifact selection, such as repository transfers or ABI migrations.

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

Actionlint does not yet resolve registry names itself. Disable a conflicting built-in
analyzer when replacing it, for example `--shellcheck=`. [Schemas](schemas/) describe
submission, receipt, plugin, version and catalogue data.

## Validation and development

The validator uses actionlint commit `abe9f897d9d8d019c572142626b8f055352f2ac7`.
Updating the host does not replace the evidence stored in previously accepted
records. See the [metadata and execution checks](docs/metadata.md) for details.
Downloads must not exceed 64 MiB, and decompressed WASM must not exceed 128 MiB.
The validator runs in a child process without inherited credentials. Its guest
limits do not provide the isolation of an operating-system sandbox.

```sh
go test ./internal/registry ./internal/validation ./cmd/registry
golangci-lint run --allow-parallel-runners
python3 -B -m unittest discover -s scripts -p 'test_*.py'
go run ./cmd/registry inspect --artifact /path/to/plugin.wasm
go run ./cmd/registry export --store registry/plugins --output dist
cd worker
bun install --frozen-lockfile
bun run typecheck
bun test
bun run check:bundle
```

The project requires Go 1.27.0 or newer and selects Go 1.27.2 in `go.mod`.
It uses `cli/go-gh/v2` for GitHub authentication and `google/go-github/v83` to call
GitHub API version `2026-03-10`. GitLab requests use the documented public endpoints
and do not include authentication credentials.

The registry workflow uses `admit --receipt` and `merge --candidate` to pass data
between trusted jobs. These commands are not public submission APIs. A receipt file
does not contain a verifiable signature; the workflow must obtain it from the
Worker over HTTPS after authenticating with OIDC. Never pass a receipt supplied by
a publisher to the job that writes registry records. The `inspect` command checks
compatibility without authorizing publication.

To deploy the Worker, run `bunx wrangler deploy` from `worker/`. It uses a custom
domain and a SQLite Durable Object. Access through workers.dev and preview URLs is
disabled. None of the checks listed above requires a development server.
