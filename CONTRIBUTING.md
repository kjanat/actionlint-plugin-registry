# Contributing

Keep deployment credentials, GitHub App keys, and OIDC tokens out of Git.

## Registry aliases

Maintainers assign short names in [`registry/aliases.json`](registry/aliases.json).
These assignments are reviewed registry configuration; publisher metadata and
repository-name prefixes cannot claim an alias. Names use lowercase letters,
digits and hyphens, begin with a letter, and have at most 64 characters.
An alias identifies the exact forge-qualified repository, not a plugin ID.

An unpublished target requires `"reserved": true`. Reservations do not create
catalogue entries: the alias appears on the existing `CatalogPlugin` only after
that exact repository has an accepted release. This reserves `ruff` without
claiming that Ruff has already been published. Multiple aliases may identify one
repository; each name has exactly one target. Export rejects duplicate JSON keys,
invalid names and unreserved unknown targets before writing the catalogue.

`registry export` reads this file by default; `--aliases PATH` selects another
curator-maintained assignment file. Alias changes never rewrite accepted release
records or change checksums. Reassignments need review because they change what
a new dependency selection resolves; existing consumers must retain their pins.

## Validation and development

See the [metadata and execution checks](docs/metadata.md) for details.
Downloads must not exceed 64 MiB, and decompressed WASM must not exceed 128 MiB.
Admission inspects declarations without executing plugins. Optional compatibility
checkers run in child processes without inherited registry credentials; this is
not an operating-system sandbox.

```sh
mise run setup
mise run check
```

The shared CLI uses Bun, TypeScript and `@kjanat/dreamcli`. `bun run typecheck` and
`bun run test` cover publication HTTP responses, package bytes, OIDC boundaries,
workflow authorization and recovery. `mise run setup` installs both CLI and Worker
dependencies. See [candidate and publication tooling](docs/publishing.md) for a
local rehearsal without tags or remote mutations.

The registry workflow uses `admit --store registry/plugins --candidate candidate/plugins`
and `merge --candidate` to pass data
between trusted jobs. These commands are not public submission APIs. A receipt file
does not contain a verifiable signature; the workflow must obtain it from the
Worker over HTTPS after authenticating with OIDC. Never pass a receipt supplied by
a publisher to the job that writes registry records. The `inspect` command reads
discovery metadata. The separate `check` command records host compatibility without
authorizing publication. Admission reads existing registrations and writes only the
new delta; publication repeats conflict checks against the latest store. Both
commands produce reports that distinguish deterministic rejection from retryable
infrastructure failures. Recovery acts only on a report matching its authenticated
submission and lease.

## Worker deployment

From the repository root, after configuring [Cloudflare and GitHub App credentials](docs/github-app.md#configure-the-deployment):

```sh
mise run deploy:worker
```

This runs `check:worker` before deploying through the project's Wrangler dependency.
For a bundle check without deployment, run `bun run --cwd worker check:bundle`.
The package also exposes `bun run deploy` from `worker/` for direct Wrangler use.

The Worker uses a custom domain and a SQLite Durable Object. Access through
workers.dev and preview URLs is disabled. None of the checks requires a development server.
