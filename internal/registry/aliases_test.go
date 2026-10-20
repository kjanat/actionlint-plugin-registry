package registry

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCuratedAliasesExportOnlyPublishedTargets(t *testing.T) {
	entry := acceptedFixture(t)
	name := entry.Submission.Provider + ".com/" + entry.Submission.Repository
	index := Index{SchemaVersion: 1, Entries: []Entry{entry}}
	aliases := Aliases{SchemaVersion: 1, Aliases: map[string]AliasTarget{
		"friendly": {Repository: name, Reserved: true}, "another": {Repository: name},
		"future": {Repository: "github.com/publisher/future", Reserved: true},
	}}
	output := t.TempDir()
	if err := Export(index, output, nil, &aliases); err != nil {
		t.Fatal(err)
	}
	var catalog Catalog
	if err := readRecord(filepath.Join(output, "index.json"), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Plugins) != 1 || catalog.Plugins[0].Name != name ||
		!reflect.DeepEqual(catalog.Plugins[0].Aliases, []string{"another", "friendly"}) {
		t.Fatalf("unexpected alias publication: %+v", catalog)
	}
	// Publisher names and plugin IDs confer no automatic shorthand ownership.
	if err := Export(index, output, nil, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(output, "index.json"))
	if err != nil || strings.Contains(string(data), `"aliases"`) {
		t.Fatalf("publisher metadata assigned aliases: %s, %v", data, err)
	}
}

func TestAliasAssignmentsRejectMalformedAndAccidentalTargets(t *testing.T) {
	index := Index{SchemaVersion: 1, Entries: []Entry{acceptedFixture(t)}}
	for _, text := range []string{
		`{"schema_version":1,"aliases":{"Upper":{"repository":"github.com/publisher/plugin","reserved":true}}}`,
		`{"schema_version":1,"aliases":{"a_b":{"repository":"github.com/publisher/plugin","reserved":true}}}`,
		`{"schema_version":1,"aliases":{"` + strings.Repeat("a", 65) + `":{"repository":"github.com/publisher/plugin","reserved":true}}}`,
		`{"schema_version":1,"aliases":{"future":{"repository":"github.com/publisher/unpublished"}}}`,
		`{"schema_version":1,"aliases":{"future":{"repository":"https://github.com/publisher/plugin","reserved":true}}}`,
		`{"schema_version":1,"aliases":{"future":{"repository":"github.com/publisher/../plugin","reserved":true}}}`,
		`{"schema_version":1,"aliases":{"future":{"repository":"github.com/Publisher/plugin","reserved":true}}}`,
		`{"schema_version":1,"aliases":{"future":{"repository":"github.com/publisher/plugin","reserved":true},"future":{"repository":"github.com/other/plugin","reserved":true}}}`,
		`{"schema_version":1,"aliases":null}`,
		`{"schema_version":2,"aliases":{}}`,
		`{"schema_version":1,"aliases":{"future":{"repository":"github.com/publisher/plugin","publisher_claim":true}}}`,
	} {
		t.Run(text, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "aliases.json")
			if err := os.WriteFile(path, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadAliases(path, index); err == nil {
				t.Fatal("invalid alias assignment accepted")
			}
		})
	}
}

func TestRepositoryAliasAssignments(t *testing.T) {
	index, err := (Store{Directory: "../../registry/plugins"}).Load()
	if err != nil {
		t.Fatal(err)
	}
	aliases, err := ReadAliases("../../registry/aliases.json", index)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"shellcheck", "ruff"} {
		target := aliases.Aliases[name]
		if target.Repository != "github.com/kjanat/actionlint-plugin-"+name {
			t.Fatalf("unexpected reserved identity: %s -> %+v", name, target)
		}
	}
}
