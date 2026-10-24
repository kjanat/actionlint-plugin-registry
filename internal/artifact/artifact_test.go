package artifact

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func leb(value int) []byte {
	var result []byte
	for {
		b := byte(value & 127)
		value >>= 7
		if value != 0 {
			b |= 128
		}
		result = append(result, b)
		if value == 0 {
			return result
		}
	}
}
func custom(name string, data []byte) []byte {
	section := append(leb(len(name)), []byte(name)...)
	section = append(section, data...)
	result := append([]byte{0}, leb(len(section))...)
	return append(result, section...)
}
func declarationJSON(kind string) []byte {
	contract := `"abi_version":999,"transport":"wasm-core-json"`
	if kind == "component" {
		contract = `"transport":"wasm-component","interface":"example:future/analyzer@9.0.0","contract_sha256":"` + strings.Repeat("a", 64) + `"`
	}
	return []byte(`{"metadata_version":1,"id":"example","name":"Example","description":"A plugin","license":"MIT","repository":"https://github.com/owner/plugin","plugin_version":"1.0.0",` + contract + `,"descriptor":{"requires":{"future":"shape"}},"future":{"number":9007199254740993,"descriptor":{"new-fact":true}}}`)
}
func wasm(kind string, data []byte) []byte {
	header := []byte{0, 97, 115, 109, 1, 0, 0, 0}
	if kind == "component" {
		header = []byte{0, 97, 115, 109, 13, 0, 1, 0}
	}
	return append(header, custom("actionlint.plugin", data)...)
}
func inspectBytes(t *testing.T, data []byte) (Declaration, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plugin.wasm")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return Inspect(t.Context(), path)
}
func TestUnknownContractsRemainDeclarations(t *testing.T) {
	for _, kind := range []string{"core", "component"} {
		t.Run(kind, func(t *testing.T) {
			metadata := declarationJSON(kind)
			raw := wasm(kind, metadata)
			result, err := inspectBytes(t, raw)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(raw)
			if result.Kind != kind || result.SHA256 != hex.EncodeToString(digest[:]) || !bytes.Equal(result.Metadata, metadata) {
				t.Fatalf("lost declaration: %+v", result)
			}
			if err = result.Validate(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(result.Metadata), "9007199254740993") {
				t.Fatal("unknown numeric data lost")
			}
			var compressed bytes.Buffer
			writer := gzip.NewWriter(&compressed)
			if _, err = writer.Write(raw); err != nil {
				t.Fatal(err)
			}
			if err = writer.Close(); err != nil {
				t.Fatal(err)
			}
			result, err = inspectBytes(t, compressed.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			digest = sha256.Sum256(compressed.Bytes())
			if result.SHA256 != hex.EncodeToString(digest[:]) {
				t.Fatal("checksum did not bind distributed bytes")
			}
		})
	}
}
func TestMalformedDeclarationsRejected(t *testing.T) {
	valid := wasm("core", declarationJSON("core"))
	for name, data := range map[string][]byte{
		"header":             []byte("not wasm"),
		"missing":            {0, 97, 115, 109, 1, 0, 0, 0},
		"duplicate":          append(bytes.Clone(valid), custom("actionlint.plugin", declarationJSON("core"))...),
		"truncated":          valid[:len(valid)-1],
		"overflow":           {0, 97, 115, 109, 1, 0, 0, 0, 0, 255, 255, 255, 255, 16},
		"utf8":               wasm("core", []byte{255}),
		"duplicate-key":      wasm("core", bytes.Replace(declarationJSON("core"), []byte(`"metadata_version":1`), []byte(`"metadata_version":1,"metadata_version":1`), 1)),
		"oversized-metadata": wasm("core", bytes.Repeat([]byte(" "), MaxMetadata+1)),
		"invalid-gzip":       {31, 139, 0},
		"empty":              wasm("core", nil),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := inspectBytes(t, data); err == nil {
				t.Fatal("malformed declaration accepted")
			}
		})
	}
}

func TestExistingIdentityGrammarPreserved(t *testing.T) {
	for _, id := range []string{"vendor/plugin", ".example", "-example", "_example"} {
		metadata := bytes.Replace(declarationJSON("core"), []byte(`"id":"example"`), []byte(`"id":"`+id+`"`), 1)
		result, err := inspectBytes(t, wasm("core", metadata))
		if err != nil || result.PluginID != id {
			t.Fatalf("existing identity rejected: %s, %v", id, err)
		}
	}
}

func TestMetadataAllowsCaseVariantExtensions(t *testing.T) {
	for name, extension := range map[string]string{
		"typed extensions":  `"Name":{"future":"extension"},"Description":["future"],"License":{"id":"future"}`,
		"string extensions": `"Name":"Future name","Description":"Future description","License":"Future license"`,
	} {
		t.Run(name, func(t *testing.T) {
			metadata := []byte(strings.TrimSuffix(string(declarationJSON("component")), "}") + "," + extension + "}")
			result, err := inspectBytes(t, wasm("component", metadata))
			if err != nil {
				t.Fatal(err)
			}
			if err := result.Validate(); err != nil {
				t.Fatalf("admission rejected case-variant extension fields: %v", err)
			}
			if !bytes.Equal(result.Metadata, metadata) {
				t.Fatal("extension metadata changed")
			}
		})
	}
}
