package registry

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCatalogueGroupsVersionsAndUsesNewestMetadata(t *testing.T) {
	old := acceptedFixture(t)
	old.PublishedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := changedArtifactFixture(t, old, "v2.0.0", "component", "plugin.wasm")
	newer.PublishedAt = old.PublishedAt.Add(time.Hour)
	declaration := *newer.Artifact
	declaration.Metadata = json.RawMessage(`{"name":"Updated name","description":"Updated description","license":"MIT"}`)
	newer.Artifact = &declaration
	plugins, err := cataloguePlugins(Index{Entries: []Entry{old, newer}}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plugins) != 1 || len(plugins[0].Releases) != 2 {
		t.Fatalf("versions duplicated the plugin: %+v", plugins)
	}
	plugin := plugins[0]
	if plugin.Name != "Updated name" || plugin.Description != "Updated description" || plugin.License != "MIT" ||
		!plugin.UpdatedAt.Equal(newer.PublishedAt) || plugin.UpdatedVersion != newer.Submission.Tag || plugin.Releases[0].Submission.Tag != newer.Submission.Tag {
		t.Fatalf("plugin metadata did not follow newest publication: %+v", plugin)
	}
	if plugin.DownloadCount != nil || plugin.Downloads != "not collected" {
		t.Fatal("absent statistics were treated as zero downloads")
	}
	view := catalogueHomepage(plugins, nil)
	if len(view.Popular) != 0 || len(view.Recent) != 1 {
		t.Fatal("missing statistics prevented recent discovery or produced a ranking")
	}
}

func TestCataloguePopularityRequiresCompleteCounts(t *testing.T) {
	base := acceptedFixture(t)
	index := EmptyIndex()
	snapshot := DownloadSnapshot{SchemaVersion: 1, ObservedAt: time.Now().UTC()}
	// Input order differs from every discovery order. One publisher has two releases.
	for i, name := range []string{"partial", "zero", "popular", "recent", "unknown", "sum"} {
		entry := base
		entry.Submission.Repository = "publisher/" + name
		entry.RepositoryID = int64(i + 100)
		entry.Receipt.Identity.RepositoryID = strconv.FormatInt(entry.RepositoryID, 10)
		entry.PublishedAt = time.Date(2026, 1, i+1, 0, 0, 0, 0, time.UTC)
		declaration := *entry.Artifact
		declaration.PluginID = name
		declaration.Metadata = json.RawMessage(`{"name":"` + name + `"}`)
		entry.Artifact = &declaration
		index.Entries = append(index.Entries, entry)
		count := int64(i * 10)
		if name == "zero" {
			count = 0
		}
		if name == "popular" {
			count = 100
		}
		observation := DownloadObservation{
			Provider: entry.Submission.Provider, RepositoryID: entry.RepositoryID,
			Version: entry.Submission.Tag, AssetID: entry.AssetID, SHA256: entry.Submission.SHA256,
			Status: "available", Count: &count,
		}
		if name == "unknown" {
			observation.Status, observation.Count = "unavailable", nil
		}
		snapshot.Releases = append(snapshot.Releases, observation)
		if name == "partial" || name == "sum" {
			other := entry
			other.Submission.Tag = "v2.0.0"
			other.PublishedAt = entry.PublishedAt.Add(time.Hour)
			index.Entries = append(index.Entries, other)
			observation.Version = other.Submission.Tag
			if name == "partial" {
				observation.Status, observation.Count = "unavailable", nil
			}
			snapshot.Releases = append(snapshot.Releases, observation)
		}
	}
	plugins, err := cataloguePlugins(index, nil, &snapshot, nil)
	if err != nil {
		t.Fatal(err)
	}
	view := catalogueHomepage(plugins, &snapshot)
	names := func(items []cataloguePlugin) []string {
		result := []string{}
		for _, plugin := range items {
			result = append(result, plugin.Name)
		}
		return result
	}
	if got := names(view.Plugins); !reflect.DeepEqual(got, []string{"partial", "popular", "recent", "sum", "unknown", "zero"}) {
		t.Fatalf("plugin listing: %v", got)
	}
	if got := names(view.Popular); !reflect.DeepEqual(got, []string{"popular", "sum", "recent", "zero"}) {
		t.Fatalf("popularity included unknown counts or sorted incorrectly: %v", got)
	}
	if got := names(view.Recent); !reflect.DeepEqual(got, []string{"sum", "unknown", "recent", "popular", "zero"}) {
		t.Fatalf("recent publications: %v", got)
	}
	for _, plugin := range plugins {
		if (plugin.Name == "partial" || plugin.Name == "unknown") && (plugin.DownloadCount != nil || plugin.Downloads != "unavailable") {
			t.Fatalf("incomplete statistics presented as a total: %+v", plugin)
		}
	}
}

func TestCatalogueMetadataIgnoresCaseVariantExtensions(t *testing.T) {
	for name, extension := range map[string]string{
		"typed extensions":  `"Name":{"future":"extension"},"Description":["future"],"License":{"id":"future"}`,
		"string extensions": `"Name":"Wrong name","Description":"Wrong description","License":"Wrong license"`,
	} {
		t.Run(name, func(t *testing.T) {
			entry := acceptedFixture(t)
			declaration := *entry.Artifact
			declaration.Metadata = json.RawMessage(strings.TrimSuffix(string(declaration.Metadata), "}") + "," + extension + "}")
			if err := declaration.Validate(); err != nil {
				t.Fatalf("admission rejected extension fixture: %v", err)
			}
			entry.Artifact = &declaration
			index := Index{Entries: []Entry{entry}}
			plugins, err := cataloguePlugins(index, nil, nil, nil)
			if err != nil || len(plugins) != 1 {
				t.Fatalf("missing plugin: %v, %v", plugins, err)
			}
			if plugin := plugins[0]; plugin.Name != "Example" || plugin.Description != "Example plugin" || plugin.License != "MIT" {
				t.Fatalf("case variants replaced declared metadata: name=%q, description=%q, license=%q", plugin.Name, plugin.Description, plugin.License)
			}
		})
	}
}
