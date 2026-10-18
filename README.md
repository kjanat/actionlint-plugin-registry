# actionlint plugin registry

Public plugin catalogue; autonomous compatibility validation; publisher-owned binaries.
The Worker source is public. Deployment credentials and OIDC tokens never belong in Git.

- Publication API: **https://plugins.actionlint.kjanat.dev**
- Static catalogue: **https://kjanat.github.io/actionlint-plugin-registry/**
- No publisher allowlist. Every authorized submission passing validation is accepted.
- OIDC authorizes an attempt; acceptance does not certify code safety or general correctness.

## Publishing

Publish a tagged release with an embedded `actionlint.plugin` custom section.
[Metadata policy](docs/metadata.md) describes both supported WASM formats.
Submit the distributed file's SHA256 (compressed bytes for `.wasm.gz`).
The Worker binds the submission to the CI issuer, stable project ID and tag.
The validator independently checks the provider's project identity and tag commit.

### GitHub Actions

Enable immutable releases. Upload assets to a draft release before publishing it.
Run submission from the same public repository's tag workflow:

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

The digest above is illustrative: wire your release job's actual digest output.
Repository and tag default to the workflow context. No PAT, issue creation or
cross-repository write permission is needed. The action outputs `submission-id`.

### GitLab.com

Public tag pipelines are supported, including nested groups. Publish a release and
upload the file to the same project's generic package named `actionlint-plugin`,
with package version equal to the exact release tag. The only accepted download URL is:

```text
https://gitlab.com/api/v4/projects/<project-id>/packages/generic/actionlint-plugin/<tag>/<asset>
```

Use a fresh dedicated submission job: tokens older than ten minutes are rejected,
even when GitLab gives the job's token a longer expiry. Request this audience:

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

Provide `PLUGIN_SHA256` from the release build and obtain `scripts/submit.py` plus
`scripts/registry_api.py` from a reviewed registry commit at `registry-client/`.
Keep the ID token masked; never print it or use shell tracing. The client reads it
from its environment. No long-lived registry credential is required.

GitLab releases/packages can change upstream. The registry records
`upstream_immutable: false` and an immutable version-to-digest binding; changed bytes
fail hash verification. GitHub entries require upstream immutable releases and record
that separately. Neither provider guarantees availability through this pointer-only registry.

Other hosts require an explicit issuer/release adapter. Arbitrary self-hosted issuer
URLs, arbitrary asset URLs and webhook-only publication are rejected. Supporting a
new trusted issuer does not require approving each publisher under that issuer.

## Authorization and processing

1. CI posts to `/v1/submissions` with a short-lived OIDC token, audience equal to the API origin.
2. The Worker verifies signature, issuer, audience, timestamps and signed project/tag claims.
3. A SQLite Durable Object atomically records the request, replay key and immutable version binding.
4. Registry Actions polls the durable inbox every five minutes and authenticates with its own OIDC token.
5. A read-only job downloads and validates the authorized bytes through the pinned actionlint host.
6. A separate writer merges immutable version records, then Pages publishes generated catalogues.
7. The workflow marks the submission `published` only after Pages succeeds.

Scheduling can be delayed by GitHub. Requests persist independently of scheduled runs.
There is no dedicated GitHub App/bot: polling and separate OIDC audiences eliminate
its credential from this prototype. Issues and the discovery source list have no
publication authority. Manual dispatch drains authorized work; it cannot invent a submission.

`GET /v1/submissions/<submission-id>` reports state. Execution failures leave the lease
for retry. After three expired attempts the request becomes `failed`; administrative
recovery is not implemented yet. A retry cannot overwrite a version's recorded bytes.
[Worker protocol and limits](worker/README.md) covers quotas, leases and identity policy.

OIDC proves which CI identity authorized a digest. It does not prove those bytes
were built from that commit; build attestations are separate work.

## Storage and consumption

Authoritative records are independent files:

```text
registry/plugins/<sha256(issuer + newline + project-id)>/
  plugin.json
  versions/<sha256(exact-tag)>.json
```

Names retain readable publisher/repository/tag values inside JSON. Hash paths prevent
case collisions, reserved Windows filenames and tag path traversal. Each version holds
its immutable artifact pointer, verified publisher receipt, complete embedded metadata,
release evidence and validation result. Plugin identity and artifact selection remain
stable; renames, transfers and ABI migrations require an explicit future policy.

The generated root `index.json` contains plugin summaries and links. Per-plugin indexes
contain version summaries and links; version records contain full evidence and metadata.
Generated indexes are never merged as source data. There is no lexically guessed `latest`.

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

Host pinned to `abe9f897d9d8d019c572142626b8f055352f2ac7`; existing records retain their
original evidence when the host changes. See [metadata and execution checks](docs/metadata.md).
Downloads are limited to 64 MiB, expanded WASM to 128 MiB. Credentials are removed from
child validator environments. Guest limits are not a substitute for an OS sandbox.

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

Go requires the pinned 1.27.2 toolchain. GitHub APIs use `cli/go-gh/v2` authentication
and typed `google/go-github/v83`, with API version `2026-03-10`. GitLab access is
unauthenticated and restricted to the documented public provider endpoints.

`admit --receipt` and `merge --candidate` are trusted workflow handoffs, not public
APIs. A receipt file is not a signed credential: its authorization comes from the
OIDC-authenticated HTTPS claim response. Never consume a publisher-provided receipt
file through the writer workflow. `inspect` never authorizes publication.

Worker deployment is explicit (`bunx wrangler deploy` from `worker/`). It uses a
custom domain and SQLite Durable Object; workers.dev and preview URLs are disabled.
No development server is required for any listed check.
