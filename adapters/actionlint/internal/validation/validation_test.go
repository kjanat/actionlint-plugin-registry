package validation

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"actionlint.kjanat.dev/v2/analyzer"
	"actionlint.kjanat.dev/v2/diagnostic"
	"actionlint.kjanat.dev/v2/plugin"
	"actionlint.kjanat.dev/v2/rule"
)

func TestCompatibleComponent(t *testing.T) {
	result, err := Validate(t.Context(), packagedFixture(t, "component-no-sudo", componentMetadata))
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != "component" || result.PluginID != "no-sudo" || result.HostCommit != HostCommit || !slices.Contains(result.Checks, "repeated-sample") {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestComponentFindingUsesWorkflowCoordinates(t *testing.T) {
	component, err := plugin.Load(t.Context(), filepath.Join("testdata", "component-no-sudo.wasm"), plugin.Options{Timeout: callTimeout, MemoryPages: 1024})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := component.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	input, err := workflowInput(t.Context(), strings.Replace(sampleWorkflow, "printf", "sudo printf", 1))
	if err != nil {
		t.Fatal(err)
	}
	findings, err := component.Analyze(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Start != (diagnostic.Position{Line: 10, Column: 11}) {
		t.Fatalf("expected mapped sudo diagnostic: %+v", findings)
	}
	if err := componentFindings(findings, input, []string{"no-sudo"}); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeRejections(t *testing.T) {
	for _, name := range []string{"core-missing-export", "core-echo", "component-wrong-enum", "component-wrong-record", "component-wrong-signature"} {
		t.Run(name, func(t *testing.T) {
			metadata := componentMetadata
			if strings.HasPrefix(name, "core-") {
				metadata = coreMetadata
			}
			if _, err := Validate(t.Context(), packagedFixture(t, name, metadata)); err == nil {
				t.Fatal("invalid host contract accepted")
			}
		})
	}
}

func TestMalformedAndRemoteRejections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.wasm")
	if err := os.WriteFile(path, []byte("not wasm"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{path, filepath.Dir(path), "https://example.invalid/plugin.wasm"} {
		if _, err := Validate(t.Context(), input); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}

func TestCoreFixture(t *testing.T) {
	result, err := Validate(t.Context(), coreFixture(t, coreMetadata))
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != "core" || result.PluginID != "registry-smoke" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestCoreRejectsMetadataVersion(t *testing.T) {
	_, err := Validate(t.Context(), coreFixture(t, strings.Replace(coreMetadata, `"metadata_version":1`, `"metadata_version":2`, 1)))
	if err == nil || !strings.Contains(err.Error(), "metadata_version") {
		t.Fatalf("expected metadata version rejection, got %v", err)
	}
}

func TestCoreRejectsUnusableRegistration(t *testing.T) {
	for name, metadata := range map[string]string{
		"missing-engine":         strings.Replace(coreMetadata, `"engine":{"name":"smoke"},`, "", 1),
		"missing-selectors":      strings.Replace(coreMetadata, `"capabilities":{"script_shells":["bash"]},`, "", 1),
		"invalid-selector":       strings.Replace(coreMetadata, `"script_shells":["bash"]`, `"script_shells":["bash -e"]`, 1),
		"invalid-options-schema": strings.Replace(coreMetadata, `"description":"Smoke test rule"`, `"description":"Smoke test rule","options_schema":{"type":42}`, 1),
		"builtin-collision":      strings.ReplaceAll(coreMetadata, `"name":"smoke"`, `"name":"expression-type"`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Validate(t.Context(), coreFixture(t, metadata)); err == nil {
				t.Fatal("accepted a core plugin the application cannot register or invoke")
			}
		})
	}
}

func TestEngineSelectorsMustMatchNormalizedRuntimeShell(t *testing.T) {
	for _, selector := range []string{"", "Bash", "bash.exe", "/bin/bash", "bash -e", "bash\n"} {
		if err := engineRegistration("smoke", []string{selector}); err == nil {
			t.Fatalf("accepted unreachable selector %q", selector)
		}
	}
	if err := engineRegistration("smoke", []string{"bash", "python3", "pwsh"}); err != nil {
		t.Fatal(err)
	}
}

func TestBundledBackendsSupplyTheirOwnShellSelection(t *testing.T) {
	for _, name := range []string{"ruff", "shellcheck"} {
		if err := engineRegistration(name, nil); err != nil {
			t.Fatalf("bundled %s backend must not require generic selectors: %v", name, err)
		}
	}
	metadata := strings.Replace(coreMetadata, `"capabilities":{"script_shells":["bash"]},`, "", 1)
	metadata = strings.ReplaceAll(metadata, `"name":"smoke"`, `"name":"ruff"`)
	response := `{"ok":true,"diagnostics":[{"filename":"registry-smoke.py","code":"F821","message":"Undefined name","location":{"row":1,"column":7},"end_location":{"row":1,"column":14}}]}`
	result, err := Validate(t.Context(), coreFixtureResponse(t, metadata, response))
	if err != nil || result.Kind != "core" {
		t.Fatalf("Ruff-compatible plugin without generic selectors: %+v %v", result, err)
	}
}

type optionsAnalyzer struct {
	schema json.RawMessage
	called bool
}

func (candidate *optionsAnalyzer) Descriptor() analyzer.Descriptor {
	return analyzer.Descriptor{Name: "registry-options-test", Description: "Exercise option registration", OptionsSchema: candidate.schema}
}

func (candidate *optionsAnalyzer) Analyze(context.Context, analyzer.Input) ([]diagnostic.Diagnostic, error) {
	candidate.called = true
	return nil, nil
}

func TestApplicationValidatesComponentOptionsBeforeInvocation(t *testing.T) {
	for _, schema := range []string{`{"type":42}`, `{"$ref":"https://example.invalid/options.json"}`, `{"type":"object","required":["token"]}`} {
		candidate := &optionsAnalyzer{schema: json.RawMessage(schema)}
		if _, err := analyzeComponent(t.Context(), candidate, sampleWorkflow, []string{"registry-options-test"}); err == nil {
			t.Fatalf("accepted schema incompatible with default options: %s", schema)
		}
		if candidate.called {
			t.Fatal("invoked analyzer despite rejected default options")
		}
	}
	candidate := &optionsAnalyzer{schema: json.RawMessage(`{"type":"object","additionalProperties":false}`)}
	if _, err := analyzeComponent(t.Context(), candidate, sampleWorkflow, []string{"registry-options-test"}); err != nil || !candidate.called {
		t.Fatalf("valid application registration: called=%v error=%v", candidate.called, err)
	}
}

func TestDescriptorBounds(t *testing.T) {
	valid := rule.Descriptor{Name: "test-rule", Description: "Useful check"}
	for _, descriptors := range [][]rule.Descriptor{nil, {valid, valid}, {{Name: "bad\nname", Description: "bad"}}, {{Name: "ok"}}, {{Name: "ok", Description: strings.Repeat("x", 4097)}}} {
		if _, err := ruleNames(descriptors); err == nil {
			t.Fatalf("accepted invalid descriptors: %+v", descriptors)
		}
	}
}

func TestDiagnosticBounds(t *testing.T) {
	request := analyzer.ScriptRequest{Source: "é\n", Filename: "test.py"}
	valid := analyzer.ScriptDiagnostic{Filename: request.Filename, Code: "X", Message: "example", Location: analyzer.ScriptPosition{Row: 1, Column: 1}, EndLocation: analyzer.ScriptPosition{Row: 1, Column: 2}}
	if err := scriptFindings([]analyzer.ScriptDiagnostic{valid}, request); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.EndLocation.Column = 3 // Byte offsets are not Unicode scalar offsets.
	if err := scriptFindings([]analyzer.ScriptDiagnostic{invalid}, request); err == nil {
		t.Fatal("accepted out-of-bounds location")
	}
	input, err := workflowInput(t.Context(), sampleWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	d := diagnostic.Diagnostic{Rule: "test", Message: "example", Start: diagnostic.Position{Line: 1, Column: 1}, End: diagnostic.Position{Line: 1, Column: 2}}
	if err := componentFindings([]diagnostic.Diagnostic{d}, input, []string{"test"}); err != nil {
		t.Fatal(err)
	}
	d.Fixes = []diagnostic.Fix{{Edits: []diagnostic.Edit{{Path: "other-file", Start: d.Start, End: d.End}}}}
	if err := componentFindings([]diagnostic.Diagnostic{d}, input, []string{"test"}); err == nil {
		t.Fatal("accepted edit outside fixture")
	}
}

func TestSharedFacts(t *testing.T) {
	input, err := workflowInput(t.Context(), sampleWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	if len(input.Scripts) != 1 || input.Reference.Revision == "" || len(input.Scripts[0].Spans) == 0 || input.Scripts[0].Runtime == nil || !input.Scripts[0].Runtime.ShellKnown {
		t.Fatalf("missing authentic workflow facts: %+v", input)
	}
}
