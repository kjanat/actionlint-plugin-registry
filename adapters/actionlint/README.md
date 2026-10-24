# actionlint compatibility checker

This independent Go module owns the actionlint SDK dependency and runtime fixtures.
The registry's admission service does not import or execute it.

Build it from the repository root with:

```sh
go -C adapters/actionlint build -o ../../actionlint-checker .
```

The executable accepts one local artifact path and writes one JSON compatibility
report to stdout. Run it through the registry's `check` command to verify the
accepted artifact's checksum, isolate inherited credentials, and store the report.
The process boundary does not provide an operating-system sandbox.

The checker uses actionlint v2 at `16768627b0e9cefbb28781c5793ba9f5d9689c5c`.
It supports core ABI 1 and the analyzer, script-engine and runner-provider WIT roles.
The host validates exported component types. A WIT source checksum also covers
documentation comments, so a different checksum alone does not reject an analyzer.
Script engines validate default options and repeated diagnostic calls. Runner
providers resolve a selector with missing evidence for each declared target;
vendor-specific snapshot behavior belongs in the publisher's candidate fixtures.
Unknown contracts return `unsupported`; understood contracts that fail validation
return `failed`. Neither result rejects a release from the registry.

When updating the host dependency, update `HostCommit`, contract constants, and
fixtures together. Run the module's tests and linter before using the new checker.
