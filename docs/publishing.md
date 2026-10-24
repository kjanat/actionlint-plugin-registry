# Check candidates, then publish

Build and package once. Run the candidate check on those bytes in PR CI. The tag
workflow downloads the package and receipt, publishes the same files, and checks
their delivery. Testing a candidate requires no tag, GitHub release, OIDC credential
or registry write access.

## Local candidate check

From a registry checkout with Bun and its Go toolchain installed:

```sh
bun install --frozen-lockfile
go build -o registry ./cmd/registry
bun scripts/cli.ts check \
  --registry ./registry --actionlint /path/to/actionlint \
  --artifact /path/to/artifacts/plugin.wasm \
  --fixture /path/to/plugin/installation.ts \
  --store registry/plugins --report candidate.json
```

The check uses the registry inspector and catalogue exporter, including existing
plugin/version identity checks. It installs from a temporary local catalogue into
a fresh project and cache. Actionlint creates the configuration, lockfile and cache
through `plugin install`; the harness does not write installed state itself.

The fixture runs with the installed plugin. The check then removes the temporary
catalogue, repeats the fixture, verifies that the lockfile stayed unchanged, removes
the cached Wasm and requires a plugin error. Host processes receive a fresh home
and cache, no inherited credentials, and blocked HTTP proxies. The temporary
directories are removed afterward.

`candidate.json` records the artifact digest and metadata, host binary digest and
version, fixture digest, named behavioral checks, installed version/digest and
offline outcomes. A failed run cannot leave an old success receipt in place.
Publisher authorization still requires OIDC. Store the receipt with the exact
package in your workflow artifacts.

To inspect and export a candidate without running a host:

```sh
./registry candidate --artifact plugin.wasm --store registry/plugins --output trial
```

`trial` must be a new directory. Its catalogue is marked as candidate data and uses
relative artifact paths. It cannot be merged into accepted registration records.
Use the full check above to verify installation and behavior.

## Behavioral fixture

Provide a TypeScript module exporting functions with these signatures:

```ts
export declare function prepare(context: { directory: string }): Promise<void>;

export declare function verify(context: {
  directory: string;
  run(args: string[], expected?: number): Promise<string>;
  runResult(
    args: string[],
    expected?: number,
  ): Promise<{ stdout: string; stderr: string }>;
}): Promise<{ checks: string[]; probe: string[] }>;
```

`prepare` writes workflow inputs and plugin options before installation. `verify`
asserts diagnostics, locations and relevant behavior, then returns the names of
the checks that passed. For source-following plugins, assert a finding that changes when a sourced
file changes. Checking only that a process exits or returns an array misses that
behavior. The probe must run `check --json`; the harness first requires successful
analysis, then uses the same probe after cache removal.

`run` returns stdout; `runResult` also exposes stderr for assertions about expected failures.
Both check the exit status, defaulting to zero.

Fixtures are trusted publisher code, run in separate child processes. The clean
environment is not an operating-system sandbox. Do not run an untrusted fixture
in a privileged publication job. Guest plugins still run through the host's Wasm
runtime and capability controls.

## GitHub Actions

[`check/action.yml`](../check/action.yml) builds the registry CLI and runs the local
candidate check. Supply `artifact`, `actionlint` and `fixture`; `report` defaults
to `candidate.json`. Its `sha256` output identifies the tested Wasm bytes.

[`publish/action.yml`](../publish/action.yml) accepts:

| Input                   | Purpose                                                                     |
| ----------------------- | --------------------------------------------------------------------------- |
| `directory`             | Packaged files, default `artifacts`; every file must appear in `SHA256SUMS` |
| `asset`                 | Distributed Wasm filename, default `plugin.wasm`                            |
| `sha256`                | Digest from the candidate check                                             |
| `candidate-receipt`     | Successful candidate receipt matching this artifact and release             |
| `actionlint`, `fixture` | Host executable and the same behavioral fixture for delivery checks         |
| `tag`, `repository`     | Existing tag and its public repository, defaulting to the workflow context  |
| `kind`                  | `core` or `component`, default `component`                                  |
| `notes-file`            | Optional UTF-8 release notes                                                |
| `receipt`               | Publication progress file, default `publication.json`                       |
| `timeout-seconds`       | Publication wait budget, default 1800                                       |

Use the same reviewed registry revision for both actions. The publishing job needs
`contents: write` and `id-token: write`, and must run on the existing release tag.
Supply the exact host executable and fixture used by the candidate check; their
digests are verified before any remote publication. Enable GitHub immutable
releases before the first publication. The action creates
a draft, uploads missing files, downloads and compares every asset, then makes the
release public and confirms its immutable status. Different bytes under an existing
asset name are an error; reruns never replace assets.

After OIDC submission, the action waits for `published`, then installs that exact
version from the public catalogue and runs the same fixture and offline checks.
The delivery check uses the actual release tag, including whether it starts with `v`.
Repository capitalization and optional version prefixes do not change candidate identity.
Upload `publication.json` even when the job fails: it records status and the
validator run. `installation.json` is written after successful delivery verification.

The root [`action.yml`](../action.yml) remains the smaller submission-only entry
point for publishers with an existing release workflow. It returns the submission
ID and current state; `pending` and `validating` require further observation.

## Resume and recovery

Observe a submission without uploading or submitting again:

```sh
bun scripts/cli.ts wait --id SUBMISSION_UUID --sha256 ARTIFACT_SHA256 \
  --receipt publication.json --timeout-seconds 1800
```

The waiter retries network failures, HTTP 408/429 and server errors until its
deadline, respecting `Retry-After`. It prints validator links while work is active.
An exhausted wait fails; it does not report the release as published.

The admission workflow rejects invalid identities or conflicting accepted versions.
Infrastructure failures release the exact claim for retry after 60 seconds, then
120 seconds. After three failed attempts, resubmit identical metadata with fresh
publisher OIDC to start a new attempt sequence. The original submission ID and
accepted bytes stay unchanged. If workflow cleanup cannot run, lease expiry remains
the fallback. See [admission recovery](github-app.md#verify-the-integration).

Run `mise run deploy:worker` from the registry checkout before enabling the workflow's
`consume recover` step; that step calls the Worker's `/v1/claims/:id/retry` endpoint.

Candidate checks test local artifact behavior and installation. GitHub permissions,
immutable-release settings and public catalogue delivery are checked during actual
publication; testing local bytes does not require invoking those remote mutations.
