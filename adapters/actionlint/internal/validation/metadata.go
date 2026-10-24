package validation

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"actionlint.kjanat.dev/v2/analyzer"
	"actionlint.kjanat.dev/v2/plugin"
	"actionlint.kjanat.dev/v2/rule"
)

// The selected host validates exported WIT types before starting the guest.
var ErrUnsupportedContract = errors.New("contract is not supported by this checker")

const componentInterface = "kjanat:actionlint/analyzer@0.1.0"
const scriptInterface = "kjanat:actionlint/script-engine@0.1.0"
const runnerInterface = "kjanat:actionlint/runner-provider@0.1.0"

func supportedComponent(iface string) bool {
	switch iface {
	case componentInterface, scriptInterface, runnerInterface:
		return true
	default:
		return false
	}
}

// componentDeclaration reuses the host's public descriptor and adapts its JSON field names.
// The rule fields retain the types defined by the host.
type componentDeclaration struct {
	rule.Descriptor
	Requires []analyzer.Fact `json:"requires"`
}

type embeddedMetadata struct {
	MetadataVersion int    `json:"metadata_version"`
	ABIVersion      int    `json:"abi_version"`
	ID              string `json:"id"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	License         string `json:"license"`
	Repository      string `json:"repository"`
	PluginVersion   string `json:"plugin_version"`
	Transport       string `json:"transport"`
	Interface       string `json:"interface"`
	ContractSHA256  string `json:"contract_sha256"`
	Interfaces      map[string][]struct {
		Version int    `json:"version"`
		Export  string `json:"export"`
	} `json:"interfaces"`
	Capabilities struct {
		ConfigSchema bool `json:"config_schema"`
	} `json:"capabilities"`
	ConfigSchema json.RawMessage       `json:"config_schema"`
	Descriptor   *componentDeclaration `json:"descriptor"`
}

func inspectMetadata(ctx context.Context, info plugin.ArtifactInfo) (embeddedMetadata, error) {
	var metadata embeddedMetadata
	if len(info.Metadata) == 0 {
		return metadata, errors.New("registry admission requires a top-level actionlint.plugin custom section")
	}
	// Read only contract identity before touching host-specific descriptors.
	// An unfamiliar ABI may legitimately change their JSON representation.
	switch info.Kind {
	case plugin.CoreArtifact:
		var contract struct {
			ABI       uint32 `json:"abi_version"`
			Transport string `json:"transport"`
		}
		if err := jsonv2.Unmarshal(info.Metadata, &contract); err != nil {
			return metadata, fmt.Errorf("core contract declaration: %w", err)
		}
		if contract.ABI != 1 || contract.Transport != "wasm-core-json" {
			return metadata, ErrUnsupportedContract
		}
	case plugin.ComponentArtifact:
		var contract struct {
			Transport string `json:"transport"`
			Interface string `json:"interface"`
			Digest    string `json:"contract_sha256"`
		}
		if err := jsonv2.Unmarshal(info.Metadata, &contract); err != nil {
			return metadata, fmt.Errorf("component contract declaration: %w", err)
		}
		if contract.Transport != "wasm-component" || !supportedComponent(contract.Interface) {
			return metadata, ErrUnsupportedContract
		}
		// Source hashes change with WIT comments. The host checks actual types;
		// the declaration's digest identifies the publisher's source contract.
		if _, err := hex.DecodeString(contract.Digest); err != nil || len(contract.Digest) != 64 || strings.ToLower(contract.Digest) != contract.Digest {
			return metadata, errors.New("component contract digest must be a lowercase SHA-256")
		}
	default:
		return metadata, ErrUnsupportedContract
	}
	// Unknown discovery fields remain in Result.Metadata. The host performs the
	// same complete JSON comparison for core metadata, including unknown fields.
	if err := jsonv2.Unmarshal(info.Metadata, &metadata); err != nil {
		return metadata, fmt.Errorf("embedded metadata: %w", err)
	}
	if metadata.MetadataVersion != 1 {
		return metadata, errors.New("embedded metadata_version must be 1")
	}
	if !identifier(metadata.ID) || metadata.Name == "" || !boundedText(metadata.Name, 256) || metadata.Description == "" || !boundedText(metadata.Description, 4096) || metadata.License == "" || !boundedText(metadata.License, 256) || metadata.PluginVersion == "" || !boundedText(metadata.PluginVersion, 128) {
		return metadata, errors.New("embedded metadata requires valid id, name, description, license and plugin_version")
	}
	u, err := url.Parse(metadata.Repository)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path == "" {
		return metadata, errors.New("embedded metadata repository must be an absolute HTTPS repository URL")
	}
	switch info.Kind {
	case plugin.CoreArtifact:
		if metadata.ABIVersion != 1 || metadata.Transport != "wasm-core-json" {
			return metadata, fmt.Errorf("%w: requires ABI 1 and wasm-core-json transport", ErrUnsupportedContract)
		}
		for _, operation := range []string{"check", "validate_config", "metadata"} {
			compatible := false
			for _, implementation := range metadata.Interfaces[operation] {
				if implementation.Version == 1 && implementation.Export != "" {
					compatible = true
				}
			}
			if !compatible {
				return metadata, fmt.Errorf("embedded metadata requires an explicit version 1 %s interface", operation)
			}
		}
		if metadata.Capabilities.ConfigSchema && len(metadata.ConfigSchema) == 0 {
			return metadata, errors.New("embedded metadata advertises a missing config_schema")
		}
		if len(metadata.ConfigSchema) != 0 {
			if _, err := workflowInputWithSchema(ctx, emptyWorkflow, metadata.ConfigSchema); err != nil {
				return metadata, fmt.Errorf("embedded configuration schema: %w", err)
			}
		}
	case plugin.ComponentArtifact:
		if metadata.Interface != componentInterface {
			break
		}
		if metadata.Descriptor == nil || metadata.Descriptor.Name != metadata.ID {
			return metadata, errors.New("embedded component metadata requires a descriptor matching its id")
		}
		// Reject unsupported descriptor members instead of dropping contract data.
		var fields map[string]json.RawMessage
		if err := jsonv2.Unmarshal(info.Metadata, &fields); err != nil {
			return metadata, err
		}
		if err := jsonv2.Unmarshal(fields["descriptor"], metadata.Descriptor, jsonv2.RejectUnknownMembers(true)); err != nil {
			return metadata, fmt.Errorf("embedded component descriptor: %w", err)
		}
	default:
		return metadata, errors.New("unsupported artifact kind")
	}
	return metadata, nil
}

func (metadata embeddedMetadata) contract() string {
	if metadata.Transport == "wasm-core-json" {
		return "wasm-core-json/1"
	}
	return metadata.Interface + "#sha256=" + metadata.ContractSHA256
}

func (declared *componentDeclaration) matches(actual analyzer.Descriptor) error {
	if declared == nil {
		return errors.New("missing embedded component descriptor")
	}
	// Normalize optional fields through the same public Go descriptor, then
	// canonicalize JSON so option schema key order is immaterial.
	normalized := *declared
	if len(normalized.Requires) == 0 {
		normalized.Requires = nil
	}
	if len(actual.Requires) == 0 {
		actual.Requires = nil
	}
	expected, err := json.Marshal(normalized)
	if err != nil {
		return err
	}
	observed, err := json.Marshal(componentDeclaration{Descriptor: actual.Descriptor, Requires: actual.Requires})
	if err != nil {
		return err
	}
	left, right := jsontext.Value(expected), jsontext.Value(observed)
	if err := left.Canonicalize(); err != nil {
		return err
	}
	if err := right.Canonicalize(); err != nil {
		return err
	}
	if !bytes.Equal(left, right) {
		return errors.New("embedded component descriptor differs from runtime describe()")
	}
	return nil
}
