# Publication inbox

This directory contains the public source code for the publication Worker.
Do not commit publisher tokens, GitHub App keys, or Cloudflare credentials.
The deployment identifiers in `wrangler.jsonc` are public configuration values.

The Worker authenticates publishers and queues their submissions for validation.
It does not download or execute plugins. The registry validator resolves the
release and checks its repository ID and commit against the authenticated receipt.
It then downloads the artifact, verifies its checksum, and validates the plugin
before publishing a registry record.

After accepting a submission, the Worker uses the registry's GitHub App to start
the validation workflow. The workflow still authenticates with OIDC and claims
the stored request. Starting a workflow does not authorize a new submission.
The [GitHub App setup guide](../docs/github-app.md) describes the required installation
and credentials.

## Authentication

- Publisher tokens must use the audience `https://registry.actionlint.kjanat.dev`.
- Validator tokens must use the audience `https://registry.actionlint.kjanat.dev/validator`.
- Only GitHub Actions and GitLab.com issuers are configured. JWKS endpoints are
  fixed in the source code, so JWT headers cannot redirect key retrieval to another
  URL. The `jose` library verifies the RS256 signature, exact audience, issuer, and
  required timestamps. Tokens must have been issued within the last ten minutes,
  allowing five seconds for clock differences. Requests for signing keys reject redirects.
- GitHub publishers must run a workflow from a tag in their own public repository.
  The event must be `push`, `release`, or `workflow_dispatch`. Tokens from
  pull-request workflows are rejected.
- GitLab publishers must use tag pipelines in public projects, triggered by `push`
  or `web`. The `job_project_id` and `job_project_path` claims must match both the
  pipeline project and the submission. The CI configuration must come from the
  same tag and commit. Merge requests and external CI configurations are rejected.
- The Worker retains the token's subject as identity evidence, with a length limit.
  It uses the signed project ID and reference claims to identify the repository,
  because both providers allow customized subjects.
- The registry workflow must match the configured repository ID and workflow path
  on `master`, and its event must be `schedule` or `workflow_dispatch`. Publisher
  tokens cannot claim queued work. Different jobs in the same workflow run and
  attempt can use fresh tokens to act on the same claim.

OIDC identifies the CI job that authorized a submission. It does not prove that
the job built the submitted file. Build attestations and the hosting provider's
release immutability guarantees provide separate evidence.

## HTTP contract

`GET /` and `HEAD /` redirect to the [plugin catalogue](https://plugins.actionlint.kjanat.dev/).

JSON request bodies must not exceed 16 KiB, and submissions can contain only the
documented fields. POST requests must include `Authorization: Bearer <OIDC token>`.
The Worker does not store tokens.

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

Set `provider` to `github` or `gitlab` and `kind` to `core` or `component`.
GitLab repository paths can include nested groups. Assets can use either the
`.wasm` or `.wasm.gz` extension. A new submission returns HTTP 202 with
`{"id":"UUID","status":"pending"}`. Repeating the same submission returns its
current status.

`POST /v1/claims` uses the validator audience and returns
`{"claim":null}` or `{"claim":{"receipt":{...},"lease_id":"UUID"}}`.
The receipt contains `schema_version`, `id`, `submission`, `authorized_at`, and
an `identity` object. That object contains `issuer`, `subject`, `repository_id`,
`owner_id`, `repository`, `ref`, `commit`, `workflow`, and `run_id`. IDs are stored
as strings. GitHub run identifiers use `run_id:run_attempt`; GitLab identifiers
use `pipeline_id:job_id`.

To complete a claim, send `POST /v1/claims/<id>/complete` with
`{"lease_id":"UUID","status":"published"}`. The `status` field can also be
`rejected`, and an optional `detail` field can contain up to 1,024 characters.
The response contains the submission ID and its resulting status, for example
`{"id":"UUID","status":"published"}`. The request must use the current lease
and the identity that claimed it. Repeating the same completion request has no
additional effect. Stored validator `detail` is private; public explanations use
fixed messages and never copy raw validator output.

The public endpoint `GET /v1/submissions/<id>` returns `id` and `status`. A rejected
or failed request also includes a safe `detail` and, when available, a
`validator_run` URL derived from its authenticated validator identity.
The possible statuses are `pending`, `validating`, `published`, `rejected`, and
`failed`. The health endpoint `GET /health` returns `{"status":"ok"}`. Responses
disable caching. The API does not enable CORS for browser-based publication clients.

GitHub identity and validator authorization failures include a fixed `code` that
identifies the failed check without revealing claim values. The Python client
accepts only its closed list of codes and renders local descriptions; it never
prints response text. An unrecognized, oversized, or non-JSON error body is reported
as `no recognized registry error code`, so edge failures remain distinguishable
from recognized Worker authorization failures.

## Persistence and retry rules

The prototype stores its queue in one SQLite Durable Object. Each transaction
associates both the token's `(issuer, jti)` and the version's
`(issuer, repository ID, tag, asset)` with the normalized submission. Neither
identity can later be used to submit different data. A new token or a change to
the plugin kind or checksum cannot replace an existing version. Repeating the
same normalized submission returns the original receipt.

Each publisher can have at most ten requests that are pending or being validated.
It can make at most thirty submission attempts with fresh tokens per UTC hour.
Across all publishers, at most 1,000 requests can be pending or being validated.
Token replay records expire, and hourly rate-limit records are removed after
48 hours. The associations between versions and submissions remain stored.

Each new submission also creates a dispatch notification in the same transaction.
The Worker saves a Durable Object alarm before accepting the submission, then uses
that alarm to notify GitHub. Failed notifications are retried at increasing intervals,
up to five attempts or one hour. Repeating a pending, active, rejected, or published
submission does not create another notification or reset its retry schedule. Claiming a request removes its pending
notification. Scheduled workflow runs remain available if dispatch fails.

Workers Logs records one `registry-dispatch-failed` event per failed notification
attempt, with the submission ID, attempt number, failure stage and fixed code.
HTTP refusals include the status code. Credentials, upstream response bodies and
exception messages are never logged. Invocation logs are disabled; use these
explicit diagnostics to distinguish signing, token issuance, token scope and
workflow dispatch failures without exposing publisher requests.

When the registry workflow claims a request, it receives a lease lasting sixty
minutes. If the lease expires before completion, a later poll can claim the request
again. After three expired attempts, the next poll marks the request `failed`.
Infrastructure, upstream availability, and unclassified failures leave the lease
eligible for retry. The admission report marks proven invalid artifact bytes or
identity as a deterministic rejection. The workflow completes those requests as
`rejected` immediately, using a report bound to the claimed submission ID.

Rejected and published requests cannot be reopened. A publisher can recover a
`failed` request by submitting identical metadata with a fresh OIDC token. This
preserves the original receipt, records the recovery and prior validator identity,
clears the obsolete lease, resets the three-attempt budget, and adds a durable
dispatch notification. Recovery respects the same hourly and pending quotas; token
replay cannot trigger it. Publishing different bytes requires a new tag.
If scheduled workflows stop running, both validation and expiry processing
are delayed. A delayed job may finish after its lease expires; its completion
request then fails. The next attempt must handle any records already published
without changing them. Only a workflow with an authenticated claim can publish a
registry entry.

## Local checks and deployment

```sh
bun install --frozen-lockfile
bun run typecheck
bun test
bun run check:bundle
```

The tests verify JWT signatures and authentication failures. They also run the
queue's SQL against Bun SQLite to check replay protection, version immutability,
quotas, and lease transitions. The `check:bundle` command builds the Worker without
starting a server or deploying it. These checks do not replace an end-to-end test
that submits and validates a plugin using tokens issued by a real CI provider.

Before deploying, check the account, domain, repository ID, and workflow reference
in `wrangler.jsonc`. Then run `bunx wrangler deploy` from this directory. The Worker
uses `registry.actionlint.kjanat.dev`. Access through workers.dev and preview URLs is
disabled.

References: [Cloudflare SQLite storage](https://developers.cloudflare.com/durable-objects/api/sqlite-storage-api/),
[GitHub OIDC](https://docs.github.com/en/actions/reference/security/oidc),
[GitLab ID tokens](https://docs.gitlab.com/ci/secrets/id_token_authentication/).
