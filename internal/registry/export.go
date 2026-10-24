package registry

import (
	"errors"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"registry.actionlint.kjanat.dev/internal/compatibility"
)

func Find(index Index, repository, tag string) (Entry, error) {
	for _, e := range index.Entries {
		if (strings.EqualFold(e.Submission.Provider+"/"+e.Submission.Repository, repository) || strings.EqualFold(e.Submission.Provider+".com/"+e.Submission.Repository, repository)) && e.Submission.Tag == tag {
			return e, nil
		}
	}
	return Entry{}, fmt.Errorf("no accepted version %s@%s", repository, tag)
}

func Resolve(index Index, repository, tag string) (string, error) {
	entry, err := Find(index, repository, tag)
	if err != nil {
		return "", err
	}
	return entry.PinnedURL(), nil
}

type Catalog struct {
	SchemaVersion int             `json:"schema_version"`
	Source        string          `json:"source,omitempty"`
	Statuses      string          `json:"statuses,omitempty"`
	Plugins       []CatalogPlugin `json:"plugins"`
}
type CatalogPlugin struct {
	Name     string   `json:"name"`
	PluginID string   `json:"plugin_id"`
	Manifest string   `json:"manifest"`
	Aliases  []string `json:"aliases,omitempty"`
}
type CompatibilityLink struct {
	Host    compatibility.Host `json:"host"`
	Policy  string             `json:"policy"`
	Outcome string             `json:"outcome"`
	Report  string             `json:"report"`
}

type VersionLink struct {
	Compatibility []CompatibilityLink `json:"compatibility"`
	Version       string              `json:"version"`
	Record        string              `json:"record"`
}
type PluginManifest struct {
	Plugin   Plugin        `json:"plugin"`
	Versions []VersionLink `json:"versions"`
}

func Export(index Index, directory string, statuses []ReleaseStatus, aliases *Aliases, evidence ...compatibility.Evidence) error {
	return export(index, directory, statuses, nil, aliases, evidence...)
}

func export(index Index, directory string, statuses []ReleaseStatus, downloads *DownloadSnapshot, aliases *Aliases, evidence ...compatibility.Evidence) error {
	if aliases != nil {
		if err := aliases.Validate(index); err != nil {
			return err
		}
	}
	for _, status := range statuses {
		if _, err := status.validate(index); err != nil {
			return err
		}
	}
	observations := map[string][]CompatibilityLink{}
	for _, item := range evidence {
		if err := item.Validate(); err != nil {
			return err
		}
		accepted := false
		for _, entry := range index.Entries {
			if entry.Submission.SHA256 == item.Report.ArtifactSHA256 {
				accepted = true
			}
		}
		if !accepted {
			return errors.New("cannot export evidence for an unknown artifact")
		}
		name, err := evidenceName(item)
		if err != nil {
			return err
		}
		path := "compatibility/" + item.Report.ArtifactSHA256 + "/" + name
		if err = WriteJSON(filepath.Join(directory, filepath.FromSlash(path)), item); err != nil {
			return err
		}
		observations[item.Report.ArtifactSHA256] = append(observations[item.Report.ArtifactSHA256], CompatibilityLink{Host: item.Report.Host, Policy: item.Report.Policy, Outcome: item.Report.Outcome, Report: "../../" + path})
	}
	catalog := Catalog{SchemaVersion: 1, Statuses: "statuses.json", Plugins: []CatalogPlugin{}}
	manifests := map[string]PluginManifest{}
	for _, entry := range index.Entries {
		id := entry.Receipt.PluginKey()
		manifest, ok := manifests[id]
		if !ok {
			manifest = PluginManifest{Plugin: pluginFor(entry), Versions: []VersionLink{}}
			plugin := CatalogPlugin{Name: entry.Submission.Provider + ".com/" + entry.Submission.Repository, PluginID: entry.PluginID(), Manifest: "plugins/" + id + "/index.json"}
			if aliases != nil {
				plugin.Aliases = aliases.For(plugin.Name)
			}
			catalog.Plugins = append(catalog.Plugins, plugin)
		}
		filename := key(entry.Submission.Tag) + ".json"
		manifest.Versions = append(manifest.Versions, VersionLink{Version: entry.Submission.Tag, Record: "versions/" + filename, Compatibility: append([]CompatibilityLink{}, observations[entry.Submission.SHA256]...)})
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
	if err := writeCatalog(directory, catalog); err != nil {
		return err
	}
	if err := WriteJSON(filepath.Join(directory, "statuses.json"), StatusSnapshot{SchemaVersion: 1, Releases: append([]ReleaseStatus{}, statuses...)}); err != nil {
		return err
	}
	if downloads != nil {
		if err := WriteJSON(filepath.Join(directory, "downloads.json"), downloads); err != nil {
			return err
		}
	}
	count := func(entry Entry) string {
		if downloads == nil {
			return "not collected"
		}
		return downloads.display(entry)
	}
	state := func(entry Entry) string {
		if status := StatusFor(entry, statuses); status != nil {
			return string(status.Status) + ": " + status.Reason
		}
		return string(StatusActive)
	}
	page := template.Must(template.New("index").Funcs(template.FuncMap{"downloads": count, "status": state}).Parse(`<!doctype html>
<html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>actionlint plugin registry</title>
<style>body{font:1rem system-ui;max-width:70rem;margin:3rem auto;padding:1rem}td,th{text-align:left;padding:.5rem}code{overflow-wrap:anywhere}</style>
<h1>actionlint plugin registry</h1><p>This catalogue lists authenticated plugin releases with verified checksums and discovery metadata. Each entry identifies its publisher and records a download URL and checksum. Acceptance does not guarantee that a plugin is safe or correct.</p>
<p><a href="index.json">Download the JSON index</a> · <a href="https://github.com/kjanat/actionlint-registry#publishing">Learn how to publish a plugin</a></p>
<p>Downloads are GitHub's cumulative counters for the accepted plugin assets. They include CI, validation and repeat downloads; they do not measure unique users or installations. Cached checks do not download again.</p>
{{with .Downloads}}<p>Observed {{.ObservedAt.Format "2006-01-02 15:04:05 UTC"}} · <a href="downloads.json">Download counts JSON</a>. Scheduled refresh every six hours; unavailable or unsupported counts are not zero.</p>{{end}}
<table><thead><tr><th>Plugin</th><th>Version</th><th>Format</th><th>Status</th><th>Downloads</th><th>Download URL and checksum</th></tr></thead><tbody>
{{range .Entries}}<tr><td>{{.Submission.Repository}}</td><td>{{.Submission.Tag}}</td><td>{{.Submission.Kind}}</td><td>{{status .}}</td><td>{{downloads .}}</td><td><code>{{.PinnedURL}}</code></td></tr>{{else}}<tr><td colspan="6">The registry has not accepted any releases yet. Publishers must authenticate through their CI provider and submit a release that passes admission checks.</td></tr>{{end}}
</tbody></table></html>`))
	f, err := os.Create(filepath.Join(directory, "index.html"))
	if err != nil {
		return err
	}
	if err = page.Execute(f, struct {
		Entries   []Entry
		Downloads *DownloadSnapshot
	}{index.Entries, downloads}); err != nil {
		return errors.Join(err, f.Close())
	}
	return f.Close()
}

func writeCatalog(directory string, catalog Catalog) error {
	slices.SortFunc(catalog.Plugins, func(a, b CatalogPlugin) int { return strings.Compare(a.Name, b.Name) })
	return WriteJSON(filepath.Join(directory, "index.json"), catalog)
}
