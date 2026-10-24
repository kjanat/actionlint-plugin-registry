package registry

import (
	"errors"
	"fmt"
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
	return writeCataloguePage(index, directory, statuses, downloads, aliases)
}

func writeCatalog(directory string, catalog Catalog) error {
	slices.SortFunc(catalog.Plugins, func(a, b CatalogPlugin) int { return strings.Compare(a.Name, b.Name) })
	return WriteJSON(filepath.Join(directory, "index.json"), catalog)
}
