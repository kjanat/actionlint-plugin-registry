package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-github/v93/github"
	"registry.actionlint.kjanat.dev/internal/artifact"
)

func changedArtifactFixture(t *testing.T, old Entry, tag, kind, assetName string) Entry {
	t.Helper()
	entry := old
	entry.Submission.Tag, entry.Submission.Kind, entry.Submission.Asset = tag, kind, assetName
	entry.Submission.SHA256 = strings.Repeat("b", 64)
	entry.Commit = strings.Repeat("b", 40)
	entry.ReleaseID++
	entry.AssetID++
	entry.URL = assetURL(entry.Submission)
	entry.Receipt.Submission = entry.Submission
	entry.Receipt.Identity.Ref = "refs/tags/" + tag
	entry.Receipt.Identity.Commit = entry.Commit
	declaration := *old.Artifact
	declaration.Kind, declaration.SHA256 = kind, entry.Submission.SHA256
	declaration.PluginVersion = strings.TrimPrefix(tag, "v")
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(declaration.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	version, err := json.Marshal(declaration.PluginVersion)
	if err != nil {
		t.Fatal(err)
	}
	metadata["plugin_version"] = version
	if kind == "component" {
		delete(metadata, "abi_version")
		metadata["transport"] = json.RawMessage(`"wasm-component"`)
		metadata["interface"] = json.RawMessage(`"example:plugin/analyzer@1.0.0"`)
		metadata["contract_sha256"] = json.RawMessage(`"` + strings.Repeat("c", 64) + `"`)
		declaration.Contract = "example:plugin/analyzer@1.0.0#sha256=" + strings.Repeat("c", 64)
	}
	declaration.Metadata, err = json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	entry.Artifact = &declaration
	return entry
}

func TestArtifactMigrationKeepsHistoricalVersions(t *testing.T) {
	old := acceptedFixture(t)
	newVersion := changedArtifactFixture(t, old, "v2.0.0", "component", "plugin.component.wasm")
	store := Store{Directory: filepath.Join(t.TempDir(), "plugins")}
	if err := store.Merge(Index{SchemaVersion: 1, Entries: []Entry{old}}); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(store.Directory, old.Receipt.PluginKey())
	oldPath := filepath.Join(base, "versions", key(old.Submission.Tag)+".json")
	original, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	pluginPath := filepath.Join(base, "plugin.json")
	originalPlugin, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Merge(Index{SchemaVersion: 1, Entries: []Entry{newVersion}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Entries) != 2 {
		t.Fatalf("got %d versions, want 2", len(loaded.Entries))
	}
	after, err := os.ReadFile(oldPath)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("new release rewrote historical version bytes")
	}
	afterPlugin, err := os.ReadFile(pluginPath)
	if err != nil || !bytes.Equal(originalPlugin, afterPlugin) {
		t.Fatal("new release rewrote publisher identity")
	}
	output := t.TempDir()
	if err := Export(loaded, output, nil); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(output, "plugins", old.Receipt.PluginKey(), "index.json")
	var manifest PluginManifest
	if err := readRecord(manifestPath, &manifest); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Plugin != pluginFor(old) || manifest.Plugin.SchemaVersion != 1 || len(manifest.Versions) != 2 ||
		bytes.Contains(raw, []byte(`"asset"`)) || bytes.Contains(raw, []byte(`"kind"`)) {
		t.Fatalf("artifact leaked into publisher identity: %s", raw)
	}
	for _, expected := range []Entry{old, newVersion} {
		resolved, err := Resolve(loaded, "github.com/publisher/plugin", expected.Submission.Tag)
		if err != nil || resolved != expected.PinnedURL() {
			t.Fatalf("wrong historical resolution %s: %s, %v", expected.Submission.Tag, resolved, err)
		}
		var exported Entry
		path := filepath.Join(filepath.Dir(manifestPath), "versions", key(expected.Submission.Tag)+".json")
		if err := readRecord(path, &exported); err != nil {
			t.Fatal(err)
		}
		if exported.Submission != expected.Submission || exported.Artifact.Kind != expected.Artifact.Kind {
			t.Fatal("export did not preserve the selected release's artifact")
		}
	}
}

func TestArtifactMigrationRejectsIdentityAndVersionReplacement(t *testing.T) {
	old := acceptedFixture(t)
	newVersion := changedArtifactFixture(t, old, "v2.0.0", "component", "plugin.component.wasm")
	for name, mutate := range map[string]func(*Entry){
		"owner": func(entry *Entry) {
			entry.OwnerID++
			entry.Receipt.Identity.OwnerID = "6"
		},
		"repository": func(entry *Entry) {
			entry.RepositoryID++
			entry.Receipt.Identity.RepositoryID = "11"
		},
		"plugin": func(entry *Entry) {
			entry.Artifact.PluginID = "replacement"
			entry.Artifact.Metadata = bytes.Replace(entry.Artifact.Metadata, []byte(`"id":"example"`), []byte(`"id":"replacement"`), 1)
		},
		"same-version": func(entry *Entry) {
			*entry = changedArtifactFixture(t, old, old.Submission.Tag, "component", "plugin.component.wasm")
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := Store{Directory: filepath.Join(t.TempDir(), "plugins")}
			if err := store.Merge(Index{SchemaVersion: 1, Entries: []Entry{old}}); err != nil {
				t.Fatal(err)
			}
			changed := newVersion
			copyArtifact := *newVersion.Artifact
			changed.Artifact = &copyArtifact
			mutate(&changed)
			if err := store.Merge(Index{SchemaVersion: 1, Entries: []Entry{changed}}); err == nil {
				t.Fatal("accepted replacement")
			}
			loaded, err := store.Load()
			if err != nil || len(loaded.Entries) != 1 || loaded.Entries[0].Submission != old.Submission {
				t.Fatalf("rejection modified store: %v", err)
			}
		})
	}
}

func TestArtifactSelectionDoesNotChangePublisherManifest(t *testing.T) {
	old := acceptedFixture(t)
	component := changedArtifactFixture(t, old, "v2.0.0", "component", "plugin.component.wasm")
	coreAgain := changedArtifactFixture(t, old, "v3.0.0", "core", "plugin.wasm")
	for _, entries := range [][]Entry{{old, component, coreAgain}, {component, coreAgain, old}} {
		output := t.TempDir()
		if err := Export(Index{SchemaVersion: 1, Entries: entries}, output, nil); err != nil {
			t.Fatal(err)
		}
		var manifest PluginManifest
		if err := readRecord(filepath.Join(output, "plugins", old.Receipt.PluginKey(), "index.json"), &manifest); err != nil {
			t.Fatal(err)
		}
		if manifest.Plugin != pluginFor(old) || len(manifest.Versions) != len(entries) {
			t.Fatal("publisher manifest depends on artifact selection or registration order")
		}
	}
}

func TestPublisherManifestRejectsArtifactFields(t *testing.T) {
	old := acceptedFixture(t)
	for _, field := range []string{`"asset":"plugin.wasm"`, `"kind":"core"`} {
		store := Store{Directory: filepath.Join(t.TempDir(), "plugins")}
		if err := store.Merge(Index{SchemaVersion: 1, Entries: []Entry{old}}); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(pluginFor(old))
		if err != nil {
			t.Fatal(err)
		}
		data = append(data[:len(data)-1], []byte(","+field+"}")...)
		if err := os.WriteFile(filepath.Join(store.Directory, old.Receipt.PluginKey(), "plugin.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Load(); err == nil {
			t.Fatal("publisher manifest accepted release artifact fields")
		}
	}
}

type migrationProvider struct{ entry Entry }

func (p migrationProvider) ResolveRelease(_ context.Context, submission Submission) (Release, error) {
	if submission != p.entry.Submission {
		return Release{}, errors.New("unexpected submission")
	}
	return Release{Entry: p.entry}, nil
}

func (p migrationProvider) DownloadRelease(_ context.Context, _ Submission, _ Release, path string) error {
	return os.WriteFile(path, []byte("fixture inspected by the test inspector"), 0o600)
}

func TestAdmissionAcceptsNewVersionArtifactMigration(t *testing.T) {
	old := acceptedFixture(t)
	next := changedArtifactFixture(t, old, "v2.0.0", "component", "plugin.component.wasm")
	index := Index{SchemaVersion: 1, Entries: []Entry{old}}
	service := Service{
		Providers: map[string]ReleaseProvider{"github": migrationProvider{next}},
		Inspect: func(context.Context, string) (artifact.Declaration, error) {
			return *next.Artifact, nil
		},
	}
	if _, err := service.Admit(t.Context(), &index, next.Receipt); err != nil {
		t.Fatal(err)
	}
	if len(index.Entries) != 2 {
		t.Fatal("new artifact version was not admitted")
	}
	replacement := changedArtifactFixture(t, old, old.Submission.Tag, "component", "plugin.component.wasm")
	service.Providers["github"] = migrationProvider{replacement}
	if _, err := service.Admit(t.Context(), &index, replacement.Receipt); err == nil || FailureDisposition(err) != "rejected" {
		t.Fatalf("same-version replacement must be rejected: %v", err)
	}
}

func TestDiscoveryUsesExplicitArtifactSelection(t *testing.T) {
	for _, componentFirst := range []bool{false, true} {
		client := fixtureGitHub(t, func(release *github.RepositoryRelease) {
			component := *release.Assets[0]
			component.Name = new("plugin.component.wasm")
			release.Assets = append(release.Assets, &component)
			if componentFirst {
				release.Assets[0], release.Assets[1] = release.Assets[1], release.Assets[0]
			}
		})
		source := sampleSubmission().Source()
		source.Kind, source.Asset = "component", "plugin.component.wasm"
		candidates, err := client.Candidates(t.Context(), source)
		if err != nil {
			t.Fatal(err)
		}
		if len(candidates) != 1 || candidates[0].Kind != source.Kind || candidates[0].Asset != source.Asset {
			t.Fatal("release asset order changed the selected artifact")
		}
	}
}

func TestAdmissionRejectsPluginIdentityReplacement(t *testing.T) {
	old := acceptedFixture(t)
	next := changedArtifactFixture(t, old, "v2.0.0", "component", "plugin.component.wasm")
	next.Artifact.PluginID = "replacement"
	next.Artifact.Metadata = bytes.Replace(next.Artifact.Metadata, []byte(`"id":"example"`), []byte(`"id":"replacement"`), 1)
	if err := next.Artifact.Validate(); err != nil {
		t.Fatal(err)
	}
	index := Index{SchemaVersion: 1, Entries: []Entry{old}}
	service := Service{
		Providers: map[string]ReleaseProvider{"github": migrationProvider{next}},
		Inspect: func(context.Context, string) (artifact.Declaration, error) {
			return *next.Artifact, nil
		},
	}
	_, err := service.Admit(t.Context(), &index, next.Receipt)
	if err == nil || FailureDisposition(err) != "rejected" {
		t.Fatalf("plugin identity replacement must be permanently rejected: %v", err)
	}
	if len(index.Entries) != 1 || index.Entries[0].PluginID() != old.PluginID() {
		t.Fatal("rejected plugin identity replacement modified the index")
	}
}
