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

The checker supports the core ABI and component contract understood by its host.
Unknown contracts return `unsupported`; understood contracts that fail validation
return `failed`. Neither result rejects a release from the registry.

When updating the host dependency, update `HostCommit`, contract constants, and
fixtures together. Run the module's tests and linter before using the new checker.
