package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"registry.actionlint.kjanat.dev/internal/registry"
)

// The exporter is also used without a website builder by JSON consumers.
func TestExportWritesJSONWithoutWebsiteAssets(t *testing.T) {
	root := t.TempDir()
	aliases := filepath.Join(root, "aliases.json")
	if err := registry.WriteJSON(aliases, registry.Aliases{SchemaVersion: 1, Aliases: map[string]registry.AliasTarget{}}); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "public")
	if err := run(t.Context(), []string{
		"export", "--store", filepath.Join(root, "plugins"), "--aliases", aliases,
		"--statuses", filepath.Join(root, "status"), "--compatibility", filepath.Join(root, "compatibility"),
		"--output", output,
	}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"index.json", "statuses.json"} {
		if _, err := os.ReadFile(filepath.Join(output, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := filepath.WalkDir(output, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && filepath.Ext(path) != ".json" {
			t.Errorf("export wrote website asset %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
