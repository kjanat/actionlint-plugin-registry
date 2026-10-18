# Publication inbox

Public Worker source. No publisher token, GitHub App key or Cloudflare credential
belongs in this repository. `wrangler.jsonc` contains public deployment identifiers.

The Worker authorizes a publication **attempt**. It does not execute WASM, download
publisher artifacts, or declare a plugin safe. The registry validator independently
resolves the release, binds its repository ID and commit to the receipt, downloads
the digest-pinned artifact, and validates the plugin before publishing static data.

## Authentication

- Publisher audience: `https://plugins.actionlint.kjanat.dev`.
- Validator audience: `https://plugins.actionlint.kjanat.dev/validator`.
- Only GitHub Actions and GitLab.com issuers are configured. JWKS endpoints are
  fixed in source; JWT headers never select a URL. `jose` verifies RS256 signatures,
  exact audience, issuer, required timestamps and a maximum token age of ten minutes
  with five seconds of clock tolerance. Its JWKS fetch rejects redirects.
- GitHub publishers require a public repository, their own tag and workflow, and
  `push`, `release` or `workflow_dispatch`. Pull-request contexts are rejected.
- GitLab publishers require public tag pipelines from `push` or `web`;
  `job_project_id`/`job_project_path` must match the pipeline project and submission.
  CI configuration must come from that same tag and commit. Merge requests and
  external CI configurations are rejected.
- Subjects are retained as bounded identity evidence, not parsed into repository
  identity. Both providers support customized subjects. Stable provider IDs and
  explicit signed project/ref claims establish ownership.
- Consumers require the configured registry repository ID, exact admission workflow
  on `master`, and `schedule` or `workflow_dispatch`. Publisher tokens cannot claim
  work. Fresh consumer tokens across jobs in the same run/attempt retain lease ownership.

OIDC establishes authorization by the repository's CI identity. It does **not**
prove that the submitted bytes were built by that workflow. Build provenance and
host-enforced release immutability are separate evidence.

## HTTP contract

All JSON bodies are bounded to 16 KiB; only listed submission fields are accepted.
POST requests use `Authorization: Bearer <OIDC token>`. Tokens are never persisted.

`POST /v1/submissions` accepts:

```json
{
  "schema_version": 1,
  "provider": "github",
  "repository": "owner/plugin",
  "tag": "v1.0.0",
  "asset": "plugin.wasm",
  "sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
  "kind": "core"
}
```

Provider is `github` or `gitlab`; GitLab nested groups are supported. Kind is `core`
or `component`; `.wasm.gz` assets are also supported. Success is HTTP 202
`{"id":"UUID","status":"pending"}` (retries return the current status).

`POST /v1/claims` uses the validator audience and returns
`{"claim":null}` or `{"claim":{"receipt":{...},"lease_id":"UUID"}}`.
The receipt contains `schema_version`, `id`, `submission`, `authorized_at` and
`identity`: `issuer`, `subject`, `repository_id`, `owner_id`, `repository`, `ref`,
`commit`, `workflow`, `run_id`. IDs are strings. GitHub run identity is
`run_id:run_attempt`; GitLab uses `pipeline_id:job_id`.

`POST /v1/claims/<id>/complete` accepts
`{"lease_id":"UUID","status":"published"}` or `rejected`, with an optional
bounded `detail`. Return value is `{"id":"UUID","status":"published"}`.
Completion requires the current lease and original consumer identity. Identical
completion retries are idempotent. Detail remains private.

`GET /v1/submissions/<id>` returns only `{id,status}`. Public states are `pending`,
`validating`, `published`, `rejected`, `failed`. `GET /health` returns `{status:"ok"}`.
Responses disable caching. No browser CORS publication interface is enabled.

## Persistence and retry rules

One SQLite Durable Object serializes the prototype inbox. Transactions atomically
bind `(issuer, jti)` to canonical submission bytes and
`(issuer, repository ID, tag, asset)` to those same bytes. A new token, different
plugin kind or changed digest cannot replace an existing version. Retries of the
same normalized submission return the original receipt.

Each publisher has at most ten pending/validating records and thirty fresh-token
admissions per UTC hour. The global pending limit is 1,000. Replay records expire;
rate buckets older than 48 hours are removed. Immutable version bindings remain.

Leases last sixty minutes. Expired work is retried up to three claims; a subsequent
poll marks exhausted work `failed`. Infrastructure failures should leave the lease
to expire, rather than incorrectly reject the plugin. Terminal records cannot be
reopened through publication. Administrative recovery is not implemented yet;
changing bytes requires a new tag. A scheduler outage delays both work and expiry
processing. A queued validator can outlive a lease; later completion then fails and
the next claim must reconcile any already-published immutable record idempotently.
No claim means no publication, regardless of public inbox status.

## Local checks and deployment

```sh
bun install --frozen-lockfile
bun run typecheck
bun test
bun run check:bundle
```

Tests verify real JWT signatures and policy failures, and execute the same SQL
against Bun SQLite for replay, immutable binding, quota and lease transitions.
Dry-run bundles the Worker without starting a server or deploying. Tests do not
substitute for a live provider-issued OIDC publication and validator round trip.

Deploy explicitly with `bunx wrangler deploy` after reviewing the account, domain,
repository ID and workflow reference. The custom domain is
`plugins.actionlint.kjanat.dev`; workers.dev and preview URLs are disabled.

References: [Cloudflare SQLite storage](https://developers.cloudflare.com/durable-objects/api/sqlite-storage-api/),
[GitHub OIDC](https://docs.github.com/en/actions/reference/security/oidc),
[GitLab ID tokens](https://docs.gitlab.com/ci/secrets/id_token_authentication/).
