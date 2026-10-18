// Package validation exercises plugins using the configured actionlint host.
// Run it in a disposable, credential-free process with an outer deadline.
package validation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"actionlint.kjanat.dev/analyzer"
	"actionlint.kjanat.dev/plugin"
	"actionlint.kjanat.dev/rule"
)

const HostCommit = "abe9f897d9d8d019c572142626b8f055352f2ac7"

const callTimeout = 10 * time.Second

type Result struct {
	Kind          string          `json:"kind"`
	PluginID      string          `json:"plugin_id"`
	PluginVersion string          `json:"plugin_version,omitempty"`
	Rules         []string        `json:"rules"`
	Checks        []string        `json:"checks"`
	HostCommit    string          `json:"host_commit"`
	Metadata      json.RawMessage `json:"metadata"`
	Repository    string          `json:"repository"`
	Contract      string          `json:"contract"`
	PolicyVersion string          `json:"policy_version"`
}

// Validate accepts only a snapshot stored in a local regular file. The caller must
// verify its checksum before calling Validate and keep the file unchanged until it returns.
func Validate(ctx context.Context, path string) (Result, error) {
	if strings.Contains(path, "://") {
		return Result{}, errors.New("validation requires a local snapshot")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return Result{}, err
	}
	stat, err := os.Lstat(path)
	if err != nil {
		return Result{}, err
	}
	if !stat.Mode().IsRegular() {
		return Result{}, errors.New("snapshot must be a regular file")
	}
	info, err := plugin.Inspect(ctx, path, plugin.ArtifactOptions{MaxBytes: 128 << 20})
	if err != nil {
		return Result{}, err
	}
	manifest, err := inspectMetadata(ctx, info)
	if err != nil {
		return Result{}, err
	}
	var result Result
	switch info.Kind {
	case plugin.CoreArtifact:
		result, err = validateCore(ctx, path)
	case plugin.ComponentArtifact:
		result, err = validateComponent(ctx, path, manifest.Descriptor)
	default:
		return Result{}, errors.New("unsupported artifact kind")
	}
	if err != nil {
		return Result{}, err
	}
	if result.PluginID != manifest.ID {
		return Result{}, errors.New("embedded metadata identity differs from runtime descriptor")
	}
	result.Metadata = slices.Clone(info.Metadata)
	result.Repository = manifest.Repository
	result.PluginVersion = manifest.PluginVersion
	result.Contract = manifest.contract()
	result.PolicyVersion = "1"
	result.Checks = append(result.Checks, "embedded-metadata", "static-runtime-consistency")
	return result, nil
}

func validateCore(ctx context.Context, path string) (result Result, err error) {
	engine, err := plugin.LoadEngine(ctx, path, plugin.EngineOptions{Timeout: callTimeout, MemoryPages: 4096})
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, engine.Close(context.WithoutCancel(ctx))) }()
	metadata := engine.Metadata()
	if metadata.MetadataVersion != 1 {
		return result, fmt.Errorf("unsupported metadata_version %d", metadata.MetadataVersion)
	}
	if !identifier(metadata.ID) || !boundedText(metadata.PluginVersion, 128) {
		return result, errors.New("invalid plugin identity or version")
	}
	names, err := ruleNames(metadata.Rules)
	if err != nil {
		return result, err
	}
	if err := engineRegistration(metadata.Engine.Name, metadata.Capabilities.ScriptShells); err != nil {
		return result, err
	}
	// Match localapp.engineDescriptor: the rule matching Engine.Name supplies
	// options; otherwise the CLI constructs a descriptor without an options schema.
	descriptor := rule.Descriptor{Name: metadata.Engine.Name, Description: metadata.Name, Provider: metadata.ID}
	for _, candidate := range metadata.Rules {
		if candidate.Name == descriptor.Name {
			descriptor = candidate
			break
		}
	}
	// These two names replace existing external tool providers in the CLI.
	// Other engines must not collide with any built-in analyzer registration.
	if descriptor.Name == "ruff" || descriptor.Name == "shellcheck" {
		descriptor.Name = "registry-fixture"
	}
	if _, err := workflowInputWithDescriptor(ctx, sampleWorkflow, descriptor); err != nil {
		return result, fmt.Errorf("engine registration or options: %w", err)
	}
	if err := engine.ValidateConfig(ctx, nil); err != nil {
		return result, fmt.Errorf("default configuration: %w", err)
	}
	request := analyzer.ScriptRequest{Source: "\n", Filename: "registry-smoke.txt"}
	if slices.Contains(metadata.Capabilities.ScriptShells, "python") || metadata.Engine.Name == "ruff" {
		request.Source, request.Filename = "print(missing)\n", "registry-smoke.py"
	}
	for attempt := range 2 {
		findings, err := engine.Check(ctx, request)
		if err != nil {
			return result, fmt.Errorf("sample invocation %d: %w", attempt+1, err)
		}
		if err := scriptFindings(findings, request); err != nil {
			return result, err
		}
		if metadata.Engine.Name == "ruff" && !slices.ContainsFunc(findings, func(d analyzer.ScriptDiagnostic) bool { return d.Code == "F821" }) {
			return result, errors.New("ruff smoke sample did not report F821")
		}
	}
	return Result{Kind: "core", PluginID: metadata.ID, PluginVersion: metadata.PluginVersion, Rules: names,
		Checks: []string{"host-abi", "metadata-v1", "rule-descriptors", "cli-registration", "default-config", "repeated-sample", "diagnostic-ranges"}, HostCommit: HostCommit}, nil
}

// The public host exposes LoadEngine, but not localapp.configure or the CLI's
// generic backend.Register. Bundled Ruff and ShellCheck registrations replace
// their backend before generic registration and supply their own shell selection.
// Keep these constraints aligned with the configured host paths.
// Selectors must also be canonical: the host lowercases the executable basename
// and strips .exe before matching, so mixed-case and suffixed selectors never run.
func engineRegistration(name string, shells []string) error {
	if !identifier(name) {
		return errors.New("engine requires a usable name")
	}
	if name == "ruff" || name == "shellcheck" {
		return nil
	}
	if len(shells) == 0 || len(shells) > 64 {
		return errors.New("engine requires a usable name and 1 to 64 shell selectors")
	}
	for _, shell := range shells {
		if !identifier(shell) || strings.ContainsAny(shell, " /\\\t\r\n") || shell != strings.ToLower(shell) || strings.HasSuffix(shell, ".exe") {
			return errors.New("engine has an invalid or noncanonical shell selector")
		}
	}
	return nil
}

func ruleNames(descriptors []rule.Descriptor) ([]string, error) {
	if len(descriptors) == 0 || len(descriptors) > 4096 {
		return nil, errors.New("plugin must declare 1 to 4096 rules")
	}
	names := make([]string, 0, len(descriptors))
	seen := make(map[string]bool, len(descriptors))
	for _, d := range descriptors {
		if !identifier(d.Name) || seen[d.Name] || !boundedText(d.Description, 4096) || strings.TrimSpace(d.Description) == "" {
			return nil, errors.New("invalid, duplicate or undescribed rule")
		}
		seen[d.Name] = true
		names = append(names, d.Name)
	}
	slices.Sort(names)
	return names, nil
}

func identifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, c := range value {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', strings.ContainsRune("-._/", c):
			continue
		default:
			return false
		}
	}
	return true
}

func boundedText(value string, limit int) bool {
	return len(value) <= limit && utf8.ValidString(value) && !strings.ContainsFunc(value, func(r rune) bool {
		return unicode.IsControl(r) && r != '\n' && r != '\t'
	})
}
