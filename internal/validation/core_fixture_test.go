package validation

import (
	"os"
	"path/filepath"
	"testing"
)

const coreMetadata = `{"metadata_version":1,"abi_version":1,"transport":"wasm-core-json","id":"registry-smoke","name":"Registry smoke","description":"Registry fixture","license":"MIT","repository":"https://github.com/example/plugin","plugin_version":"1.0.0","interfaces":{"check":[{"version":1,"export":"check"}],"validate_config":[{"version":1,"export":"validate_config"}],"metadata":[{"version":1,"export":"metadata"}]},"engine":{"name":"smoke"},"capabilities":{"script_shells":["bash"]},"rules":[{"name":"smoke","description":"Smoke test rule"}]}`

// coreFixture encodes an actual minimal core module, avoiding an external WAT
// compiler in the test dependency graph. alloc returns scratch memory; check
// and validate_config return a successful JSON response. metadata returns its
// supplied record. Runtime execution still traverses the real host ABI.
func coreFixture(t *testing.T, metadata string) string {
	t.Helper()
	return coreFixtureResponse(t, metadata, `{"ok":true,"diagnostics":[]}`)
}

func coreFixtureResponse(t *testing.T, metadata, response string) string {
	t.Helper()
	module := []byte{0, 'a', 's', 'm', 1, 0, 0, 0}
	section := func(id byte, data []byte) {
		module = append(module, id)
		module = append(module, unsignedLEB(uint64(len(data)))...)
		module = append(module, data...)
	}
	section(1, []byte{5, 0x60, 0, 1, 0x7f, 0x60, 1, 0x7f, 1, 0x7f, 0x60, 1, 0x7f, 0, 0x60, 2, 0x7f, 0x7f, 1, 0x7e, 0x60, 0, 1, 0x7e})
	section(3, []byte{6, 0, 1, 2, 3, 3, 4})
	section(5, []byte{1, 0, 1}) // One page of memory.
	exports := []byte{7}
	for i, name := range []string{"abi_version", "alloc", "dealloc", "check", "validate_config", "metadata", "memory"} {
		exports = append(exports, byte(len(name)))
		exports = append(exports, name...)
		if name == "memory" {
			exports = append(exports, 2, 0)
		} else {
			exports = append(exports, 0, byte(i))
		}
	}
	section(7, exports)
	constant := func(opcode byte, value int64) []byte {
		body := append([]byte{0, opcode}, signedLEB(value)...)
		return append(body, 0x0b)
	}
	code := []byte{6}
	for _, body := range [][]byte{constant(0x41, 1), constant(0x41, 8192), {0, 0x0b}, constant(0x42, 2048<<32|int64(len(response))), constant(0x42, 2048<<32|int64(len(response))), constant(0x42, 1024<<32|int64(len(metadata)))} {
		code = append(code, unsignedLEB(uint64(len(body)))...)
		code = append(code, body...)
	}
	section(10, code)
	data := []byte{2}
	for i, text := range []string{metadata, response} {
		data = append(data, 0, 0x41)
		data = append(data, signedLEB(int64(1024*(i+1)))...)
		data = append(data, 0x0b)
		data = append(data, unsignedLEB(uint64(len(text)))...)
		data = append(data, text...)
	}
	section(11, data)
	section(0, metadataSection(metadata))
	path := filepath.Join(t.TempDir(), "core.wasm")
	if err := os.WriteFile(path, module, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func unsignedLEB(value uint64) []byte {
	var result []byte
	for {
		b := byte(value & 0x7f)
		value >>= 7
		if value == 0 {
			return append(result, b)
		}
		result = append(result, b|0x80)
	}
}

func signedLEB(value int64) []byte {
	var result []byte
	for {
		b := byte(value & 0x7f)
		value >>= 7
		if value == 0 && b&0x40 == 0 || value == -1 && b&0x40 != 0 {
			return append(result, b)
		}
		result = append(result, b|0x80)
	}
}
