# Contributing

Keep deployment credentials, GitHub App keys, and OIDC tokens out of Git.

## Validation and development

See the [metadata and execution checks](docs/metadata.md) for details.
Downloads must not exceed 64 MiB, and decompressed WASM must not exceed 128 MiB.
The validator runs in a child process without inherited credentials. Its guest
limits do not provide the isolation of an operating-system sandbox.

```sh
mise run setup
mise run check
```

The registry workflow uses `admit --receipt` and `merge --candidate` to pass data
between trusted jobs. These commands are not public submission APIs. A receipt file
does not contain a verifiable signature; the workflow must obtain it from the
Worker over HTTPS after authenticating with OIDC. Never pass a receipt supplied by
a publisher to the job that writes registry records. The `inspect` command checks
compatibility without authorizing publication.

To deploy the Worker, run `bunx wrangler deploy` from `worker/`. It uses a custom
domain and a SQLite Durable Object. Access through workers.dev and preview URLs is
disabled. None of the checks listed above requires a development server.
