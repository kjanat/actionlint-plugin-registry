package validation

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"actionlint.kjanat.dev/v2/analyzer"
	"actionlint.kjanat.dev/v2/diagnostic"
	"actionlint.kjanat.dev/v2/plugin"
	"actionlint.kjanat.dev/v2/rule"
	"actionlint.kjanat.dev/v2/runner"
)

func validateScriptComponent(ctx context.Context, component *plugin.Component) (Result, error) {
	if !component.HasScriptEngine() {
		return Result{}, errors.New("component does not implement its declared script-engine role")
	}
	descriptor := component.ScriptDescriptor()
	names, err := ruleNames([]rule.Descriptor{descriptor.Rule})
	if err != nil {
		return Result{}, err
	}
	registration := descriptor.Rule
	if registration.Name == "ruff" || registration.Name == "shellcheck" {
		registration.Name = "registry-fixture"
	}
	if _, err := workflowInputWithDescriptor(ctx, sampleWorkflow, registration); err != nil {
		return Result{}, fmt.Errorf("engine registration: %w", err)
	}
	if err := component.ValidateConfig(ctx, nil); err != nil {
		return Result{}, fmt.Errorf("default configuration: %w", err)
	}
	request := analyzer.ScriptRequest{Source: "\n", Filename: "registry-smoke.txt"}
	if slices.Contains(descriptor.Shells, "python") || descriptor.Engine == "ruff" {
		request.Source, request.Filename = "print(missing)\n", "registry-smoke.py"
	}
	for attempt := range 2 {
		findings, err := component.Check(ctx, request)
		if err != nil {
			return Result{}, fmt.Errorf("sample invocation %d: %w", attempt+1, err)
		}
		if err := scriptFindings(findings, request); err != nil {
			return Result{}, err
		}
		if descriptor.Engine == "ruff" && !slices.ContainsFunc(findings, func(d analyzer.ScriptDiagnostic) bool { return d.Code == "F821" }) {
			return Result{}, errors.New("ruff smoke sample did not report F821")
		}
	}
	return Result{Kind: "component", PluginID: descriptor.ID, Rules: names, HostCommit: HostCommit,
		Checks: []string{"host-wit-contract", "script-engine", "rule-descriptors", "application-registration", "default-config", "repeated-sample", "diagnostic-ranges"}}, nil
}

func validateRunnerComponent(ctx context.Context, component *plugin.Component, metadata embeddedMetadata) (Result, error) {
	if !component.HasRunnerProvider() {
		return Result{}, errors.New("component does not implement its declared runner-provider role")
	}
	descriptor := component.RunnerDescriptor()
	if descriptor.Name != metadata.ID || descriptor.Version != metadata.PluginVersion {
		return Result{}, errors.New("embedded runner identity differs from runtime descriptor")
	}
	// A generic checker has no vendor dataset. Missing evidence must still produce
	// a valid answer for each selector, without fetching resources during analysis.
	for _, target := range descriptor.Targets {
		request := runner.Request{Target: target, Scope: "registry-smoke", Snapshot: runner.Snapshot{Scope: "registry-smoke", Freshness: runner.Missing},
			Selectors: []runner.Selector{{Labels: []runner.Constraint{{Value: runner.Literal("registry-smoke"), Position: diagnostic.Position{Line: 1, Column: 1}}}}}}
		for attempt := range 2 {
			answers, err := component.ResolveRunners(ctx, request)
			if err != nil {
				return Result{}, fmt.Errorf("runner invocation %d: %w", attempt+1, err)
			}
			if len(answers) != len(request.Selectors) {
				return Result{}, errors.New("runner returned the wrong answer count")
			}
		}
	}
	return Result{Kind: "component", PluginID: descriptor.Name, Rules: []string{}, HostCommit: HostCommit,
		Checks: []string{"host-wit-contract", "runner-provider", "runner-descriptor", "missing-snapshot", "repeated-sample"}}, nil
}
