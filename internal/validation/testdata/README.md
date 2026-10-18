# Runtime fixtures

These fixtures were copied unchanged from [kjanat/actionlint at abe9f897d9d8d019c572142626b8f055352f2ac7](https://github.com/kjanat/actionlint/tree/abe9f897d9d8d019c572142626b8f055352f2ac7).
They are licensed under the MIT license. `ACTIONLINT-LICENSE` retains the upstream copyright notice.

| Local file                       | Upstream path                                                        |
| -------------------------------- | -------------------------------------------------------------------- |
| `core-echo.wasm`                 | `internal/plugins/core/testdata/engines/echo.wasm`                   |
| `core-missing-export.wasm`       | `internal/plugins/core/testdata/wat/missing-export.wasm`             |
| `component-no-sudo.wasm`         | `internal/plugins/component/testdata/plugins/no-sudo.wasm`           |
| `component-wrong-enum.wasm`      | `internal/plugins/component/testdata/contracts/wrong-enum.wasm`      |
| `component-wrong-record.wasm`    | `internal/plugins/component/testdata/contracts/wrong-record.wasm`    |
| `component-wrong-signature.wasm` | `internal/plugins/component/testdata/contracts/wrong-signature.wasm` |

The incompatible components export the expected interface name, but their exported
types do not match the contract. The tests must reject them before invoking them.

The host can load `core-echo.wasm`, but the registry rejects it because it omits
rule descriptors. The tests in `core_fixture_test.go` generate core fixtures that
can pass validation. One uses Ruff's registration format to test the bundled
backend without generic shell selectors.
