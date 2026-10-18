# Embedded plugin metadata

Admission requires exactly one **top-level** `actionlint.plugin` WASM custom section
containing a JSON object of at most 1 MiB. Duplicate sections or JSON members,
malformed section lengths, invalid UTF-8, and missing metadata are rejected.
Metadata inside a component's nested core module does not describe the component.
The registry never changes a publisher's artifact.

Both formats require `metadata_version: 1`, `id`, `name`, `description`, `license`,
`repository`, and `plugin_version`. The repository must be the canonical HTTPS URL
of the authenticated release repository. The version must match the release tag
after removing at most one leading lowercase `v` from each. License information is
a publisher declaration, not an independent license audit.

The entire metadata object is retained, including unknown fields and numeric values.
The registry reads discovery fields; it does not deserialize rule descriptors,
configuration schemas, capabilities, or facts into a particular host's types.

## Core modules

Set `transport` to `"wasm-core-json"` and `abi_version` to a positive unsigned
32-bit integer. The registry records `wasm-core-json/<abi_version>` as the declared
contract. Admission does not require the registry's checker to support that ABI.

## Components

Set `transport` to `"wasm-component"`, `interface` to the interface identity, and
`contract_sha256` to the lowercase SHA-256 checksum of the canonical contract.
These are declarations. Admission does not establish that the exported types match
them; a compatible host's checker must verify that separately.

For example, a component can declare:

```json
{
  "metadata_version": 1,
  "id": "example-check",
  "name": "Example check",
  "description": "Checks an example workflow property",
  "license": "MIT",
  "repository": "https://github.com/owner/example-plugin",
  "plugin_version": "1.0.0",
  "transport": "wasm-component",
  "interface": "example:checks/analyzer@2.0.0",
  "contract_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "descriptor": {
    "name": "example-check",
    "future_fact": "workflow-declarations"
  }
}
```

The checksum above is illustrative. Unknown contracts are catalogued without a
compatibility claim.

## Admission limits

The distributed file may be at most 64 MiB. Gzip expansion is limited to 128 MiB.
Checksums always cover the distributed bytes, including compression. The inspector
validates container framing and discovery metadata. It does not validate WASM
instructions, verify WIT semantics, or execute guest code.

`go run registry.actionlint.kjanat.dev/cmd/registry@master inspect --artifact plugin.wasm` inspects declarations locally.
It does not authorize or publish a release.

## Optional compatibility checks

The [actionlint adapter](../adapters/actionlint/) checks the contract understood by
its own host dependency. Its checks cover ABI or WIT compatibility, embedded/runtime
metadata agreement, application registration, default configuration, repeated sample
analysis, diagnostic positions, and fixes. Its Ruff sample requires F821, and its
ShellCheck sample requires SC2086. These samples do not prove correctness for every input.

The registry's `check` command snapshots and verifies the accepted artifact before
starting an explicitly selected checker. The checker has a 45-second process
deadline and bounded stdout/stderr. The bundled adapter gives guest calls a
10-second deadline and limits guest linear memory. `GOMEMLIMIT` guides garbage
collection; it does not impose an operating-system memory limit. The checker
process has no inherited registry credentials, but it is not an OS sandbox.

The versioned [JSON report protocol](../schemas/compatibility.schema.json) records
`passed`, `failed`, or `unsupported` for a particular artifact, host revision, and
policy. A checker crash produces no compatibility evidence. Missing evidence means
untested. No outcome changes admission or an existing release record.
