package validation

import (
	"actionlint.kjanat.dev/v2/plugin"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"actionlint.kjanat.dev/v2/analyzer"
	"actionlint.kjanat.dev/v2/rule"
)

const componentMetadata = `{"metadata_version":1,"transport":"wasm-component","id":"no-sudo","name":"No sudo","description":"Registry component fixture","license":"MIT","repository":"https://github.com/example/plugin","plugin_version":"1.0.0","interface":"kjanat:actionlint/analyzer@0.1.0","contract_sha256":"7ce5cd5b7c62542d8b3b9860cdc5b1fa050ee50a87d068765b5050261861dfa7","descriptor":{"name":"no-sudo","description":"Check runner privilege assumptions","category":"portability","subcategory":"scripts","maturity":"stable","provider":"example","explanation":"Self-hosted runners may not provide sudo.","help":"Run without privilege elevation or explicitly prepare the runner.","fix_availability":"none","options_schema":{},"requires":["scripts","invocations"]}}`

func metadataSection(metadata string) []byte {
	name := "actionlint.plugin"
	payload := unsignedLEB(uint64(len(name)))
	payload = append(payload, name...)
	return append(payload, metadata...)
}

// Publisher packaging is modeled only for these test fixtures. Admission never
// adds a missing section or manufactures declarations for submitted binaries.
func packagedFixture(t *testing.T, name, metadata string) string {
	t.Helper()
	module, err := os.ReadFile(filepath.Join("testdata", name+".wasm"))
	if err != nil {
		t.Fatal(err)
	}
	return appendFixtureMetadata(t, module, metadata)
}

func appendFixtureMetadata(t *testing.T, module []byte, metadata string) string {
	t.Helper()
	section := metadataSection(metadata)
	module = append(module, 0)
	module = append(module, unsignedLEB(uint64(len(section)))...)
	module = append(module, section...)
	path := filepath.Join(t.TempDir(), "packaged.wasm")
	if err := os.WriteFile(path, module, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAdmissionRequiresEmbeddedMetadata(t *testing.T) {
	for _, name := range []string{"component-no-sudo", "core-echo"} {
		_, err := Validate(t.Context(), filepath.Join("testdata", name+".wasm"))
		if err == nil || !strings.Contains(err.Error(), "custom section") {
			t.Fatalf("%s: expected missing metadata, got %v", name, err)
		}
	}
}

func TestEmbeddedComponentDescriptorMustMatchRuntime(t *testing.T) {
	for _, metadata := range []string{
		strings.Replace(componentMetadata, "Check runner privilege assumptions", "Different description", 1),
		strings.Replace(componentMetadata, `"scripts","invocations"`, `"scripts","dependencies"`, 1),
		strings.Replace(componentMetadata, `"options_schema":{}`, `"options_schema":{"type":"object"}`, 1),
	} {
		_, err := Validate(t.Context(), packagedFixture(t, "component-no-sudo", metadata))
		if err == nil || !strings.Contains(err.Error(), "differs from runtime") {
			t.Fatalf("expected static/runtime mismatch, got %v", err)
		}
	}
}

func TestPreservesCompleteEmbeddedMetadata(t *testing.T) {
	metadata := strings.Replace(componentMetadata, `"metadata_version":1`, `"metadata_version":1,"publisher_extra":{"nested":["preserved",123]}`, 1)
	result, err := Validate(t.Context(), packagedFixture(t, "component-no-sudo", metadata))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result.Metadata, []byte(metadata)) || result.Repository != "https://github.com/example/plugin" || result.PluginVersion != "1.0.0" || result.PolicyVersion != "1" || !strings.Contains(result.Contract, componentContractDigest) {
		t.Fatalf("lost embedded evidence: %+v", result)
	}
}

func TestMalformedMetadataSectionsRejected(t *testing.T) {
	for _, metadata := range []string{`null`, `{`, `{"id":"a","id":"b"}`, strings.Repeat(" ", (1<<20)+1)} {
		if _, err := Validate(t.Context(), packagedFixture(t, "component-no-sudo", metadata)); err == nil {
			t.Fatal("invalid or oversized custom section accepted")
		}
	}
	path := packagedFixture(t, "component-no-sudo", componentMetadata)
	module, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Validate(t.Context(), appendFixtureMetadata(t, module, componentMetadata)); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate custom section: %v", err)
	}
}

func TestCoreStaticRuntimeMismatch(t *testing.T) {
	path := coreFixture(t, coreMetadata)
	module, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The data segment precedes the custom section. Change runtime metadata only.
	module = bytes.Replace(module, []byte(`"name":"Registry smoke"`), []byte(`"name":"Registry other"`), 1)
	if err := os.WriteFile(path, module, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Validate(t.Context(), path); err == nil || !strings.Contains(err.Error(), "metadata") {
		t.Fatalf("core static/runtime mismatch: %v", err)
	}
}

func TestEmptyRequiredFactsAreEquivalent(t *testing.T) {
	descriptor := rule.Descriptor{Name: "example", Description: "Example"}
	declared := componentDeclaration{Descriptor: descriptor, Requires: []analyzer.Fact{}}
	if err := declared.matches(analyzer.Descriptor{Descriptor: descriptor}); err != nil {
		t.Fatal(err)
	}
	if declared.Requires == nil {
		t.Fatal("comparison mutated declaration")
	}
}

func TestUnknownContractCheckedBeforeHostSpecificFields(t *testing.T) {
	for _, info := range []plugin.ArtifactInfo{
		{Kind: plugin.ComponentArtifact, Metadata: []byte(`{"transport":"wasm-component","interface":"example:future/analyzer@2.0.0","contract_sha256":"future","abi_version":{"unrelated":"extension"},"descriptor":{"requires":{"new":"shape"}}}`)},
		{Kind: plugin.CoreArtifact, Metadata: []byte(`{"transport":"wasm-core-json","abi_version":999,"interface":{"unrelated":"extension"},"interfaces":{"check":"new-shape"}}`)},
	} {
		if _, err := inspectMetadata(t.Context(), info); !errors.Is(err, ErrUnsupportedContract) {
			t.Fatalf("unknown contract became runtime failure: %v", err)
		}
	}
}
