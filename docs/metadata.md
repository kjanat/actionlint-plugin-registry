# Embedded plugin metadata

Version 1 of the admission policy requires each plugin to contain exactly one
**top-level** `actionlint.plugin` WASM custom section. The section must contain a
JSON object no larger than 1 MiB. The selected actionlint host rejects duplicate
sections, duplicate JSON members, malformed section headers, and invalid UTF-8.
Metadata in a component's nested core module does not describe the component.
The registry never inserts missing metadata into a publisher's artifact.

Both formats require `metadata_version: 1`, `id`, `name`, `description`, `license`,
`repository`, and `plugin_version`. The `repository` field must contain the canonical
HTTPS URL of the repository identified by the authenticated release. The plugin
version must match the release tag after removing at most one leading lowercase
`v` from each. The registry records the license declared by the publisher; it does
not independently audit that declaration.

Each accepted version record retains the complete original JSON, including fields
the registry does not recognize. A capability declaration describes what the
publisher claims the plugin supports. Validation checks only the behavior described
below.

## Core modules

Use actionlint's existing metadata format for the core JSON ABI:

- Set `abi_version` to `1` and `transport` to `"wasm-core-json"`.
- Declare version 1 implementations in `interfaces.check`,
  `interfaces.validate_config`, and `interfaces.metadata`.
- Supply valid rule descriptors and an engine configuration that the host can register.
- Include a valid `config_schema` when `capabilities.config_schema` is `true`.

The host compares the entire embedded object with the value returned by
`metadata()`, including fields its Go metadata types do not recognize. JSON member
order and whitespace do not affect the comparison. The validator also registers
configuration schemas with the application and validates the default options.
Schemas cannot fetch remote resources.

## Components

The registry requires components to include the metadata shown below. This is an
additional registry requirement; it is not part of the existing WIT contract.
The host separately checks that the component's exported types match that contract.
Publishers must embed the metadata when packaging the component.

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

The `descriptor` object uses the JSON fields of the host's public `rule.Descriptor`
type. Its additional `requires` field lists the WIT facts the component needs.
The object must match the complete result returned by `describe()`, including rule
metadata, examples, the options schema, and required facts. The validator uses the
public Go descriptor type to normalize optional empty fields and rejects unsupported
fields. It compares the descriptors before invoking analysis.

The contract checksum above was generated from the canonical WIT definition at
actionlint commit `abe9f897d9d8d019c572142626b8f055352f2ac7`. Matching the interface
name alone does not establish compatibility. When upgrading the host, update the
selected commit, contract checksum, metadata requirements, and compatibility
fixtures together.

## Checks and limits

Each validation runs in a new child process without inherited credentials. The
process has a 45-second deadline, and the parent limits how much standard output
and standard error it retains. Each guest call has a 10-second deadline, and the
host limits guest linear memory. `GOMEMLIMIT=512MiB` sets a target for Go's garbage
collector; it does not impose an operating-system memory limit.

The validator checks embedded metadata, ABI or WIT compatibility, and agreement
between embedded metadata and runtime responses. It also checks application
registration, default configuration, repeated analysis of sample inputs, diagnostic
positions, and fixes. The Ruff sample must produce F821, and the ShellCheck sample
must produce SC2086. These samples do not establish that an analyzer behaves
correctly for every input.

`go run ./cmd/registry inspect --artifact plugin.wasm` performs local compatibility
checks only. It does not authorize or publish a release.
