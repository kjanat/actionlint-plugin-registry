package validation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"actionlint.kjanat.dev/v2/analysis"
	"actionlint.kjanat.dev/v2/analyzer"
	"actionlint.kjanat.dev/v2/diagnostic"
	"actionlint.kjanat.dev/v2/plugin"
	"actionlint.kjanat.dev/v2/rule"
)

const sampleWorkflow = "name: Registry smoke\non: push\njobs:\n  smoke:\n    runs-on: ubuntu-latest\n    steps:\n      - id: sample\n        shell: bash\n        run: |\n          printf '%s\\n' $value\n"
const emptyWorkflow = "name: Registry empty\non: push\njobs: {}\n"
const samplePath = "registry-smoke.yml"

func validateComponent(ctx context.Context, path string, metadata embeddedMetadata) (result Result, err error) {
	component, err := plugin.Load(ctx, path, plugin.Options{Timeout: callTimeout, MemoryPages: 1024})
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, component.Close(context.WithoutCancel(ctx))) }()
	switch metadata.Interface {
	case scriptInterface:
		return validateScriptComponent(ctx, component)
	case runnerInterface:
		return validateRunnerComponent(ctx, component, metadata)
	}
	if !component.HasAnalyzer() {
		return result, errors.New("component does not implement its declared analyzer role")
	}
	descriptor := component.Descriptor()
	if err := metadata.Descriptor.matches(descriptor); err != nil {
		return result, err
	}
	names, err := ruleNames([]rule.Descriptor{descriptor.Descriptor})
	if err != nil {
		return result, err
	}
	for attempt := range 2 {
		findings, err := analyzeComponent(ctx, component, sampleWorkflow, names)
		if err != nil {
			return result, fmt.Errorf("sample invocation %d: %w", attempt+1, err)
		}
		if descriptor.Name == "shellcheck-rs" && !slices.ContainsFunc(findings, func(d diagnostic.Diagnostic) bool { return d.Code == "SC2086" }) {
			return result, errors.New("shellcheck smoke sample did not report SC2086")
		}
	}
	_, err = analyzeComponent(ctx, component, emptyWorkflow, names)
	if err != nil {
		return result, fmt.Errorf("empty workflow: %w", err)
	}
	return Result{Kind: "component", PluginID: descriptor.Name, Rules: names,
		Checks: []string{"host-wit-contract", "rule-descriptors", "application-registration", "default-config", "repeated-sample", "diagnostic-ranges", "empty-workflow"}, HostCommit: HostCommit}, nil
}

type checkedAnalyzer struct {
	analyzer.Analyzer
	rules    []string
	findings []diagnostic.Diagnostic
	called   bool
}

func (checked *checkedAnalyzer) Analyze(ctx context.Context, input analyzer.Input) ([]diagnostic.Diagnostic, error) {
	checked.called = true
	findings, err := checked.Analyzer.Analyze(ctx, input)
	if err != nil {
		return nil, err
	}
	if err := componentFindings(findings, input, checked.rules); err != nil {
		return nil, err
	}
	checked.findings = findings
	return findings, nil
}

// Invoke through the public application so schema compilation, default options,
// descriptor collisions and required facts follow the same path as real use.
func analyzeComponent(ctx context.Context, component analyzer.Analyzer, content string, names []string) ([]diagnostic.Diagnostic, error) {
	checked := &checkedAnalyzer{Analyzer: component, rules: names}
	_, err := analysis.Analyze(ctx, analysis.Request{
		Sources: []analysis.Source{{Path: samplePath, Content: []byte(content)}},
		Options: analysis.Options{Analyzers: []analyzer.Registration{{Analyzer: checked}}},
	})
	if err != nil {
		return nil, err
	}
	if !checked.called {
		return nil, errors.New("application did not invoke component")
	}
	return checked.findings, nil
}

type inputCollector struct {
	input      *analyzer.Input
	descriptor rule.Descriptor
}

func (collector *inputCollector) Descriptor() analyzer.Descriptor {
	return analyzer.Descriptor{Descriptor: collector.descriptor, Requires: []analyzer.Fact{
		analyzer.Source, analyzer.Scripts, analyzer.Mappings, analyzer.Invocations, analyzer.Execution, analyzer.Dependencies,
	}}
}

func (collector *inputCollector) Analyze(_ context.Context, input analyzer.Input) ([]diagnostic.Diagnostic, error) {
	collector.input = &input
	return nil, nil
}

func workflowInput(ctx context.Context, content string) (analyzer.Input, error) {
	return workflowInputWithSchema(ctx, content, nil)
}

func workflowInputWithSchema(ctx context.Context, content string, schema json.RawMessage) (analyzer.Input, error) {
	return workflowInputWithDescriptor(ctx, content, rule.Descriptor{Name: "registry-fixture", Description: "Collect shared workflow facts", OptionsSchema: schema})
}

func workflowInputWithDescriptor(ctx context.Context, content string, descriptor rule.Descriptor) (analyzer.Input, error) {
	collector := &inputCollector{descriptor: descriptor}
	_, err := analysis.Analyze(ctx, analysis.Request{
		Sources: []analysis.Source{{Path: samplePath, Content: []byte(content)}},
		Options: analysis.Options{Analyzers: []analyzer.Registration{{Analyzer: collector}}},
	})
	if err != nil {
		return analyzer.Input{}, err
	}
	if collector.input == nil {
		return analyzer.Input{}, errors.New("host did not produce workflow facts")
	}
	return *collector.input, nil
}
