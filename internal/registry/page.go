package registry

import (
	"embed"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

//go:embed page.html
var catalogueHTML string

//go:embed site.css brand/*
var catalogueAssets embed.FS

var catalogueTemplate = template.Must(template.New("catalogue").Parse(catalogueHTML))

type catalogueRelease struct {
	Entry
	Reference   string
	Record      string
	State       ReleaseState
	Reason      string
	Replacement string
	Downloads   string
}

type cataloguePlugin struct {
	Name           string
	Description    string
	License        string
	Repository     string
	RepositoryURL  string
	Reference      string
	Path           string
	ID             string
	UpdatedAt      time.Time
	UpdatedVersion string
	Downloads      string
	DownloadCount  *int64
	Releases       []catalogueRelease
}

type catalogueView struct {
	Root      string
	Plugins   []cataloguePlugin
	Popular   []cataloguePlugin
	Recent    []cataloguePlugin
	Plugin    cataloguePlugin
	Downloads *DownloadSnapshot
}

func cataloguePlugins(index Index, statuses []ReleaseStatus, downloads *DownloadSnapshot, aliases *Aliases) ([]cataloguePlugin, error) {
	plugins := []cataloguePlugin{}
	positions := map[string]int{}
	for _, entry := range index.Entries {
		repository := entry.Submission.Provider + ".com/" + entry.Submission.Repository
		id := entry.Receipt.PluginKey()
		position, exists := positions[id]
		if !exists {
			position = len(plugins)
			positions[id] = position
			plugin := cataloguePlugin{
				Name: entry.PluginID(), ID: entry.PluginID(), Repository: repository,
				RepositoryURL: "https://" + repository, Reference: repository,
				Path: "plugins/" + id + "/index.html", Downloads: "not collected",
			}
			if aliases != nil {
				if names := aliases.For(repository); len(names) > 0 {
					plugin.Name, plugin.Reference = names[0], names[0]
				}
			}
			if plugin.Name == "" {
				plugin.Name = entry.Submission.Repository
			}
			if downloads != nil {
				plugin.DownloadCount = new(int64)
				plugin.Downloads = "unavailable"
			}
			plugins = append(plugins, plugin)
		}
		plugin := &plugins[position]
		release := catalogueRelease{
			Entry:     entry,
			Reference: plugin.Reference + "@" + entry.Submission.Tag, State: StatusActive, Downloads: "not collected",
			Record: "versions/" + key(entry.Submission.Tag) + ".json",
		}
		if status := StatusFor(entry, statuses); status != nil {
			release.State, release.Reason, release.Replacement = status.Status, status.Reason, status.Replacement
		}
		if downloads != nil {
			release.Downloads = downloads.display(entry)
			if plugin.DownloadCount != nil {
				observation := downloads.observation(entry)
				if observation == nil || observation.Status != "available" || observation.Count == nil ||
					*observation.Count < 0 || *observation.Count > math.MaxInt64-*plugin.DownloadCount {
					plugin.DownloadCount = nil
				} else {
					*plugin.DownloadCount += *observation.Count
				}
			}
		}
		plugin.Releases = append(plugin.Releases, release)
	}
	for i := range plugins {
		plugin := &plugins[i]
		slices.SortFunc(plugin.Releases, func(a, b catalogueRelease) int {
			if n := b.PublishedAt.Compare(a.PublishedAt); n != 0 {
				return n
			}
			return strings.Compare(b.Submission.Tag, a.Submission.Tag)
		})
		newest := plugin.Releases[0]
		plugin.UpdatedAt = newest.PublishedAt
		plugin.UpdatedVersion = newest.Submission.Tag
		var metadata struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			License     string `json:"license"`
		}
		var raw json.RawMessage
		if newest.Artifact != nil {
			raw = newest.Artifact.Metadata
		} else if newest.Validation != nil {
			raw = newest.Validation.Metadata
		}
		if len(raw) > 0 {
			if err := jsonv2.Unmarshal(raw, &metadata); err != nil {
				return nil, fmt.Errorf("catalogue metadata for %s: %w", plugin.Repository, err)
			}
		}
		plugin.Description, plugin.License = metadata.Description, metadata.License
		if metadata.Name != "" {
			plugin.Name = metadata.Name
		}
		if plugin.DownloadCount != nil {
			plugin.Downloads = fmt.Sprint(*plugin.DownloadCount)
		}
	}
	slices.SortFunc(plugins, func(a, b cataloguePlugin) int {
		if n := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); n != 0 {
			return n
		}
		return strings.Compare(a.Repository, b.Repository)
	})
	return plugins, nil
}

func catalogueHomepage(plugins []cataloguePlugin, downloads *DownloadSnapshot) catalogueView {
	view := catalogueView{Plugins: plugins, Downloads: downloads}
	view.Recent = slices.Clone(plugins)
	slices.SortStableFunc(view.Recent, func(a, b cataloguePlugin) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	view.Recent = view.Recent[:min(5, len(view.Recent))]
	for _, plugin := range plugins {
		if plugin.DownloadCount != nil {
			view.Popular = append(view.Popular, plugin)
		}
	}
	slices.SortStableFunc(view.Popular, func(a, b cataloguePlugin) int {
		if *a.DownloadCount < *b.DownloadCount {
			return 1
		}
		if *a.DownloadCount > *b.DownloadCount {
			return -1
		}
		return 0
	})
	view.Popular = view.Popular[:min(5, len(view.Popular))]
	return view
}

func writeCataloguePage(index Index, directory string, statuses []ReleaseStatus, downloads *DownloadSnapshot, aliases *Aliases) error {
	plugins, err := cataloguePlugins(index, statuses, downloads, aliases)
	if err != nil {
		return err
	}
	view := catalogueHomepage(plugins, downloads)
	if err := fs.WalkDir(catalogueAssets, ".", func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil || item.IsDir() {
			return walkErr
		}
		data, err := catalogueAssets.ReadFile(path)
		if err != nil {
			return err
		}
		target := filepath.Join(directory, "assets", filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	}); err != nil {
		return err
	}
	if err := writeCatalogueTemplate(filepath.Join(directory, "index.html"), "home", view); err != nil {
		return err
	}
	for _, plugin := range plugins {
		view := catalogueView{Root: "../../", Plugin: plugin, Downloads: downloads}
		if err := writeCatalogueTemplate(filepath.Join(directory, filepath.FromSlash(plugin.Path)), "plugin", view); err != nil {
			return err
		}
	}
	return nil
}

func writeCatalogueTemplate(path, name string, view catalogueView) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err = catalogueTemplate.ExecuteTemplate(f, name, view); err != nil {
		return errors.Join(err, f.Close())
	}
	return f.Close()
}
