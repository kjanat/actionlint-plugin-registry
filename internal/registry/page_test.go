package registry

import (
	"encoding/json"
	"html"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCataloguePagesLinkPluginsAndPinnedReleases(t *testing.T) {
	entry := acceptedFixture(t)
	index := Index{SchemaVersion: 1, Entries: []Entry{entry}}
	aliases := Aliases{SchemaVersion: 1, Aliases: map[string]AliasTarget{
		"example": {Repository: "github.com/publisher/plugin"},
		"future":  {Repository: "github.com/publisher/future", Reserved: true},
	}}
	directory := t.TempDir()
	if err := exportCatalogue(index, directory, nil, &aliases); err != nil {
		t.Fatal(err)
	}
	page := readCataloguePage(t, directory)
	if strings.Contains(page, "actionlint plugin install ") {
		t.Fatal("homepage lists release details instead of plugins")
	}
	pluginPath := "plugins/" + entry.Receipt.PluginKey() + "/index.html"
	if !strings.Contains(page, `href="`+pluginPath+`"`) {
		t.Fatal("homepage has no plugin detail link")
	}
	page = readPluginPage(t, directory, entry)
	record := "plugins/" + entry.Receipt.PluginKey() + "/versions/" + key(entry.Submission.Tag) + ".json"
	for _, text := range []string{
		"actionlint plugin install example@" + entry.Submission.Tag,
		`href="https://github.com/publisher/plugin"`,
		`href="` + entry.PinnedURL() + `"`,
		`href="versions/` + key(entry.Submission.Tag) + `.json"`,
		"<dd>not collected</dd>",
		"<code>" + entry.PinnedURL() + "</code>",
		"<code>" + entry.Submission.SHA256 + "</code>",
	} {
		if !strings.Contains(page, text) {
			t.Errorf("missing release information %q", text)
		}
	}
	if strings.Contains(page, "publisher/future") || strings.Contains(readCataloguePage(t, directory), "publisher/future") {
		t.Fatal("page includes an unpublished alias")
	}
	if _, err := os.Stat(filepath.Join(directory, filepath.FromSlash(record))); err != nil {
		t.Fatalf("publication record link has no target: %v", err)
	}
}

func TestCataloguePageRevocationAndEscaping(t *testing.T) {
	entry := acceptedFixture(t)
	other := entry
	other.Submission.Tag = "v2.0.0"
	index := Index{SchemaVersion: 1, Entries: []Entry{entry, other}}
	status := statusFixture(entry, StatusRevoked)
	status.Reason = `Unexpected <script>alert("reason")</script> & unsafe behavior`
	status.Replacement = `javascript:alert("replacement")`
	directory := t.TempDir()
	if err := exportCatalogue(index, directory, []ReleaseStatus{status}, nil); err != nil {
		t.Fatal(err)
	}
	page := readPluginPage(t, directory, entry)
	if strings.Count(page, `class="state revoked"`) != 2 || strings.Contains(page, "actionlint plugin install ") {
		t.Fatal("revoked bytes were offered for installation under another tag")
	}
	if !strings.Contains(page, html.EscapeString(status.Reason)) ||
		!strings.Contains(page, html.EscapeString(status.Replacement)) ||
		strings.Contains(page, "<script>") || strings.Contains(page, `href="javascript:`) {
		t.Fatal("status text was lost or rendered as executable markup")
	}
}

func TestCatalogueUnaliasedReleaseUsesCanonicalReference(t *testing.T) {
	entry := acceptedFixture(t)
	directory := t.TempDir()
	if err := exportCatalogue(Index{Entries: []Entry{entry}}, directory, nil, nil); err != nil {
		t.Fatal(err)
	}
	page := readPluginPage(t, directory, entry)
	if !strings.Contains(page, "actionlint plugin install github.com/"+entry.Submission.Repository+"@"+entry.Submission.Tag) {
		t.Fatal("unaliased plugin lost its canonical installation reference")
	}
}

func TestCatalogueEmptyRegistryShowsPublicationGuidance(t *testing.T) {
	directory := t.TempDir()
	if err := exportCatalogue(EmptyIndex(), directory, nil, nil); err != nil {
		t.Fatal(err)
	}
	page := readCataloguePage(t, directory)
	if !strings.Contains(page, "No plugins published yet.") || !strings.Contains(page, "Publish your first plugin") {
		t.Fatal("empty registry has no publication guidance")
	}
}

func TestCataloguePageInstallableStatuses(t *testing.T) {
	entry := acceptedFixture(t)
	index := Index{SchemaVersion: 1, Entries: []Entry{entry}}
	for _, state := range []ReleaseState{StatusActive, StatusDeprecated, StatusWithdrawn, StatusRevoked} {
		t.Run(string(state), func(t *testing.T) {
			status := statusFixture(entry, state)
			directory := t.TempDir()
			if err := exportCatalogue(index, directory, []ReleaseStatus{status}, nil); err != nil {
				t.Fatal(err)
			}
			page := readPluginPage(t, directory, entry)
			installable := state == StatusActive || state == StatusDeprecated
			if strings.Contains(page, "actionlint plugin install ") != installable {
				t.Fatalf("install command disagrees with %s status", state)
			}
			if !strings.Contains(page, status.Reason) {
				t.Fatal("maintainer's status explanation missing")
			}
		})
	}
}

func TestCatalogueHomepageShowsNewestPublishedVersion(t *testing.T) {
	old := acceptedFixture(t)
	old.PublishedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := changedArtifactFixture(t, old, "v2.0.0", "component", "plugin.wasm")
	newer.PublishedAt = old.PublishedAt.Add(time.Hour)
	directory := t.TempDir()
	if err := exportCatalogue(Index{Entries: []Entry{old, newer}}, directory, nil, nil); err != nil {
		t.Fatal(err)
	}
	page := readCataloguePage(t, directory)
	if !strings.Contains(page, "<code>"+newer.Submission.Tag+"</code>") || strings.Contains(page, "<code>"+old.Submission.Tag+"</code>") {
		t.Fatal("recently updated section omitted the published version or showed an old version")
	}
}

func TestCatalogueMetadataIsEscapedOnBothPages(t *testing.T) {
	entry := acceptedFixture(t)
	declaration := *entry.Artifact
	declaration.Metadata = json.RawMessage(`{"name":"<script>alert(1)</script>","description":"<img src=x onerror=alert(1)>","license":"<b>MIT</b>"}`)
	entry.Artifact = &declaration
	directory := t.TempDir()
	if err := exportCatalogue(Index{Entries: []Entry{entry}}, directory, nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, page := range []string{readCataloguePage(t, directory), readPluginPage(t, directory, entry)} {
		if strings.Contains(page, "<script>") || strings.Contains(page, "<img src=x") || !strings.Contains(page, "&lt;script&gt;") {
			t.Fatal("publisher metadata became executable markup or disappeared")
		}
	}
}

func exportCatalogue(index Index, directory string, statuses []ReleaseStatus, aliases *Aliases) error {
	if err := Export(index, directory, statuses, aliases); err != nil {
		return err
	}
	return WriteCatalogue(index, directory, statuses, nil, aliases)
}

func readCataloguePage(t *testing.T, directory string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(directory, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func readPluginPage(t *testing.T, directory string, entry Entry) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(directory, "plugins", entry.Receipt.PluginKey(), "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
