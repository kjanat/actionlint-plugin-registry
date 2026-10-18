# Embedded plugin metadata

Admission policy 1 requires exactly one **top-level** `actionlint.plugin` WASM
custom section containing a JSON object, at most 1 MiB. The pinned host rejects
duplicate sections, duplicate JSON members, malformed framing and invalid UTF-8.
Metadata in a component's nested core module does not describe the component.
The registry never inserts missing metadata into a publisher's artifact.

Both formats require `metadata_version: 1`, `id`, `name`, `description`, `license`,
`repository` (canonical HTTPS repository URL), and `plugin_version`. The authenticated
release repository must match `repository`; version and tag must match after
removing at most one leading lowercase `v` from each. License is a publisher
declaration, not an independent license audit.

The complete original JSON is retained in each accepted version record, including
unknown discovery fields. Claimed capabilities are declarations; acceptance does
not prove the implementation's behavior beyond the documented checks.

## Core modules

Use the existing actionlint core JSON ABI metadata object, unchanged:

- `abi_version: 1`, `transport: "wasm-core-json"`.
- Explicit version-1 implementations in `interfaces.check`,
  `interfaces.validate_config`, and `interfaces.metadata`.
- Valid rule descriptors and usable engine/shell registration.
- A valid `config_schema` when `capabilities.config_schema` is true.

The host compares the **whole** embedded object with runtime `metadata()`,
including fields unknown to its Go metadata projection. JSON object order and
whitespace are immaterial. Configuration schemas also traverse actual application
registration and default-option validation; remote schema fetches are forbidden.

## Components

This is a **registry metadata profile**, not an existing WIT requirement. The
component must still pass the pinned host's structural WIT validation. Publishers
must embed this profile during packaging; components without it are rejected.

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
  "interface": "kjanat:actionlint/analyzer@0.1.0",
  "contract_sha256": "a0096f562ab3da46b30d0f025ad1c6ed0d46e5b7ea1a53660b3214e3270fcbee",
  "descriptor": {
    "name": "example-check",
    "description": "Checks an example workflow property",
    "requires": ["source"]
  }
}
```

`descriptor` uses the host's public `rule.Descriptor` JSON fields plus `requires`
with the WIT fact names. It must match the complete runtime `describe()` result,
including rule metadata, examples, options schema and required facts. Optional
empty fields normalize through the public Go descriptor. Unsupported descriptor
members are rejected. The comparison happens before analysis invocations.

The contract digest above is the generated canonical WIT digest at host commit
`abe9f897d9d8d019c572142626b8f055352f2ac7`. An interface name alone is insufficient.
Host upgrades must update the pin, digest, profile and compatibility fixtures together.

## Checks and limits

The validator runs a fresh child process without inherited credentials, with a
45-second outer deadline and bounded stdout/stderr. Individual guest calls have
10-second deadlines; guest linear memory is bounded. `GOMEMLIMIT=512MiB` is a Go GC
target, not an OS memory limit.

Checks include static metadata, ABI/WIT structure, static/runtime consistency,
application registration, default configuration, repeated sample invocations,
diagnostic positions and fixes. Known Ruff/ShellCheck fixtures additionally require
F821/SC2086 respectively. Samples do not prove general analyzer correctness.

`go run ./cmd/registry inspect --artifact plugin.wasm` performs local compatibility
checks only. It does not authorize or publish a release.
