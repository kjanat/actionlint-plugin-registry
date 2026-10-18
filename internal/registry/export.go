package registry

import (
	"errors"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func Resolve(index Index, repository, tag string) (string, error) {
	for _, e := range index.Entries {
		if (strings.EqualFold(e.Submission.Provider+"/"+e.Submission.Repository, repository) || strings.EqualFold(e.Submission.Provider+".com/"+e.Submission.Repository, repository)) && e.Submission.Tag == tag {
			return e.PinnedURL(), nil
		}
	}
	return "", fmt.Errorf("no accepted version %s@%s", repository, tag)
}

type Catalog struct {
	SchemaVersion int             `json:"schema_version"`
	Plugins       []CatalogPlugin `json:"plugins"`
}
type CatalogPlugin struct {
	Name     string `json:"name"`
	PluginID string `json:"plugin_id"`
	Manifest string `json:"manifest"`
}
type VersionLink struct {
	Version string `json:"version"`
	Record  string `json:"record"`
}
type PluginManifest struct {
	Plugin   Plugin        `json:"plugin"`
	Versions []VersionLink `json:"versions"`
}

func Export(index Index, directory string) error {
	catalog := Catalog{SchemaVersion: 1, Plugins: []CatalogPlugin{}}
	manifests := map[string]PluginManifest{}
	for _, entry := range index.Entries {
		id := entry.Receipt.PluginKey()
		manifest, ok := manifests[id]
		if !ok {
			manifest = PluginManifest{Plugin: pluginFor(entry), Versions: []VersionLink{}}
			catalog.Plugins = append(catalog.Plugins, CatalogPlugin{Name: entry.Submission.Provider + ".com/" + entry.Submission.Repository, PluginID: entry.Validation.PluginID, Manifest: "plugins/" + id + "/index.json"})
		}
		filename := key(entry.Submission.Tag) + ".json"
		manifest.Versions = append(manifest.Versions, VersionLink{entry.Submission.Tag, "versions/" + filename})
		manifests[id] = manifest
		if err := WriteJSON(filepath.Join(directory, "plugins", id, "versions", filename), entry); err != nil {
			return err
		}
	}
	slices.SortFunc(catalog.Plugins, func(a, b CatalogPlugin) int { return strings.Compare(a.Name, b.Name) })
	for id, manifest := range manifests {
		if err := WriteJSON(filepath.Join(directory, "plugins", id, "index.json"), manifest); err != nil {
			return err
		}
	}
	if err := WriteJSON(filepath.Join(directory, "index.json"), catalog); err != nil {
		return err
	}
	page := template.Must(template.New("index").Parse(`<!doctype html>
<html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>actionlint plugin registry</title>
<style>body{font:1rem system-ui;max-width:70rem;margin:3rem auto;padding:1rem}td,th{text-align:left;padding:.5rem}code{overflow-wrap:anywhere}</style>
<h1>actionlint plugin registry</h1><p>This catalogue lists plugin releases that passed actionlint's compatibility checks. Each entry identifies its publisher and records a download URL and checksum. Acceptance does not guarantee that a plugin is safe or correct.</p>
<p><a href="index.json">Download the JSON index</a> · <a href="https://github.com/kjanat/actionlint-plugin-registry#publishing">Learn how to publish a plugin</a></p>
<table><thead><tr><th>Plugin</th><th>Version</th><th>Format</th><th>Download URL and checksum</th></tr></thead><tbody>
{{range .Entries}}<tr><td>{{.Submission.Repository}}</td><td>{{.Submission.Tag}}</td><td>{{.Submission.Kind}}</td><td><code>{{.PinnedURL}}</code></td></tr>{{else}}<tr><td colspan="4">The registry has not accepted any releases yet. Publishers must authenticate through their CI provider and submit a plugin that passes validation.</td></tr>{{end}}
</tbody></table></html>`))
	f, err := os.Create(filepath.Join(directory, "index.html"))
	if err != nil {
		return err
	}
	if err = page.Execute(f, index); err != nil {
		return errors.Join(err, f.Close())
	}
	return f.Close()
}
