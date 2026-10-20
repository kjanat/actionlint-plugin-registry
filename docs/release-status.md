# Release status

Publication records and artifact digests remain immutable. Administrators can
append a separate status event for an accepted repository, exact version and
artifact SHA256. This never deletes or replaces published bytes.

| Status       | Consumer behavior                                                                                        |
| ------------ | -------------------------------------------------------------------------------------------------------- |
| `active`     | Eligible for selection. An explicit event restores a deprecated or withdrawn release.                    |
| `deprecated` | Eligible, with a warning containing the reason and optional replacement.                                 |
| `withdrawn`  | Excluded from fresh selections, including exact-version installs. Existing matching locks remain usable. |
| `revoked`    | The exact digest must not execute, including cached copies and other tags using those bytes. Permanent.  |

No event means active. Revocation cannot be reversed; publish corrected bytes as
a different release. An `active` event cannot remove a digest revocation.
Consumers must retain known revocations when offline. Fetching fresh statuses
and handling unavailable status servers are host responsibilities; the registry
cannot retroactively notify an offline host that has never seen a revocation.

## Requesting a change

Publishers request a reviewed status change in the registry repository, providing
the canonical repository, exact tag, accepted digest, desired status, public
reason and optional replacement. A maintainer verifies publisher ownership and
the evidence before approving the request. Security revocations can also
originate with registry administrators.

This is not a publisher self-service API. The publication Worker's OIDC API
continues to handle publication only. A PR must not invent administrator audit
records: the approved change is applied by the administrator workflow.

The **Change release status** workflow runs only on `master`. It verifies that
the dispatcher has current `admin` or `maintain` permission on the registry,
records the actor's GitHub numeric ID, login, workflow reference and run ID,
and serializes writes with publication. Another user cannot rerun the original
dispatch as that actor. Inputs travel through environment variables and quoted
arguments. The registry App receives write access only after the event and
generated catalogue pass validation. Pages publication follows the commit.

The local implementation command is:

```sh
go run ./cmd/registry status \
  --plugin github.com/owner/plugin --version v1.0.0 \
  --sha256 <accepted-sha256> --status withdrawn \
  --reason 'Incorrect analysis for supported scripts; see issue #123' \
  --replacement github.com/owner/plugin@v1.0.1 \
  --actor-file /path/to/trusted-workflow-actor.json
```

Like `admit --receipt`, this command operates on trusted local data; an actor
JSON file is not cryptographic authorization. Do not expose it as an API or
publish records supplied directly by an untrusted requester.

## Storage and export

Audit events are append-only:

```text
registry/status/<plugin-identity-key>/<sha256(exact-tag)>/<sequence>.json
```

Each event contains a positive consecutive `sequence` and the release status
fields below. Loading validates release/digest identity, actor structure,
monotonic timestamps, sequence continuity and terminal revocation. Accepted
version files are never rewritten.

Every export writes `statuses.json`, even when empty:

```json
{ "schema_version": 1, "releases": [] }
```

The root catalogue advertises `"statuses":"statuses.json"`. Consumers may use
the fixed same-origin path. Each modified release has one current entry:

```json
{
  "name": "github.com/owner/plugin",
  "version": "v1.0.0",
  "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "status": "withdrawn",
  "reason": "Incorrect analysis for supported scripts",
  "replacement": "github.com/owner/plugin@v1.0.1",
  "changed_at": "2026-10-20T12:00:00Z",
  "actor": {
    "kind": "registry-admin",
    "id": "123",
    "name": "maintainer",
    "workflow": "owner/registry/.github/workflows/status.yml@refs/heads/master",
    "run_id": "100:1"
  }
}
```

`replacement` is optional advisory text, not an automatic dependency rewrite.
The [status schema](../schemas/statuses.schema.json) describes the projection.
The complete transition history remains in the registry's audited event files.
The local `resolve` command refuses withdrawn releases and revoked digests, and
warns for deprecated releases.
