package registry

import (
	"html"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCataloguePageInstallationAndEvidence(t *testing.T) {
	entry := acceptedFixture(t)
	index := Index{SchemaVersion: 1, Entries: []Entry{entry}}
	aliases := Aliases{SchemaVersion: 1, Aliases: map[string]AliasTarget{
		"example": {Repository: "github.com/publisher/plugin"},
		"future":  {Repository: "github.com/publisher/future", Reserved: true},
	}}
	directory := t.TempDir()
	if err := Export(index, directory, nil, &aliases); err != nil {
		t.Fatal(err)
	}
	page := readCataloguePage(t, directory)
	if strings.Contains(page, "actionlint plugin install ") || strings.Contains(page, entry.Submission.Tag) {
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
	if strings.Contains(page, "publisher/future") {
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
	status := ReleaseStatus{
		Name: "github.com/" + entry.Submission.Repository, Version: entry.Submission.Tag,
		SHA256: entry.Submission.SHA256, Status: StatusRevoked,
		Reason:      `Unexpected <script>alert("reason")</script> & unsafe behavior`,
		Replacement: `javascript:alert("replacement")`, ChangedAt: time.Now().UTC(),
		Actor: StatusActor{Kind: "registry-admin", ID: "1", Name: "maintainer",
			Workflow: "publisher/registry/.github/workflows/status.yml@refs/heads/master", RunID: "1:1"},
	}
	directory := t.TempDir()
	if err := Export(index, directory, []ReleaseStatus{status}, nil); err != nil {
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

func TestCataloguePageWithoutAliasAndEmptyRegistry(t *testing.T) {
	entry := acceptedFixture(t)
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "canonical reference", true: "empty"}[empty], func(t *testing.T) {
			index := EmptyIndex()
			if !empty {
				index.Entries = append(index.Entries, entry)
			}
			directory := t.TempDir()
			if err := Export(index, directory, nil, nil); err != nil {
				t.Fatal(err)
			}
			page := readCataloguePage(t, directory)
			if empty {
				if !strings.Contains(page, "No plugins published yet.") || !strings.Contains(page, "Publish your first plugin") {
					t.Fatal("empty registry has no publication guidance")
				}
			} else {
				page = readPluginPage(t, directory, entry)
				if !strings.Contains(page, "actionlint plugin install github.com/"+entry.Submission.Repository+"@"+entry.Submission.Tag) {
					t.Fatal("unaliased plugin lost its canonical installation reference")
				}
			}
		})
	}
}

func TestCataloguePageInstallableStatuses(t *testing.T) {
	entry := acceptedFixture(t)
	index := Index{SchemaVersion: 1, Entries: []Entry{entry}}
	for _, state := range []ReleaseState{StatusActive, StatusDeprecated, StatusWithdrawn, StatusRevoked} {
		t.Run(string(state), func(t *testing.T) {
			status := ReleaseStatus{
				Name: "github.com/" + entry.Submission.Repository, Version: entry.Submission.Tag,
				SHA256: entry.Submission.SHA256, Status: state, Reason: "Maintainer status explanation.",
				ChangedAt: time.Now().UTC(),
				Actor: StatusActor{Kind: "registry-admin", ID: "1", Name: "maintainer",
					Workflow: "publisher/registry/.github/workflows/status.yml@refs/heads/master", RunID: "1:1"},
			}
			directory := t.TempDir()
			if err := Export(index, directory, []ReleaseStatus{status}, nil); err != nil {
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
